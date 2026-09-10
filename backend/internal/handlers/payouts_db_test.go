package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

// DB-gated tests for the owner-payout batch: settlement matrix legs (close /
// payout_only / withhold / revive), ledger idempotency, and the escrow aging
// mechanics. Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestPayouts -v
//
// The database must be migrated through 000049. Stripe is nil throughout:
// every asserted path either never reaches Stripe by design (owner not
// payout-ready → awaiting_onboarding) or takes the zero-refund fast path.

type payoutEnv struct {
	*lifecycleEnv
	payoutRepo *repository.PayoutRepository
	payoutH    *PayoutHandler
}

const payoutTestFeeBPS = 1000 // matches prod PLATFORM_FEE_BPS

func newPayoutEnv(t *testing.T) *payoutEnv {
	t.Helper()
	e := newLifecycleEnv(t)
	logger := discardLogger()
	payoutRepo := repository.NewPayoutRepository(e.db)
	notifH := NewNotificationHandler(
		repository.NewNotificationRepository(e.db),
		repository.NewDeviceTokenRepository(e.db),
		ws.NewHub(logger), nil, logger)
	payoutH := NewPayoutHandler(payoutRepo, e.leaseRepo,
		repository.NewUserRepository(e.db), e.ticketRepo,
		nil, nil, notifH, payoutTestFeeBPS, logger)
	e.returnH.SetPayoutHandler(payoutH)
	return &payoutEnv{lifecycleEnv: e, payoutRepo: payoutRepo, payoutH: payoutH}
}

