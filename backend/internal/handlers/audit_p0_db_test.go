package handlers

import (
	"context"
	"encoding/json"
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
)

// DB-gated tests for the audit P0 batch (M3/H2/H7 — captured charges on
// terminal leases, lost-success reconciliation, unrecoverable expiry
// refunds). Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestAuditP0 -v
//
// The database must be migrated through 000052. Stripe is nil throughout:
// every asserted path is deliberately pre-Stripe (the payment-window
// serializer, the no-intent park path, adoption of an already-succeeded
// payment, repo claim semantics).

// seedAcceptedLease: request + owner accept, nothing paid.
func seedAcceptedLease(t *testing.T, e *payoutEnv, owner, driver uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	car := e.seedCar(t, owner, "available", true, false)
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed create: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID uuid.UUID `json:"id"`
		} `json:"lease_request"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	leaseID := created.LeaseRequest.ID
	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("seed accept: %v", err)
	}
	return leaseID, car
}

// seedPaymentAt inserts a payment row at an arbitrary status, optionally
// with a (fake) intent id — the knob the payoutEnv seedPayment lacks.
func seedPaymentAt(t *testing.T, e *payoutEnv, leaseID uuid.UUID, amountCents int64, status string, intentID *string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO payments (id, lease_request_id, provider, payment_intent_id, amount, currency, platform_fee_amount, status, created_at, updated_at)
		VALUES ($1, $2, 'stripe', $3, $4, 'USD', 0, $5, NOW() - interval '10 minutes', NOW() - interval '10 minutes')`,
		id, leaseID, intentID, amountCents, status); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(context.Background(), `DELETE FROM payments WHERE id = $1`, id) })
	return id
}

func paymentIntentReq(t *testing.T, userID, leaseID uuid.UUID) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", leaseID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/lease-requests/"+leaseID.String()+"/payments/intent", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// M3a: the payment-window transition and the accept-expiry claim are a
// strict one-winner pair, and a lease the sweep already expired can never
// mint a payment.
func TestAuditP0_PaymentWindowSerializer(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "p0_owner_s@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_s@example.com")
	e.seedLicense(t, driver)

	// Direction 1: sweep wins → window claim refuses → handler refuses →
	// zero payment rows.
	leaseA, _ := seedAcceptedLease(t, e, owner, driver)
	if _, err := e.leaseRepo.ClaimAcceptExpiry(ctx, leaseA); err != nil {
		t.Fatalf("claim accept expiry: %v", err)
	}
	if _, err := e.leaseRepo.SetPaymentPending(ctx, leaseA); err == nil {
		t.Fatal("SetPaymentPending succeeded on an expired lease — the M3a serializer is broken")
	}
	rr := httptest.NewRecorder()
	e.leaseH.CreatePaymentIntent(rr, paymentIntentReq(t, driver, leaseA))
	if rr.Code < 400 {
		t.Fatalf("CreatePaymentIntent on expired lease = %d, want 4xx", rr.Code)
	}
	var n int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE lease_request_id = $1`, leaseA).Scan(&n)
	if n != 0 {
		t.Fatalf("expired lease has %d payment rows, want 0", n)
	}

	// Direction 2: window claim wins → sweep claim refuses.
	leaseB, _ := seedAcceptedLease(t, e, owner, driver)
	if _, err := e.leaseRepo.SetPaymentPending(ctx, leaseB); err != nil {
		t.Fatalf("SetPaymentPending: %v", err)
	}
	if _, err := e.leaseRepo.ClaimAcceptExpiry(ctx, leaseB); err == nil {
		t.Fatal("ClaimAcceptExpiry succeeded on a payment_pending lease — the sweep could kill an open window")
	}
}

// M3 core: a succeeded charge on a terminal lease is parked (claimed-once,
// one ticket), never silently kept — here via the no-intent path, which is
// the permanent-failure arm and needs no Stripe.
func TestAuditP0_TerminalLeaseChargeParkedNotKept(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "p0_owner_t@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_t@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := seedAcceptedLease(t, e, owner, driver)
	e.cleanupLedger(t, leaseID)
	payID := seedPaymentAt(t, e, leaseID, 30000, "succeeded", nil)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET status='cancelled' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("force cancel: %v", err)
	}

	payment, err := e.leaseRepo.GetPaymentByLeaseRequestID(ctx, leaseID)
	if err != nil || payment == nil {
		t.Fatalf("load payment: %v", err)
	}
	e.leaseH.adoptSucceededPayment(ctx, payment)

	var status string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM payments WHERE id=$1`, payID).Scan(&status)
	if status != "refund_unrecoverable" {
		t.Fatalf("payment status = %q, want refund_unrecoverable", status)
	}
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("live tickets = %d, want exactly 1", tickets)
	}

	// Second pass: claimed-once — no duplicate ticket, status unchanged.
	payment.Status = models.PaymentStatusSucceeded // stale in-memory copy, as a crashed retry would have
	e.leaseH.adoptSucceededPayment(ctx, payment)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("after replay: live tickets = %d, want 1", tickets)
	}

	// And the orphan lister no longer offers it.
	orphans, err := e.leaseRepo.ListOrphanedSucceededPayments(ctx, time.Now().Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("list orphans: %v", err)
	}
	for _, o := range orphans {
		if o.Payment.ID == payID {
			t.Fatal("parked payment still listed as an orphan — the sweep would hammer it forever")
		}
	}
}

