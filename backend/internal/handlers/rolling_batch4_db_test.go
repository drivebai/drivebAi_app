package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Batch-4 tests: the driver billing surface and the arrears settlement
// path. Stripe stays nil — every asserted path is DB-only or refuses
// before the network (fail-closed).

// The billing endpoints refuse fixed-term leases by predicate.
func TestBatch4_BillingSurfaceRefusesFixedTerm(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	owner := e.seedUser(t, "car_owner", "b4_owner_ft_"+uuid.New().String()[:8]+"@example.com")
	driver := e.seedUser(t, "driver", "b4_driver_ft_"+uuid.New().String()[:8]+"@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)

	for _, probe := range []struct {
		name string
		call func(rr *httptest.ResponseRecorder)
	}{
		{"status", func(rr *httptest.ResponseRecorder) { e.leaseH.GetBillingStatus(rr, returnReq(t, driver, leaseID, ``)) }},
		{"pay-now", func(rr *httptest.ResponseRecorder) { e.leaseH.PayNow(rr, returnReq(t, driver, leaseID, `{}`)) }},
		{"card-update", func(rr *httptest.ResponseRecorder) { e.leaseH.CardUpdateStart(rr, returnReq(t, driver, leaseID, `{}`)) }},
		{"card-update-complete", func(rr *httptest.ResponseRecorder) {
			e.leaseH.CardUpdateComplete(rr, returnReq(t, driver, leaseID, `{"setup_intent_id":"seti_x"}`))
		}},
	} {
		rr := httptest.NewRecorder()
		probe.call(rr)
		if rr.Code != 409 {
			t.Errorf("%s on fixed-term = %d, want 409 NOT_ROLLING (%s)", probe.name, rr.Code, rr.Body.String())
		}
	}
}

// handleArrearsPaid: the full DB settlement — claim once, intent
// overwritten with the money-bearing arrears PI, owner share lands
// directly as pending, redelivery idempotent.
func TestBatch4_ArrearsPaidSettlesLedger(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	f := seedRollingLease(t, e, "b4arr", 10, 7)
	c2Start := f.pickup.AddDate(0, 0, 7)
	returnedAt := c2Start.AddDate(0, 0, 3)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET vehicle_returned_at=$2 WHERE id=$1`, f.leaseID, returnedAt); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	owed := int64(6426)
	c2 := seedCycleRow(t, e, f.leaseID, 2, c2Start, c2Start.AddDate(0, 0, 7), owed, "arrears_due", strPtr("pi_b4_dead"), nil, 0)

	if !e.leaseH.handleArrearsPaid(ctx, c2, "pi_b4_arrears_live") {
		t.Fatalf("arrears settlement returned false on clean state")
	}
	var st, intent string
	e.db.Pool.QueryRow(ctx, `SELECT status, stripe_payment_intent_id FROM billing_cycles WHERE id=$1`, c2).Scan(&st, &intent)
	if st != "paid" || intent != "pi_b4_arrears_live" {
		t.Errorf("cycle after arrears = %s/%s, want paid/pi_b4_arrears_live (intent overwritten)", st, intent)
	}
	wantFee, wantOwner := models.ComputePayoutSplit(owed, payoutTestFeeBPS)
	var kept, fee, ownerCents int64
	var pStatus, pSource string
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT gross_kept_cents, fee_cents, owner_amount_cents, status, source
		FROM owner_payouts WHERE billing_cycle_id=$1`, c2).Scan(&kept, &fee, &ownerCents, &pStatus, &pSource); err != nil {
		t.Fatalf("payout row: %v", err)
	}
	if kept != owed || fee != wantFee || ownerCents != wantOwner || pStatus != "pending" || pSource != "return_completed" {
		t.Errorf("arrears payout = %d/%d/%d %s/%s, want %d/%d/%d pending/return_completed",
			kept, fee, ownerCents, pStatus, pSource, owed, wantFee, wantOwner)
	}

	// Redelivery: already paid + payout present → idempotent ACK, one row.
	if !e.leaseH.handleArrearsPaid(ctx, c2, "pi_b4_arrears_live") {
		t.Errorf("redelivery should ACK")
	}
	var rows int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE billing_cycle_id=$1`, c2).Scan(&rows)
	if rows != 1 {
		t.Errorf("payout rows after redelivery = %d, want 1", rows)
	}
}

// An arrears success landing on a WAIVED cycle must not be kept: with
// Stripe unavailable the handler fails closed (500 → redelivery), never
// ACKs money away.
func TestBatch4_ArrearsOnWaivedFailsClosed(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	f := seedRollingLease(t, e, "b4wv", 10, 7)
	c2 := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), f.pickup.AddDate(0, 0, 14), 6426, "waived", strPtr("pi_b4_wv"), nil, 0)
	if e.leaseH.handleArrearsPaid(ctx, c2, "pi_b4_wv_late") {
		t.Errorf("arrears on waived cycle ACKed with no refund recorded — money silently kept")
	}
}

// ArrearsPaidClaim and UpdateConsentPaymentMethod repo gates.
func TestBatch4_RepoGates(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)

	f := seedRollingLease(t, e, "b4rg", 10, 7)
	c2 := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), f.pickup.AddDate(0, 0, 14), 6426, "arrears_due", nil, nil, 0)

	if ok, err := billingRepo.ArrearsPaidClaim(ctx, c2, "pi_claim_1"); err != nil || !ok {
		t.Fatalf("first arrears claim: ok=%v err=%v", ok, err)
	}
	if ok, _ := billingRepo.ArrearsPaidClaim(ctx, c2, "pi_claim_2"); ok {
		t.Errorf("second arrears claim must lose")
	}
	var intent string
	e.db.Pool.QueryRow(ctx, `SELECT stripe_payment_intent_id FROM billing_cycles WHERE id=$1`, c2).Scan(&intent)
	if intent != "pi_claim_1" {
		t.Errorf("intent = %s, want first claimer's pi_claim_1", intent)
	}

	// No consent row yet → PM update refuses.
	if ok, _ := billingRepo.UpdateConsentPaymentMethod(ctx, f.leaseID, "pm_new", "visa", "4242", "fp"); ok {
		t.Errorf("PM update without a consent row must refuse")
	}
	// Unactivated consent → refuses; activated → updates.
	consent, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: f.leaseID, DriverID: f.driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "test disclosure",
	})
	if err != nil || consent == nil {
		t.Fatalf("create consent: %v", err)
	}
	if ok, _ := billingRepo.UpdateConsentPaymentMethod(ctx, f.leaseID, "pm_new", "visa", "4242", "fp"); ok {
		t.Errorf("PM update on unactivated consent must refuse")
	}
	if ok, err := billingRepo.ActivateConsent(ctx, f.leaseID, "pm_orig", "visa", "1111", "fp0"); err != nil || !ok {
		t.Fatalf("activate: ok=%v err=%v", ok, err)
	}
	if ok, err := billingRepo.UpdateConsentPaymentMethod(ctx, f.leaseID, "pm_new", "mastercard", "4444", "fp1"); err != nil || !ok {
		t.Fatalf("PM update on active consent: ok=%v err=%v", ok, err)
	}
	fresh, _ := billingRepo.GetActiveConsent(ctx, f.leaseID)
	if fresh == nil || fresh.StripePaymentMethodID == nil || *fresh.StripePaymentMethodID != "pm_new" ||
		fresh.CardLast4 == nil || *fresh.CardLast4 != "4444" {
		t.Errorf("consent PM not swapped: %+v", fresh)
	}
	// Empty PM id can never clobber the mandate.
	if ok, _ := billingRepo.UpdateConsentPaymentMethod(ctx, f.leaseID, "", "x", "y", "z"); ok {
		t.Errorf("empty PM id must refuse")
	}
}

// The billing status endpoint's shapes: mandate summary + arrears bucket
// for the driver; the owner sees state but never a client secret.
func TestBatch4_BillingStatusShapes(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	f := seedRollingLease(t, e, "b4st", 10, 7)
	if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: f.leaseID, DriverID: f.driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "test disclosure",
	}); err != nil {
		t.Fatalf("consent: %v", err)
	}
	if ok, err := billingRepo.ActivateConsent(ctx, f.leaseID, "pm_b4st", "visa", "4242", "fp"); err != nil || !ok {
		t.Fatalf("activate: %v", err)
	}
	c2Start := f.pickup.AddDate(0, 0, 7)
	returnedAt := c2Start.AddDate(0, 0, 3)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET vehicle_returned_at=$2 WHERE id=$1`, f.leaseID, returnedAt); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	seedCycleRow(t, e, f.leaseID, 2, c2Start, c2Start.AddDate(0, 0, 7), 6426, "arrears_due", nil, nil, 0)

	rr := httptest.NewRecorder()
	e.leaseH.GetBillingStatus(rr, returnReq(t, f.driver, f.leaseID, ``))
	if rr.Code != 200 {
		t.Fatalf("status: %d (%s)", rr.Code, rr.Body.String())
	}
	var got struct {
		AmountCents   int64  `json:"amount_cents"`
		CardLast4     string `json:"card_last4"`
		ConsentActive bool   `json:"consent_active"`
		NextChargeAt  *string `json:"next_charge_at"`
		Arrears       *struct {
			AmountCents int64 `json:"amount_cents"`
		} `json:"arrears"`
	}
	mustDecode(t, rr.Body.Bytes(), &got)
	if got.AmountCents != 15000 || got.CardLast4 != "4242" || !got.ConsentActive {
		t.Errorf("mandate summary wrong: %+v", got)
	}
	if got.Arrears == nil || got.Arrears.AmountCents != 6426 {
		t.Errorf("arrears bucket missing/wrong: %+v", got.Arrears)
	}
	if got.NextChargeAt != nil {
		t.Errorf("returned lease advertises a next charge: %v", *got.NextChargeAt)
	}

	// Owner may read the card too (their income stream) — same 200.
	rr = httptest.NewRecorder()
	e.leaseH.GetBillingStatus(rr, returnReq(t, f.owner, f.leaseID, ``))
	if rr.Code != 200 {
		t.Errorf("owner status: %d", rr.Code)
	}
	// A stranger gets 403.
	stranger := e.seedUser(t, "driver", "b4_stranger_"+uuid.New().String()[:8]+"@example.com")
	rr = httptest.NewRecorder()
	e.leaseH.GetBillingStatus(rr, returnReq(t, stranger, f.leaseID, ``))
	if rr.Code != 403 {
		t.Errorf("stranger status: %d, want 403", rr.Code)
	}
}