// seedPayment gives the lease a real paid amount (no PaymentIntent — the
// awaiting path never dereferences it).
func (e *payoutEnv) seedPayment(t *testing.T, leaseID uuid.UUID, amountCents int64) {
	t.Helper()
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO payments (id, lease_request_id, provider, amount, currency, platform_fee_amount, status, created_at, updated_at)
		VALUES ($1, $2, 'stripe', $3, 'USD', 0, 'succeeded', NOW(), NOW())`,
		id, leaseID, amountCents); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(context.Background(), `DELETE FROM payments WHERE id = $1`, id) })
}

// cleanupLedger registers the owner_payouts delete FIRST-out (both FKs are
// RESTRICT — the row must go before the lease and users can).
func (e *payoutEnv) cleanupLedger(t *testing.T, leaseID uuid.UUID) {
	t.Helper()
	t.Cleanup(func() {
		e.db.Pool.Exec(context.Background(), `DELETE FROM support_tickets WHERE lease_request_id = $1`, leaseID)
		e.db.Pool.Exec(context.Background(), `DELETE FROM owner_payouts WHERE lease_request_id = $1`, leaseID)
	})
}

func settleReq(t *testing.T, adminID, leaseID uuid.UUID, body string) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", leaseID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/rents/"+leaseID.String()+"/settle", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, adminID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// close: an admin-authored completion of a rental with no return row. The
// return row is created, resolved, finalized (zero-refund fast path), and
// exactly one ledger row appears — awaiting_onboarding (owner not ready),
// correct split, source admin_settlement, note recorded. A second close is
// refused.
func TestPayouts_AdminSettleClose(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "po_owner_c@example.com")
	driver := e.seedUser(t, "driver", "po_driver_c@example.com")
	admin := e.seedUser(t, "admin", "po_admin_c@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)
	e.seedPayment(t, leaseID, 30000)
	e.cleanupLedger(t, leaseID)

	rr := httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"close","driver_refund_cents":0,"note":"Overdue-escalated; car recovered by owner offline."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("settle close: %d (%s)", rr.Code, rr.Body.String())
	}

	ret, err := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if err != nil || ret == nil {
		t.Fatalf("load return: %v", err)
	}
	if ret.Status != models.VehicleReturnCompleted {
		t.Errorf("return status = %s, want completed", ret.Status)
	}

	var carStatus string
	if err := e.db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id = $1`, carID).Scan(&carStatus); err != nil {
		t.Fatalf("car status: %v", err)
	}
	if carStatus != "available" {
		t.Errorf("car status = %s, want available (close releases the car)", carStatus)
	}

	row, err := e.payoutRepo.GetByLeaseRequestID(ctx, leaseID)
	if err != nil || row == nil {
		t.Fatalf("ledger row: %v", err)
	}
	if row.Status != models.PayoutAwaitingOnboarding {
		t.Errorf("ledger status = %s, want awaiting_onboarding", row.Status)
	}
	if row.GrossKeptCents != 30000 || row.FeeCents != 3000 || row.OwnerAmountCents != 27000 {
		t.Errorf("split = %d/%d/%d, want 30000/3000/27000",
			row.GrossKeptCents, row.FeeCents, row.OwnerAmountCents)
	}
	if row.Source != models.PayoutSourceAdminSettlement {
		t.Errorf("source = %s, want admin_settlement", row.Source)
	}
	if row.Note == nil || *row.Note == "" {
		t.Error("settlement note not recorded on ledger row")
	}

	// Second close: the return is completed — refused, ledger untouched.
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"close","note":"double-tap should not double-settle"}`))
	if rr.Code != http.StatusConflict {
		t.Errorf("second close = %d, want 409", rr.Code)
	}
}

// withhold → the deliberate-non-payment leg, then payout_only reverses it
// (withheld is not a dead end). Withholding money that already moved is
// refused.
func TestPayouts_AdminSettleWithholdAndRevive(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "po_owner_w@example.com")
	driver := e.seedUser(t, "driver", "po_driver_w@example.com")
	admin := e.seedUser(t, "admin", "po_admin_w@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.seedPayment(t, leaseID, 20000)
	e.cleanupLedger(t, leaseID)

	rr := httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"withhold","note":"Owner listing was fraudulent; funds held pending review."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("withhold: %d (%s)", rr.Code, rr.Body.String())
	}
	row, err := e.payoutRepo.GetByLeaseRequestID(ctx, leaseID)
	if err != nil || row == nil {
		t.Fatalf("ledger row: %v", err)
	}
	if row.Status != models.PayoutWithheld {
		t.Errorf("status = %s, want withheld", row.Status)
	}
	if row.Note == nil || *row.Note == "" {
		t.Error("withhold reason not recorded")
	}

	// Reversal: payout_only on the withheld row re-opens it. Owner isn't
	// payout-ready, so the engine parks it awaiting_onboarding — no Stripe.
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"payout_only","note":"Review cleared the owner; releasing the payout."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("revive: %d (%s)", rr.Code, rr.Body.String())
	}
	row, _ = e.payoutRepo.GetByLeaseRequestID(ctx, leaseID)
	if row.Status != models.PayoutAwaitingOnboarding {
		t.Errorf("after revive status = %s, want awaiting_onboarding", row.Status)
	}

	// Force the row to paid, then withhold again — must be refused.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE owner_payouts SET status = 'paid' WHERE lease_request_id = $1`, leaseID); err != nil {
		t.Fatalf("force paid: %v", err)
	}
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"withhold","note":"too late — money already moved"}`))
	if rr.Code != http.StatusConflict {
		t.Errorf("withhold after paid = %d, want 409", rr.Code)
	}
}

// The ledger is idempotent per lease: a second Create returns the existing
// row untouched — the UNIQUE(lease_request_id) guard that makes double
// payouts structurally impossible.
func TestPayouts_LedgerIdempotentPerLease(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "po_owner_i@example.com")
	driver := e.seedUser(t, "driver", "po_driver_i@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)

	mk := func(amount int64) *models.OwnerPayout {
		fee, ownerC := models.ComputePayoutSplit(amount, payoutTestFeeBPS)
		return &models.OwnerPayout{
			LeaseRequestID: &leaseID, OwnerID: owner,
			GrossKeptCents: amount, FeeBPS: payoutTestFeeBPS,
			FeeCents: fee, OwnerAmountCents: ownerC, Currency: "USD",
			Status: models.PayoutAwaitingOnboarding, Source: models.PayoutSourceReturnCompleted,
		}
	}
	first, created, err := e.payoutRepo.Create(ctx, mk(10000))
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	second, created, err := e.payoutRepo.Create(ctx, mk(99999))
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created {
		t.Error("second create reported created=true — double payout possible")
	}
	if second.ID != first.ID || second.GrossKeptCents != 10000 {
		t.Errorf("second create returned %s/%d, want original %s/10000",
			second.ID, second.GrossKeptCents, first.ID)
	}
}

// Escrow aging: a reminder claims exactly once per cadence window, and a
// stale balance escalates to exactly one ticket.
func TestPayouts_EscrowReminderAndEscalation(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "po_owner_e@example.com")
	driver := e.seedUser(t, "driver", "po_driver_e@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)

	fee, ownerC := models.ComputePayoutSplit(15000, payoutTestFeeBPS)
	row, _, err := e.payoutRepo.Create(ctx, &models.OwnerPayout{
		LeaseRequestID: &leaseID, OwnerID: owner,
		GrossKeptCents: 15000, FeeBPS: payoutTestFeeBPS,
		FeeCents: fee, OwnerAmountCents: ownerC, Currency: "USD",
		Status: models.PayoutAwaitingOnboarding, Source: models.PayoutSourceReturnCompleted,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Age the row past both thresholds.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE owner_payouts SET created_at = NOW() - INTERVAL '61 days' WHERE id = $1`, row.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	cutoff := time.Now().UTC().Add(-models.PayoutEscrowReminderEvery)
	claimed, err := e.payoutRepo.ClaimEscrowReminders(ctx, cutoff, cutoff, 50)
	if err != nil {
		t.Fatalf("claim reminders: %v", err)
	}
	found := false
	for _, c := range claimed {
		if c.ID == row.ID {
			found = true
			if c.ReminderCount != 1 {
				t.Errorf("reminder_count = %d, want 1", c.ReminderCount)
			}
		}
	}
	if !found {
		t.Fatal("aged awaiting row not claimed for reminder")
	}
	// Second claim in the same window: nothing (claimed-once).
	claimed, _ = e.payoutRepo.ClaimEscrowReminders(ctx, cutoff, cutoff, 50)
	for _, c := range claimed {
		if c.ID == row.ID {
			t.Error("row claimed twice in one cadence window")
		}
	}

	// Escalation: candidate once, then never again after the flag.
	stale, err := e.payoutRepo.ListEscrowEscalationCandidates(ctx,
		time.Now().UTC().Add(-models.PayoutEscrowEscalateAfter), 50)
	if err != nil {
		t.Fatalf("escalation candidates: %v", err)
	}
	found = false
	for _, s := range stale {
		if s.ID == row.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("61-day-old balance not an escalation candidate")
	}
	if err := e.payoutRepo.MarkEscrowEscalated(ctx, row.ID); err != nil {
		t.Fatalf("mark escalated: %v", err)
	}
	stale, _ = e.payoutRepo.ListEscrowEscalationCandidates(ctx,
		time.Now().UTC().Add(-models.PayoutEscrowEscalateAfter), 50)
	for _, s := range stale {
		if s.ID == row.ID {
			t.Error("escalated row still a candidate — would open duplicate tickets")
		}
	}
}

