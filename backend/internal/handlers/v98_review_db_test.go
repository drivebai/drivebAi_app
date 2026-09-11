package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// v98 review fixes. Every test here exists because a reviewer showed a
// state with no exit or a money path with no proof.

// rollingFixture: a paid, picked-up rolling lease with an ACTIVE consent.
func (e *payoutEnv) rollingFixture(t *testing.T, billingRepo *repository.BillingRepository, tag string) (leaseID, owner, driver uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner = e.seedUser(t, "car_owner", tag+"_o_"+run+"@example.com")
	driver = e.seedUser(t, "driver", tag+"_d_"+run+"@example.com")
	e.seedLicense(t, driver)
	leaseID, _ = e.seedActiveRental(t, owner, driver)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET billing_mode='rolling', weeks=1 WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("to rolling: %v", err)
	}
	text, ver := models.RollingDisclosureFor("weekly", 30000)
	if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: 30000, TermsVersion: ver, DisclosureText: text,
	}); err != nil {
		t.Fatalf("consent: %v", err)
	}
	if ok, err := billingRepo.ActivateConsent(ctx, leaseID, "pm_"+tag+run, "visa", "4242", "fp_"+tag+run); err != nil || !ok {
		t.Fatalf("activate: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_amendment_offers WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
	})
	return leaseID, owner, driver
}

func (e *payoutEnv) setEnds(t *testing.T, leaseID uuid.UUID, offset string) {
	t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(),
		`UPDATE lease_requests SET rental_ends_at = NOW() + $2::interval WHERE id=$1`, leaseID, offset); err != nil {
		t.Fatalf("set ends: %v", err)
	}
}

func (e *payoutEnv) endsAt(t *testing.T, leaseID uuid.UUID) time.Time {
	t.Helper()
	var v time.Time
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&v); err != nil {
		t.Fatalf("ends at: %v", err)
	}
	return v
}