// M3 backstop: the orphan lister finds exactly the bad combination
// (succeeded payment + terminal lease) and the refunded mark claims once.
func TestAuditP0_OrphanListerAndClaims(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "p0_owner_o@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_o@example.com")
	e.seedLicense(t, driver)

	// The orphan: succeeded payment, declined lease.
	orphanLease, _ := seedAcceptedLease(t, e, owner, driver)
	intent := "pi_test_orphan_" + orphanLease.String()[:8]
	orphanPay := seedPaymentAt(t, e, orphanLease, 15000, "succeeded", &intent)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET status='declined' WHERE id=$1`, orphanLease); err != nil {
		t.Fatalf("force decline: %v", err)
	}

	// The control: succeeded payment, PAID lease (a normal active rental).
	controlLease, _ := e.seedActiveRental(t, owner, driver)
	controlPay := seedPaymentAt(t, e, controlLease, 15000, "succeeded", nil)

	orphans, err := e.leaseRepo.ListOrphanedSucceededPayments(ctx, time.Now().Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("list orphans: %v", err)
	}
	foundOrphan, foundControl := false, false
	for _, o := range orphans {
		if o.Payment.ID == orphanPay {
			foundOrphan = true
			if o.LeaseStatus != models.LeaseStatusDeclined {
				t.Errorf("orphan lease status = %s", o.LeaseStatus)
			}
		}
		if o.Payment.ID == controlPay {
			foundControl = true
		}
	}
	if !foundOrphan {
		t.Fatal("succeeded payment on a declined lease NOT listed — M3's backstop is blind")
	}
	if foundControl {
		t.Fatal("payment on a paid lease listed as orphan — the sweep would refund an active rental")
	}

	claimed, err := e.leaseRepo.MarkPaymentRefunded(ctx, orphanPay)
	if err != nil || !claimed {
		t.Fatalf("first MarkPaymentRefunded = %v/%v, want true", claimed, err)
	}
	claimed, _ = e.leaseRepo.MarkPaymentRefunded(ctx, orphanPay)
	if claimed {
		t.Fatal("second MarkPaymentRefunded claimed — the driver would be double-notified")
	}
}

// H7: a pickup-expiry refund with no PaymentIntent is PERMANENT — one
// unrecoverable mark, one ticket, out of the retry sweep. (The old behavior
// marked 'failed' and replayed the dead end every tick, with the driver
// already told the money was back.)
func TestAuditP0_ExpiryRefundUnrecoverable(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "p0_owner_h7@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_h7@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := seedAcceptedLease(t, e, owner, driver)
	e.cleanupLedger(t, leaseID)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET status='expired_refunded', refund_status='failed' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("force expired_refunded: %v", err)
	}

	lr, err := e.leaseRepo.GetByID(ctx, leaseID)
	if err != nil {
		t.Fatalf("load lease: %v", err)
	}
	e.leaseH.issueAndFinalizeRefund(ctx, lr, "test")

	var rs *string
	e.db.Pool.QueryRow(ctx, `SELECT refund_status FROM lease_requests WHERE id=$1`, leaseID).Scan(&rs)
	if rs == nil || *rs != "unrecoverable" {
		t.Fatalf("refund_status = %v, want unrecoverable", rs)
	}
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("live tickets = %d, want exactly 1", tickets)
	}

	// Out of the sweep: ListStuckRefunds must not offer it again.
	stuck, err := e.leaseRepo.ListStuckRefunds(ctx, time.Now().Add(time.Hour), 50)
	if err != nil {
		t.Fatalf("list stuck: %v", err)
	}
	for _, s := range stuck {
		if s.ID == leaseID {
			t.Fatal("unrecoverable refund still in the retry sweep")
		}
	}

	// Replay: claimed-once, single ticket.
	e.leaseH.issueAndFinalizeRefund(ctx, lr, "test-replay")
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("after replay: live tickets = %d, want 1", tickets)
	}
}

// H2: a driver's Retry Payment on a lease whose charge already landed (lost
// webhook) ADOPTS the payment — lease flips to paid, handler answers 409 —
// instead of re-serving a dead client_secret forever.
func TestAuditP0_RetryAdoptsSucceededPayment(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "p0_owner_a@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_a@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := seedAcceptedLease(t, e, owner, driver)
	if _, err := e.leaseRepo.SetPaymentPending(ctx, leaseID); err != nil {
		t.Fatalf("to payment_pending: %v", err)
	}
	// Adoption creates a key-handover row whose car/user FKs are NO ACTION —
	// it must go before the car/user cleanups (LIFO: registered later runs
	// earlier).
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id = $1`, leaseID)
	})
	intent := "pi_test_adopt_" + leaseID.String()[:8]
	secret := intent + "_secret"
	seedPaymentAt(t, e, leaseID, 20000, "succeeded", &intent)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE payments SET payment_intent_client_secret=$2 WHERE lease_request_id=$1`, leaseID, secret); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	rr := httptest.NewRecorder()
	e.leaseH.CreatePaymentIntent(rr, paymentIntentReq(t, driver, leaseID))
	if rr.Code != http.StatusConflict {
		t.Fatalf("retry on succeeded payment = %d (%s), want 409", rr.Code, rr.Body.String())
	}
	lr, _ := e.leaseRepo.GetByID(ctx, leaseID)
	if lr.Status != models.LeaseStatusPaid {
		t.Fatalf("lease status = %s, want paid — adoption didn't happen", lr.Status)
	}
	// The retry must never have handed back the stale secret.
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatal("stale client_secret served on the adopt path")
	}
}

// M3c: an open payment window blocks account deletion for both parties,
// with self-service instructions.
func TestAuditP0_DeletionBlocksOpenPaymentWindow(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "p0_owner_d@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_d@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := seedAcceptedLease(t, e, owner, driver)
	if _, err := e.leaseRepo.SetPaymentPending(ctx, leaseID); err != nil {
		t.Fatalf("to payment_pending: %v", err)
	}

	userRepo := repository.NewUserRepository(e.db)
	for _, who := range []uuid.UUID{driver, owner} {
		blockers, err := userRepo.ListAccountDeletionBlockers(ctx, who)
		if err != nil {
			t.Fatalf("blockers(%s): %v", who, err)
		}
		found := false
		for _, b := range blockers {
			if b.Kind == "payment_in_flight" {
				found = true
				if strings.Contains(strings.ToLower(b.Detail), "support") {
					t.Errorf("blocker routes to support: %q", b.Detail)
				}
			}
		}
		if !found {
			t.Fatalf("payment_pending lease does not block deletion for %s", who)
		}
	}
}

// M4: the three settle guards — never-succeeded charges cannot settle,
// payout_only refuses mid-term, and a driver refund is refused once the
// ledger holds an owner payout. Plus the positive control (post-term
// payout_only works) and the mismatch alarm.
func TestAuditP0_SettleGuards(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	admin := e.seedUser(t, "admin", "p0_admin_g@example.com")
	owner := e.seedUser(t, "car_owner", "p0_owner_g@example.com")
	driver := e.seedUser(t, "driver", "p0_driver_g@example.com")
	e.seedLicense(t, driver)

	// (1) A charge that never succeeded cannot be settled in ANY direction.
	pendingLease, _ := seedAcceptedLease(t, e, owner, driver)
	e.cleanupLedger(t, pendingLease)
	if _, err := e.leaseRepo.SetPaymentPending(ctx, pendingLease); err != nil {
		t.Fatalf("to payment_pending: %v", err)
	}
	seedPaymentAt(t, e, pendingLease, 30000, "requires_payment_method", nil)
	for _, res := range []string{"payout_only", "close", "withhold"} {
		rr := httptest.NewRecorder()
		e.returnH.AdminSettleRent(rr, settleReq(t, admin, pendingLease, `{"resolution":"`+res+`","note":"audit p0 guard test"}`))
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "PAYMENT_NOT_SETTLED") {
			t.Fatalf("%s on unsucceeded charge = %d (%s), want 409 PAYMENT_NOT_SETTLED", res, rr.Code, rr.Body.String())
		}
	}
	var ledgerRows int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE lease_request_id=$1`, pendingLease).Scan(&ledgerRows)
	if ledgerRows != 0 {
		t.Fatalf("unsucceeded charge produced %d ledger rows", ledgerRows)
	}

	// (2) payout_only mid-term is refused; after the term ends it works.
	activeLease, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, activeLease)
	seedPaymentAt(t, e, activeLease, 30000, "succeeded", nil)
	rr := httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, activeLease, `{"resolution":"payout_only","note":"audit p0 guard test"}`))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "SETTLE_NOT_ALLOWED") {
		t.Fatalf("mid-term payout_only = %d (%s), want 409 SETTLE_NOT_ALLOWED", rr.Code, rr.Body.String())
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET rental_ends_at = NOW() - interval '1 hour' WHERE id=$1`, activeLease); err != nil {
		t.Fatalf("age term: %v", err)
	}
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, activeLease, `{"resolution":"payout_only","note":"audit p0 guard test"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("post-term payout_only = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	// (3) With the owner's payout on the ledger, a refunding close is refused…
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, activeLease, `{"resolution":"close","driver_refund_cents":5000,"note":"audit p0 guard test"}`))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "REFUND_AFTER_PAYOUT") {
		t.Fatalf("refunding close after payout = %d (%s), want 409 REFUND_AFTER_PAYOUT", rr.Code, rr.Body.String())
	}
	// …while a $0 close still completes the rental.
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, activeLease, `{"resolution":"close","driver_refund_cents":0,"note":"audit p0 guard test"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("zero close after payout = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	// (4) The ledger-mismatch alarm: a settlement recomputing a DIFFERENT
	// kept amount against the existing row raises a ticket instead of a
	// silent idempotent no-op.
	e.payoutH.SettleRentalPayout(ctx, activeLease, owner, 12345, models.PayoutSourceAdminSettlement, nil)
	var mismatchTickets int
	e.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM support_tickets
		WHERE lease_request_id=$1 AND subject LIKE '%mismatch%' AND status NOT IN ('resolved','closed')`, activeLease).Scan(&mismatchTickets)
	if mismatchTickets != 1 {
		t.Fatalf("ledger mismatch tickets = %d, want 1", mismatchTickets)
	}
}