// The status mapping the app and webhook both rely on (pure).
func TestPayouts_MapAccountStatus(t *testing.T) {
	acct := func(mut func(*stripeService.ConnectedAccount)) *stripeService.ConnectedAccount {
		a := &stripeService.ConnectedAccount{ID: "acct_test"}
		mut(a)
		return a
	}
	strPtr := func(s string) *string { return &s }

	cases := []struct {
		name string
		in   *stripeService.ConnectedAccount
		want models.UserPayoutStatus
	}{
		{"fresh account", acct(func(a *stripeService.ConnectedAccount) {}), models.PayoutAccountOnboarding},
		{"ready", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted, a.PayoutsEnabled = true, true
		}), models.PayoutAccountReady},
		{"enabled but new requirements due", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted, a.PayoutsEnabled = true, true
			a.Requirements.CurrentlyDue = []string{"individual.id_number"}
		}), models.PayoutAccountActionNeeded},
		{"submitted, stripe reviewing", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted = true
			a.Requirements.PendingVerification = []string{"individual.verification.document"}
		}), models.PayoutAccountPendingVerif},
		{"submitted, more info needed", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted = true
			a.Requirements.CurrentlyDue = []string{"individual.ssn_last_4"}
		}), models.PayoutAccountActionNeeded},
		{"disabled: pending verification", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted = true
			a.Requirements.DisabledReason = strPtr("requirements.pending_verification")
		}), models.PayoutAccountPendingVerif},
		{"disabled: past due", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted = true
			a.Requirements.DisabledReason = strPtr("requirements.past_due")
		}), models.PayoutAccountActionNeeded},
		{"rejected", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted = true
			a.Requirements.DisabledReason = strPtr("rejected.fraud")
		}), models.PayoutAccountRestricted},
		{"under review", acct(func(a *stripeService.ConnectedAccount) {
			a.DetailsSubmitted, a.PayoutsEnabled = true, true
			a.Requirements.DisabledReason = strPtr("under_review")
		}), models.PayoutAccountRestricted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapAccountStatus(tc.in); got != tc.want {
				t.Errorf("mapAccountStatus = %s, want %s", got, tc.want)
			}
		})
	}
}