func (e *payoutEnv) ticketCount(t *testing.T, where string, arg interface{}) int {
	t.Helper()
	var n int
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM support_tickets WHERE `+where, arg).Scan(&n); err != nil {
		t.Fatalf("ticket count: %v", err)
	}
	return n
}

// Resumption after a halt bills FORWARD with the notice runway: a lapsed
// lease lands at NOW()+48h (not NOW(), which would charge within a minute
// and quote a past date), and the lapsed days are reported. A lease whose
// paid-through is still ahead is untouched.
func TestClearRenewalHaltReanchorsLapsedLeaseForward(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	ctx := context.Background()

	lapsedLease, _, _ := e.rollingFixture(t, billingRepo, "halt_lapsed")
	e.setEnds(t, lapsedLease, "-10 days")
	if halted, err := e.leaseRepo.HaltRenewals(ctx, lapsedLease, "consent_revoked"); err != nil || !halted {
		t.Fatalf("halt: %v %v", halted, err)
	}
	cleared, lapsed, err := e.leaseRepo.ClearRenewalHaltReporting(ctx, lapsedLease, "consent_revoked")
	if err != nil || !cleared {
		t.Fatalf("clear: cleared=%v err=%v", cleared, err)
	}
	if lapsed < 10*24*time.Hour-time.Minute || lapsed > 10*24*time.Hour+time.Minute {
		t.Errorf("lapsed = %v, want ~10 days", lapsed)
	}
	want := time.Now().Add(models.BillingNoticeLead)
	if got := e.endsAt(t, lapsedLease); got.Before(want.Add(-time.Minute)) || got.After(want.Add(time.Minute)) {
		t.Errorf("re-anchored to %v, want ~NOW()+%v", got, models.BillingNoticeLead)
	}
	// And the mint phase does NOT charge in the same tick: at NOW()+48h the
	// lease is outside the 24h charge lead.
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.billingMintPhase(ctx, time.Now())
	var n int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, lapsedLease).Scan(&n)
	if n != 0 {
		t.Errorf("a cycle was minted in the tick after the halt cleared (%d) — charged with no runway", n)
	}

	futureLease, _, _ := e.rollingFixture(t, billingRepo, "halt_future")
	e.setEnds(t, futureLease, "20 hours")
	before := e.endsAt(t, futureLease)
	_, _ = e.leaseRepo.HaltRenewals(ctx, futureLease, "return_initiated")
	cleared, lapsed, err = e.leaseRepo.ClearRenewalHaltReporting(ctx, futureLease, "return_initiated")
	if err != nil || !cleared || lapsed != 0 {
		t.Fatalf("future clear: cleared=%v lapsed=%v err=%v", cleared, lapsed, err)
	}
	if got := e.endsAt(t, futureLease); !got.Equal(before) {
		t.Errorf("a lease with paid-through ahead was moved: %v → %v", before, got)
	}
	// Wrong reason: no clear, nothing moved.
	if c, _, _ := e.leaseRepo.ClearRenewalHaltReporting(ctx, futureLease, "dispute"); c {
		t.Error("cleared a halt that is not set")
	}
}

// A lease below the catch-up floor is not silent, and the kill switch does
// not silence it either.
func TestStaleRollingLeaseIsEscalatedEvenWithTheSwitchOff(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	ctx := context.Background()
	e.leaseH.SetTicketRepository(e.ticketRepo)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, false) // switch OFF

	leaseID, _, _ := e.rollingFixture(t, billingRepo, "stale")
	e.setEnds(t, leaseID, "-20 days")

	e.leaseH.runBillingSweep(ctx)
	if n := e.ticketCount(t, "lease_request_id = $1 AND subject LIKE 'Rolling rental stalled%'", leaseID); n != 1 {
		t.Fatalf("stale lease tickets = %d, want exactly 1 with the switch off", n)
	}
	var cycles int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&cycles)
	if cycles != 0 {
		t.Errorf("the switch is off but %d cycle(s) were minted", cycles)
	}
	var halted *string
	_ = e.db.Pool.QueryRow(ctx, `SELECT renewal_halted_reason FROM lease_requests WHERE id=$1`, leaseID).Scan(&halted)
	if halted != nil {
		t.Errorf("escalation halted the lease (%q) — it must only be surfaced", *halted)
	}
	// Ticket-first + live-ticket dedupe: a second tick opens nothing new.
	e.leaseH.runBillingSweep(ctx)
	if n := e.ticketCount(t, "lease_request_id = $1", leaseID); n != 1 {
		t.Errorf("second tick: tickets = %d, want 1", n)
	}
	// Switch ON: still no back-billing — the floor holds, the ticket stands.
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.runBillingSweep(ctx)
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&cycles)
	if cycles != 0 {
		t.Errorf("switch on: %d cycle(s) minted against a 20-day-stale paid-through — the floor regressed", cycles)
	}
}

// Delinquency recovery: a late Pay-now on a cycle whose period ended weeks
// ago resumes billing forward with the notice runway instead of parking
// paid-through in the past, below the floor.
func TestLatePayNowResumesForwardNotIntoTheFloor(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	ctx := context.Background()

	leaseID, _, _ := e.rollingFixture(t, billingRepo, "latepay")
	e.setEnds(t, leaseID, "-13 days")
	start := time.Now().Add(-20 * 24 * time.Hour)
	cycle, err := billingRepo.MintCycle(ctx, leaseID, 2, start, start.Add(7*24*time.Hour), 30000, time.Now())
	if err != nil || cycle == nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE billing_cycles SET status='failed_final', next_attempt_at=NULL WHERE id=$1`, cycle.ID); err != nil {
		t.Fatalf("fail cycle: %v", err)
	}
	if marked, _ := e.leaseRepo.MarkDelinquent(ctx, leaseID); !marked {
		t.Fatal("could not mark delinquent")
	}
	if halted, _ := e.leaseRepo.HaltRenewals(ctx, leaseID, "delinquent"); !halted {
		t.Fatal("could not halt")
	}

	if _, ok, err := billingRepo.AdvanceOnCyclePaid(ctx, cycle.ID); err != nil || !ok {
		t.Fatalf("advance: ok=%v err=%v", ok, err)
	}
	want := time.Now().Add(models.BillingNoticeLead)
	if got := e.endsAt(t, leaseID); got.Before(want.Add(-time.Minute)) || got.After(want.Add(time.Minute)) {
		t.Errorf("late pay-now left paid-through at %v, want ~NOW()+%v (forward, above the floor)", got, models.BillingNoticeLead)
	}
	var delinquent *time.Time
	var halted *string
	_ = e.db.Pool.QueryRow(ctx, `SELECT delinquent_since, renewal_halted_reason FROM lease_requests WHERE id=$1`, leaseID).Scan(&delinquent, &halted)
	if delinquent != nil || halted != nil {
		t.Errorf("delinquency not cleared: since=%v halt=%v", delinquent, halted)
	}

	// On time is untouched: a current cycle paid before its period ends
	// advances exactly to that period end.
	onTime, _, _ := e.rollingFixture(t, billingRepo, "ontime")
	e.setEnds(t, onTime, "6 days")
	pStart := e.endsAt(t, onTime)
	c2, err := billingRepo.MintCycle(ctx, onTime, 2, pStart, pStart.Add(7*24*time.Hour), 30000, time.Now())
	if err != nil || c2 == nil {
		t.Fatalf("mint on-time: %v", err)
	}
	if _, ok, err := billingRepo.AdvanceOnCyclePaid(ctx, c2.ID); err != nil || !ok {
		t.Fatalf("advance on-time: ok=%v err=%v", ok, err)
	}
	if got := e.endsAt(t, onTime); !got.Equal(pStart.Add(7 * 24 * time.Hour)) {
		t.Errorf("on-time advance moved paid-through to %v, want %v", got, pStart.Add(7*24*time.Hour))
	}
}