// Pay-now's DB-refusal arms (Stripe never reached): nothing due on a live
// lease with no open cycle, and nothing due post-return without arrears.
func TestBatch4_PayNowRefusals(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	f := seedRollingLease(t, e, "b4pn", 3, 7)
	rr := httptest.NewRecorder()
	e.leaseH.PayNow(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 503 && rr.Code != 409 {
		t.Errorf("pay-now with no stripe/no cycle = %d, want 503/409 (%s)", rr.Code, rr.Body.String())
	}

	f2 := seedRollingLease(t, e, "b4pn2", 10, 7)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET vehicle_returned_at=NOW() WHERE id=$1`, f2.leaseID); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	rr = httptest.NewRecorder()
	e.leaseH.PayNow(rr, returnReq(t, f2.driver, f2.leaseID, `{}`))
	if rr.Code != 503 && rr.Code != 409 {
		t.Errorf("pay-now post-return no-arrears = %d, want 503/409 (%s)", rr.Code, rr.Body.String())
	}
}

// Batch-4 review fixes: a paid-signal whose intent differs from the
// cycle's recorded one is a DUPLICATE charge and must fail closed with
// Stripe unavailable (500 → redelivery), never ACK — for both the arrears
// branch and the cycle branch's redelivery arm.
func TestBatch4_ReviewFix_ForeignIntentNeverAcked(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	f := seedRollingLease(t, e, "b4dup", 10, 7)
	c2Start := f.pickup.AddDate(0, 0, 7)
	// Cycle settled by arrears intent pi_A; a foreign pi_B success arrives.
	c2 := seedCycleRow(t, e, f.leaseID, 2, c2Start, c2Start.AddDate(0, 0, 7), 6426, "paid", strPtr("pi_A"), nil, 0)
	seedAccruing(t, e, f, c2, 6426, c2Start, c2Start.AddDate(0, 0, 7))
	if e.leaseH.handleArrearsPaid(ctx, c2, "pi_B") {
		t.Errorf("foreign arrears intent ACKed — duplicate charge silently kept")
	}
	if !e.leaseH.handleArrearsPaid(ctx, c2, "pi_A") {
		t.Errorf("true redelivery of the recorded intent should ACK")
	}
	if e.leaseH.handleCyclePaid(ctx, c2, "pi_B") {
		t.Errorf("foreign cycle intent ACKed by handleCyclePaid — duplicate charge silently kept")
	}
	if !e.leaseH.handleCyclePaid(ctx, c2, "pi_A") {
		t.Errorf("cycle redelivery of the recorded intent should ACK")
	}
}

// The second-charge replay guard: refund_id on the row no longer shields a
// DIFFERENT intent landing on a written-off cycle (fail-closed without
// Stripe), while the recorded intent's replay still ACKs.
func TestBatch4_ReviewFix_SecondChargeOnWrittenOffCycle(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	f := seedRollingLease(t, e, "b4sec", 10, 7)
	c2 := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), f.pickup.AddDate(0, 0, 14), 6426, "waived", strPtr("pi_orig"), strPtr("re_first"), 15000)
	if !e.leaseH.handleArrearsPaid(ctx, c2, "pi_orig") {
		t.Errorf("recorded intent's replay on waived cycle should ACK")
	}
	if e.leaseH.handleArrearsPaid(ctx, c2, "pi_second") {
		t.Errorf("second distinct charge on waived cycle ACKed — silently kept")
	}
}