// "You keep the car while we retry": the term scanner must not tell a
// delinquent driver the rental ended while the ladder still has an attempt
// scheduled.
func TestTermScannerWaitsForTheRetryLadder(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	ctx := context.Background()

	leaseID, _, _ := e.rollingFixture(t, billingRepo, "ladder")
	e.setEnds(t, leaseID, "-1 hour")
	start := time.Now().Add(-1 * time.Hour)
	cycle, err := billingRepo.MintCycle(ctx, leaseID, 2, start, start.Add(7*24*time.Hour), 30000, time.Now())
	if err != nil || cycle == nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := e.db.Pool.Exec(ctx, `UPDATE billing_cycles SET status='retrying', next_attempt_at = NOW() + interval '20 hours' WHERE id=$1`, cycle.ID); err != nil {
		t.Fatalf("retrying: %v", err)
	}
	if marked, _ := e.leaseRepo.MarkDelinquent(ctx, leaseID); !marked {
		t.Fatal("could not mark delinquent")
	}
	claimedFor := func() bool {
		rows, err := e.leaseRepo.ClaimTermOverdue(ctx, time.Now(), 200)
		if err != nil {
			t.Fatalf("claim overdue: %v", err)
		}
		for _, r := range rows {
			if r.ID == leaseID {
				return true
			}
		}
		return false
	}
	if claimedFor() {
		t.Fatal("overdue notice claimed while a retry is still scheduled — the driver would be told to hand the car back mid-ladder")
	}
	// Ladder exhausted: the next attempt is in the past (or the cycle is
	// terminal) — now the rental really is over.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE billing_cycles SET status='failed_final', next_attempt_at=NULL WHERE id=$1`, cycle.ID); err != nil {
		t.Fatalf("exhaust: %v", err)
	}
	if !claimedFor() {
		t.Fatal("overdue notice not claimed after the ladder was exhausted")
	}
}

// Build 36 decodes {ok} on accept/decline/withdraw and the created offer at
// the top level on propose. The backend now serves those shapes additively.
func TestAmendmentResponsesCarryBuild36Shapes(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	leaseID, owner, driver := e.rollingFixture(t, billingRepo, "amendshape")
	e.setEnds(t, leaseID, "5 days")

	decode := func(rr *httptest.ResponseRecorder) map[string]interface{} {
		var m map[string]interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode %s: %v", rr.Body.String(), err)
		}
		return m
	}
	propose := func() uuid.UUID {
		rr := httptest.NewRecorder()
		e.leaseH.ProposeAmendment(rr, returnReq(t, owner, leaseID, `{"kind":"price","new_amount_cents":32000}`))
		if rr.Code != http.StatusCreated {
			t.Fatalf("propose: %d %s", rr.Code, rr.Body.String())
		}
		m := decode(rr)
		if _, ok := m["amendment"]; !ok {
			t.Error("propose response lost the wrapped \"amendment\" key")
		}
		idRaw, ok := m["id"].(string)
		if !ok || idRaw == "" {
			t.Fatalf("propose response has no top-level id (build 36 decodes the offer at the top level): %s", rr.Body.String())
		}
		for _, k := range []string{"lease_request_id", "proposed_by", "kind", "new_amount_cents", "status", "expires_at"} {
			if _, ok := m[k]; !ok {
				t.Errorf("propose response missing top-level %q that build 36's model requires", k)
			}
		}
		id, err := uuid.Parse(idRaw)
		if err != nil {
			t.Fatalf("offer id: %v", err)
		}
		return id
	}
	requireOK := func(rr *httptest.ResponseRecorder, what string) {
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", what, rr.Code, rr.Body.String())
		}
		if ok, _ := decode(rr)["ok"].(bool); !ok {
			t.Errorf("%s response has no ok:true — build 36 would show a failure for a success: %s", what, rr.Body.String())
		}
	}

	offer := propose()
	rr := httptest.NewRecorder()
	e.leaseH.DeclineAmendment(rr, returnReq(t, driver, offer, `{}`))
	requireOK(rr, "decline")

	offer = propose()
	rr = httptest.NewRecorder()
	e.leaseH.WithdrawAmendment(rr, returnReq(t, owner, offer, `{}`))
	requireOK(rr, "withdraw")

	offer = propose()
	rr = httptest.NewRecorder()
	e.leaseH.AcceptAmendment(rr, returnReq(t, driver, offer, `{}`))
	requireOK(rr, "accept")
	_ = fmt.Sprintf
}

// The executor is the last gate: a sale that predates seller payouts, or has
// no intent to fund it, is parked withheld with a ticket by EVERY path that
// executes rows — including the Connect-onboarding drain — and nothing is
// transferred from platform balance. No Stripe call is reached.
func TestExecutePayoutRefusesUnfundableSaleOnEveryPath(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	purchaseRepo := repository.NewPurchaseRequestRepository(e.db)
	e.payoutH.SetPurchaseRepository(purchaseRepo)
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "unf_seller_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "unf_buyer_"+run+"@example.com")
	if _, err := e.db.Pool.Exec(ctx, `UPDATE users SET stripe_account_id = 'acct_test_unf_' || $1::text, payout_status = 'ready' WHERE id = $1`, seller); err != nil {
		t.Fatalf("ready seller: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE user_id = $1`, seller) })

	seed := func(completedAt string, intent *string) uuid.UUID {
		car := e.seedCar(t, seller, "sold", true, false)
		var chatID uuid.UUID
		if err := e.db.Pool.QueryRow(ctx, `INSERT INTO chats (car_id, driver_id, owner_id) VALUES ($1,$2,$3) RETURNING id`, car, buyer, seller).Scan(&chatID); err != nil {
			t.Fatalf("chat: %v", err)
		}
		var pid uuid.UUID
		if err := e.db.Pool.QueryRow(ctx, `
			INSERT INTO purchase_requests (car_id, seller_id, buyer_id, chat_id, offer_amount_cents, currency, status,
				expires_at, payment_status, payment_intent_id, completed_at)
			VALUES ($1,$2,$3,$4, 900000, 'USD', 'completed', NOW() + INTERVAL '30 days', 'succeeded', $5, $6::timestamptz)
			RETURNING id`, car, seller, buyer, chatID, intent, completedAt).Scan(&pid); err != nil {
			t.Fatalf("purchase: %v", err)
		}
		if _, _, err := e.payoutRepo.CreateForSale(ctx, &models.OwnerPayout{
			PurchaseRequestID: &pid, OwnerID: seller,
			GrossKeptCents: 900000, FeeBPS: 1000, FeeCents: 90000, OwnerAmountCents: 810000,
			Currency: "USD", Status: models.PayoutAwaitingOnboarding, Source: models.PayoutSourceSaleCompleted,
		}); err != nil {
			t.Fatalf("payout row: %v", err)
		}
		t.Cleanup(func() {
			e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE purchase_request_id = $1`, pid)
			e.db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id = $1`, pid)
			e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
		})
		return pid
	}
	statusOf := func(pid uuid.UUID) string {
		row, err := e.payoutRepo.GetByPurchaseRequestID(ctx, pid)
		if err != nil || row == nil {
			t.Fatalf("payout row for %s: %v", pid, err)
		}
		return string(row.Status)
	}

	intent := "pi_test_era_" + run
	preCutoff := seed("2026-07-14T00:00:00Z", &intent) // test-era sale, charge on the old account
	noIntent := seed("2026-09-11T12:00:00Z", nil)      // post-cutoff but nothing funds it

	// The Connect-onboarding drain — the exact path of the near-miss.
	e.payoutH.executeAwaitingForOwner(ctx, seller)
	if st := statusOf(preCutoff); st != "withheld" {
		t.Errorf("pre-cutoff sale after the onboarding drain = %s, want withheld", st)
	}
	if st := statusOf(noIntent); st != "withheld" {
		t.Errorf("intent-less sale after the onboarding drain = %s, want withheld", st)
	}
	if n := e.ticketCount(t, "user_id = $1 AND subject LIKE 'Payout withheld%'", seller); n != 2 {
		t.Errorf("withheld tickets = %d, want 2 (one per parked row)", n)
	}
	// An admin revive goes through the same gate and is refused the same way.
	row, _ := e.payoutRepo.GetByPurchaseRequestID(ctx, preCutoff)
	revived, err := e.payoutH.AdminReviveWithheld(ctx, row.ID, "trying again")
	if err != nil || revived == nil {
		t.Fatalf("revive: %v", err)
	}
	if revived.Status != models.PayoutWithheld {
		t.Errorf("revived pre-cutoff sale = %s, want withheld again (the gate holds on revive)", revived.Status)
	}
}

// ConfirmHandover: the buyer's claim and the window's claim are mutually
// exclusive, the buyer path never stamps auto-accepted, and the guards hold.
func TestConfirmHandoverClaimIsExclusiveAndGuarded(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "ch_seller_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "ch_buyer_"+run+"@example.com")
	e.seedLicense(t, buyer)

	mk := func(status string) uuid.UUID {
		car := e.seedCar(t, seller, "available", true, false)
		e.listForSale(t, car, 9000)
		pid := e.seedPurchase(t, car, seller, buyer, status, 4*24*time.Hour)
		if _, err := e.db.Pool.Exec(ctx, `UPDATE purchase_requests SET inspection_deadline_at = NOW() - interval '1 hour', keys_handed_over_at = NOW() - interval '49 hours' WHERE id=$1`, pid); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		return pid
	}

	// Buyer first, then the window: the window loses.
	a := mk("awaiting_inspection")
	claimed, err := e.purchaseRepo.ClaimBuyerHandoverConfirm(ctx, a)
	if err != nil || claimed == nil || claimed.Status != models.PurchaseStatusInspectionAccepted {
		t.Fatalf("buyer claim: %v %v", claimed, err)
	}
	var auto *time.Time
	_ = e.db.Pool.QueryRow(ctx, `SELECT inspection_auto_accepted_at FROM purchase_requests WHERE id=$1`, a).Scan(&auto)
	if auto != nil {
		t.Error("buyer path stamped inspection_auto_accepted_at — 'who accepted this' is now wrong")
	}
	if late, _ := e.purchaseRepo.ClaimInspectionAutoAccept(ctx, a); late != nil {
		t.Error("the window claimed a sale the buyer had already confirmed")
	}
	// Window first, then the buyer: the buyer loses.
	b := mk("awaiting_inspection")
	if first, err := e.purchaseRepo.ClaimInspectionAutoAccept(ctx, b); err != nil || first == nil {
		t.Fatalf("window claim: %v %v", first, err)
	}
	if late, _ := e.purchaseRepo.ClaimBuyerHandoverConfirm(ctx, b); late != nil {
		t.Error("the buyer claimed a sale the window had already closed")
	}

	// Handler guards (no Stripe is reached on any of these).
	c := mk("awaiting_inspection")
	rr := httptest.NewRecorder()
	e.purchaseH.ConfirmHandover(rr, purchaseReq(t, seller, c, "confirm-handover", `{}`))
	if rr.Code != http.StatusForbidden {
		t.Errorf("seller confirming their own sale: %d, want 403", rr.Code)
	}
	d := mk("handover_scheduled")
	rr = httptest.NewRecorder()
	e.purchaseH.ConfirmHandover(rr, purchaseReq(t, buyer, d, "confirm-handover", `{}`))
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "NOT_AWAITING_INSPECTION" {
		t.Errorf("confirm before keys: %d %s, want 409 NOT_AWAITING_INSPECTION", rr.Code, rr.Body.String())
	}
	f := mk("completed")
	rr = httptest.NewRecorder()
	e.purchaseH.ConfirmHandover(rr, purchaseReq(t, buyer, f, "confirm-handover", `{}`))
	if rr.Code != http.StatusOK {
		t.Errorf("confirm on a completed sale: %d, want 200 (idempotent)", rr.Code)
	}
}

// A zero-refund return older than the heal window is ticketed, not healed;
// a young one is still healed.
func TestStuckReturnPastHealWindowIsTicketedNotHealed(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	e.returnH.SetTicketRepository(e.ticketRepo)
	run := uuid.NewString()[:8]

	seedReturn := func(tag, updatedOffset string) (uuid.UUID, uuid.UUID) {
		owner := e.seedUser(t, "car_owner", "stuck_o_"+tag+run+"@example.com")
		driver := e.seedUser(t, "driver", "stuck_d_"+tag+run+"@example.com")
		e.seedLicense(t, driver)
		leaseID, carID := e.seedActiveRental(t, owner, driver)
		e.seedPayment(t, leaseID, 30000)
		e.cleanupLedger(t, leaseID)
		var retID uuid.UUID
		if err := e.db.Pool.QueryRow(ctx, `
			INSERT INTO vehicle_returns (lease_request_id, car_id, owner_id, driver_id, status,
				driver_initiated_at, owner_confirmed_at, pickup_confirmed_at, returned_at,
				rental_weeks, paid_amount_cents, used_days, refund_amount_cents, refund_status, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'owner_confirmed',
				NOW() + $5::interval - interval '1 day', NOW() + $5::interval, NOW() + $5::interval - interval '8 days', NOW() + $5::interval - interval '1 day',
				1, 30000, 7, 0, 'not_applicable', NOW() + $5::interval - interval '1 day', NOW() + $5::interval)
			RETURNING id`, leaseID, carID, owner, driver, updatedOffset).Scan(&retID); err != nil {
			t.Fatalf("seed return: %v", err)
		}
		t.Cleanup(func() {
			e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE vehicle_return_id = $1`, retID)
			e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE id = $1`, retID)
		})
		return retID, leaseID
	}
	statusOf := func(id uuid.UUID) string {
		var s string
		_ = e.db.Pool.QueryRow(ctx, `SELECT status FROM vehicle_returns WHERE id=$1`, id).Scan(&s)
		return s
	}

	old, _ := seedReturn("old", "-40 days")
	young, _ := seedReturn("young", "-5 minutes")

	e.returnH.runStuckRefundSweep(ctx)

	if st := statusOf(old); st != "owner_confirmed" {
		t.Errorf("40-day-old return was healed to %s — the floor regressed", st)
	}
	var n int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE vehicle_return_id=$1 AND subject LIKE 'Return stuck past%'`, old).Scan(&n)
	if n != 1 {
		t.Errorf("heal-window tickets for the old return = %d, want 1", n)
	}
	if st := statusOf(young); st != "completed" {
		t.Errorf("5-minute-old zero-refund return = %s, want completed (still healed inside the window)", st)
	}
	e.returnH.runStuckRefundSweep(ctx)
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE vehicle_return_id=$1`, old).Scan(&n)
	if n != 1 {
		t.Errorf("second sweep: tickets = %d, want 1 (deduped)", n)
	}
}
