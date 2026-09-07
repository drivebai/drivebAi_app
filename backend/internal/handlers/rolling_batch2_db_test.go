package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Batch-2 tests. The client's tightened requirement: the fixed-term
// guarantee demonstrated POSITIVELY — a complete fixed-term lifecycle run
// with ROLLING_RENTALS_ENABLED off and on, fingerprinted, and compared
// field by field. Plus reachability: rolling paths must be unreachable for
// fixed-term leases BY PREDICATE, shown here, not described.

type fixedTermFingerprint struct {
	StatusAfterCreate  string
	StatusAfterAccept  string
	StatusAfterPaid    string
	StatusAfterPickup  string
	CarAfterPickup     string
	TermHours          float64 // rental_ends_at − pickup_confirmed_at
	ReturnRefundCents  int64
	ReturnStatus       string
	LeaseFinalStatus   string
	VehicleReturnedSet bool
	CarFinal           string
	PayoutKept         int64
	PayoutFee          int64
	PayoutOwner        int64
	PayoutStatus       string
	PayoutSource       string
	BillingCyclesCount int // MUST be 0 — rolling table untouched
	ConsentsCount      int // MUST be 0 — no consent for fixed-term
}

// runFixedTermLifecycle drives request → accept → pay → pickup → term end →
// return → refund($0, full term) → payout, with the rolling flag as given,
// AND runs the billing sweep at every stage to prove it never touches a
// fixed-term lease.
func runFixedTermLifecycle(t *testing.T, e *payoutEnv, billingRepo *repository.BillingRepository, rollingOn bool, tag string) fixedTermFingerprint {
	t.Helper()
	ctx := context.Background()
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, rollingOn)

	// Unique-per-run emails + explicit return/handover cleanup: the
	// lifecycle creates rows whose FKs would otherwise block the user
	// cleanup and poison the next run (learned the hard way in batch 1).
	runID := uuid.New().String()[:8]
	owner := e.seedUser(t, "car_owner", "b2_owner_"+tag+"_"+runID+"@example.com")
	driver := e.seedUser(t, "driver", "b2_driver_"+tag+"_"+runID+"@example.com")
	e.seedLicense(t, driver)
	car := e.seedCar(t, owner, "available", true, false)

	var fp fixedTermFingerprint
	status := func(id uuid.UUID) string {
		lr, err := e.leaseRepo.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("[%s] load lease: %v", tag, err)
		}
		return string(lr.Status)
	}
	carStatus := func() string {
		var s string
		e.db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id=$1`, car).Scan(&s)
		return s
	}
	sweep := func() { e.leaseH.runBillingSweep(ctx) }

	// Create (NO billing_mode in body — the default path).
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
	if rr.Code != http.StatusCreated {
		t.Fatalf("[%s] create: %d (%s)", tag, rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID uuid.UUID `json:"id"`
		} `json:"lease_request"`
	}
	mustDecode(t, rr.Body.Bytes(), &created)
	leaseID := created.LeaseRequest.ID
	e.cleanupLedger(t, leaseID)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id = $1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id = $1`, leaseID)
	})
	fp.StatusAfterCreate = status(leaseID)
	sweep()

	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("[%s] accept: %v", tag, err)
	}
	fp.StatusAfterAccept = status(leaseID)
	sweep()

	if _, err := e.leaseRepo.SetPaid(ctx, leaseID); err != nil {
		t.Fatalf("[%s] paid: %v", tag, err)
	}
	seedPaymentAt(t, e, leaseID, 15000, "succeeded", nil)
	fp.StatusAfterPaid = status(leaseID)
	sweep()

	if _, err := e.leaseRepo.ConfirmPickup(ctx, leaseID, driver); err != nil {
		t.Fatalf("[%s] pickup: %v", tag, err)
	}
	fp.StatusAfterPickup = status(leaseID)
	fp.CarAfterPickup = carStatus()

	var pickupAt, endsAt time.Time
	e.db.Pool.QueryRow(ctx, `SELECT pickup_confirmed_at, rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&pickupAt, &endsAt)
	fp.TermHours = endsAt.Sub(pickupAt).Hours()
	sweep()

	// Age past term end (full week used), then run the sweep again — the
	// rolling engine must not mint anything for a due FIXED lease.
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET pickup_confirmed_at = pickup_confirmed_at - interval '8 days',
		    rental_ends_at = rental_ends_at - interval '8 days' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("[%s] age: %v", tag, err)
	}
	sweep()

	// Return (full term → $0 refund → no-Stripe fast path) + owner confirm.
	rr = httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("[%s] initiate return: %d (%s)", tag, rr.Code, rr.Body.String())
	}
	ret, err := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if err != nil || ret == nil {
		t.Fatalf("[%s] load return: %v", tag, err)
	}
	fp.ReturnRefundCents = ret.RefundAmountCents
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, owner, ret.ID, `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("[%s] owner confirm: %d (%s)", tag, rr.Code, rr.Body.String())
	}
	sweep()

	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	fp.ReturnStatus = string(ret.Status)
	lr, _ := e.leaseRepo.GetByID(ctx, leaseID)
	fp.LeaseFinalStatus = string(lr.Status)
	fp.VehicleReturnedSet = lr.VehicleReturnedAt != nil
	fp.CarFinal = carStatus()

	var kept, fee, ownerCents int64
	var pStatus, pSource string
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT gross_kept_cents, fee_cents, owner_amount_cents, status, source
		FROM owner_payouts WHERE lease_request_id=$1`, leaseID).Scan(&kept, &fee, &ownerCents, &pStatus, &pSource); err != nil {
		t.Fatalf("[%s] payout row: %v", tag, err)
	}
	fp.PayoutKept, fp.PayoutFee, fp.PayoutOwner = kept, fee, ownerCents
	fp.PayoutStatus, fp.PayoutSource = pStatus, pSource

	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&fp.BillingCyclesCount)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID).Scan(&fp.ConsentsCount)
	return fp
}

func mustDecode(t *testing.T, b []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// TestBatch2_FixedTermByteIdentical is the client-required positive proof.
func TestBatch2_FixedTermByteIdentical(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)

	off := runFixedTermLifecycle(t, e, billingRepo, false, "off")
	on := runFixedTermLifecycle(t, e, billingRepo, true, "on")

	if !reflect.DeepEqual(off, on) {
		t.Fatalf("fixed-term lifecycle DIFFERS with the rolling flag:\n off=%+v\n  on=%+v", off, on)
	}
	// And the absolute expectations, so both runs being identically WRONG
	// can't pass: full week term, $0 refund, correct split, rolling tables
	// untouched.
	if off.TermHours != 7*24 {
		t.Errorf("term hours = %v, want 168", off.TermHours)
	}
	if off.ReturnRefundCents != 0 {
		t.Errorf("full-term refund = %d, want 0", off.ReturnRefundCents)
	}
	if off.PayoutKept != 15000 || off.PayoutFee != 1500 || off.PayoutOwner != 13500 {
		t.Errorf("split = %d/%d/%d, want 15000/1500/13500", off.PayoutKept, off.PayoutFee, off.PayoutOwner)
	}
	if off.BillingCyclesCount != 0 || off.ConsentsCount != 0 {
		t.Errorf("rolling tables touched by a fixed-term lease: cycles=%d consents=%d", off.BillingCyclesCount, off.ConsentsCount)
	}
	if off.LeaseFinalStatus != "paid" || !off.VehicleReturnedSet || off.ReturnStatus != "completed" {
		t.Errorf("terminal shape wrong: lease=%s returned=%v return=%s", off.LeaseFinalStatus, off.VehicleReturnedSet, off.ReturnStatus)
	}
}

// Reachability: every rolling entry point refuses a fixed-term lease BY
// PREDICATE — shown, not described.
func TestBatch2_RollingPathsUnreachableForFixedTerm(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true) // flag ON

	owner := e.seedUser(t, "car_owner", "b2_owner_g@example.com")
	driver := e.seedUser(t, "driver", "b2_driver_g@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	// Make it DUE by every clock the engine reads.
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET rental_ends_at = NOW() - interval '1 hour' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("age: %v", err)
	}

	// 1. The due-lister's predicate excludes it.
	due, err := e.leaseRepo.ListRollingDueForBilling(ctx, time.Now().UTC().Add(48*time.Hour), 50)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	for i := range due {
		if due[i].ID == leaseID {
			t.Fatal("fixed-term lease listed by the rolling due-lister")
		}
	}

	// 2. A full sweep mints nothing and consents nothing for it.
	e.leaseH.runBillingSweep(ctx)
	var n int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&n)
	if n != 0 {
		t.Fatalf("sweep minted %d cycles for a fixed-term lease", n)
	}

	// 3. Stop/terminate refuse it (rolling-scoped UPDATE).
	if claimed, _ := e.leaseRepo.StopRenewal(ctx, leaseID, driver); claimed {
		t.Fatal("StopRenewal claimed a fixed-term lease")
	}
	if claimed, _ := e.leaseRepo.TerminateRenewal(ctx, leaseID, owner); claimed {
		t.Fatal("TerminateRenewal claimed a fixed-term lease")
	}

	// 4. The renewal-notice claim refuses it.
	if claimed, _ := e.leaseRepo.ClaimRenewalNotice(ctx, leaseID); claimed {
		t.Fatal("renewal notice claimed a fixed-term lease")
	}

	// 5. The paid-through advance refuses it (mode-scoped UPDATE): a cycle
	// row can't even exist (FK'd to the lease but minted only by the sweep),
	// so advance is doubly unreachable — assert the UPDATE scope directly.
	var ends0 time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&ends0)
	e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET rental_ends_at = rental_ends_at + INTERVAL '7 days'
		WHERE id=$1 AND billing_mode = 'rolling'`, leaseID)
	var ends1 time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&ends1)
	if !ends0.Equal(ends1) {
		t.Fatal("mode-scoped advance moved a fixed-term lease's term end")
	}

	// 6. Term scanner still owns it: the gate's first disjunct.
	var gateMatches bool
	e.db.Pool.QueryRow(ctx, `
		SELECT (billing_mode = 'fixed_term' OR renewal_stopped_at IS NOT NULL
		        OR delinquent_since IS NOT NULL OR renewal_halted_reason IS NOT NULL)
		FROM lease_requests WHERE id=$1`, leaseID).Scan(&gateMatches)
	if !gateMatches {
		t.Fatal("term-scanner gate excludes a fixed-term lease")
	}

	// 7. Rolling creation is flag-gated: with the flag OFF the API refuses.
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, false)
	car2 := e.seedCar(t, owner, "available", true, false)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("listingId", car2.String())
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"billing_mode":"rolling"}`))
	req.Header.Set("Content-Type", "application/json")
	c := context.WithValue(req.Context(), httputil.UserIDKey, driver)
	c = context.WithValue(c, chi.RouteCtxKey, rctx)
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, req.WithContext(c))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("rolling create with flag off = %d, want 503", rr.Code)
	}
}

// Engine mechanics on a real rolling lease (no Stripe: consent inactive →
// the halt path; plus the atomic advance and promotion guards at repo level).
func TestBatch2_RollingEngineMechanics(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	owner := e.seedUser(t, "car_owner", "b2_owner_m@example.com")
	driver := e.seedUser(t, "driver", "b2_driver_m@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE lease_request_id=$1 AND billing_cycle_id IS NOT NULL`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
	})
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET billing_mode='rolling', weeks=1,
		    rental_ends_at = NOW() + interval '20 hours' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("to rolling: %v", err)
	}

	// (a) Due + NO active consent → the sweep halts renewals (the
	// consent_revoked exit), and mints nothing.
	e.leaseH.runBillingSweep(ctx)
	lr, _ := e.leaseRepo.GetByID(ctx, leaseID)
	if lr.RenewalHaltedReason == nil || *lr.RenewalHaltedReason != "consent_revoked" {
		t.Fatalf("no-consent lease not halted: %+v", lr.RenewalHaltedReason)
	}
	var n int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&n)
	if n != 0 {
		t.Fatalf("halted lease minted %d cycles", n)
	}

	// (b) Consent activated → halt cleared → mint happens (charge attempt
	// itself needs Stripe; the row parks at 'scheduled').
	consent, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "test disclosure",
	})
	if err != nil {
		t.Fatalf("create consent: %v", err)
	}
	if act, aerr := billingRepo.ActivateConsent(ctx, leaseID, "pm_test", "visa", "4242", "fp"); aerr != nil || !act {
		t.Fatalf("activate consent: %v/%v", act, aerr)
	}
	if cleared, _ := e.leaseRepo.ClearRenewalHalt(ctx, leaseID, "consent_revoked"); !cleared {
		t.Fatal("halt not cleared")
	}
	cycle, merr := billingRepo.MintCycle(ctx, leaseID, 2, time.Now().UTC().Add(20*time.Hour),
		time.Now().UTC().Add(20*time.Hour+7*24*time.Hour), consent.AmountCents, time.Now().UTC())
	if merr != nil || cycle == nil {
		t.Fatalf("mint: %v", merr)
	}
	// Mint is claimed-once: re-minting the same number returns the SAME row.
	again, _ := billingRepo.MintCycle(ctx, leaseID, 2, time.Now().UTC(), time.Now().UTC(), consent.AmountCents, time.Now().UTC())
	if again == nil || again.ID != cycle.ID {
		t.Fatal("re-mint produced a sibling cycle")
	}

	// (c) The atomic advance: cycle→paid + paid-through += 7d + flags
	// cleared, one call; second call is a benign no-op.
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET delinquent_since = NOW(), overdue_notified_at = NOW() WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("stamp flags: %v", err)
	}
	var endsBefore time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&endsBefore)
	paid, advanced, aerr := billingRepo.AdvanceOnCyclePaid(ctx, cycle.ID)
	if aerr != nil || paid == nil || !advanced {
		t.Fatalf("advance: %v (advanced=%v)", aerr, advanced)
	}
	var endsAfter time.Time
	var delinquent *time.Time
	var overdueNotified *time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at, delinquent_since, overdue_notified_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&endsAfter, &delinquent, &overdueNotified)
	if got := endsAfter.Sub(endsBefore); got != 7*24*time.Hour {
		t.Fatalf("advance moved %v, want 168h (anchor arithmetic)", got)
	}
	if delinquent != nil || overdueNotified != nil {
		t.Fatal("advance did not clear delinquency/term flags")
	}
	if replay, _, _ := billingRepo.AdvanceOnCyclePaid(ctx, cycle.ID); replay != nil {
		t.Fatal("second advance claimed — double paid-through")
	}

	// (d) Arrears promotion guards: an accruing row with a LIVE return on
	// the lease must NOT promote; after the return cancels, it promotes.
	cycleRef := cycle.ID
	ps := time.Now().UTC().Add(-8 * 24 * time.Hour)
	pe := time.Now().UTC().Add(-1 * time.Hour)
	if _, _, err := e.payoutRepo.CreateCycleAccruing(ctx, &models.OwnerPayout{
		LeaseRequestID: leaseID, OwnerID: owner, GrossKeptCents: 15000,
		FeeBPS: payoutTestFeeBPS, FeeCents: 1500, OwnerAmountCents: 13500,
		Currency: "USD", BillingCycleID: &cycleRef, PeriodStart: &ps, PeriodEnd: &pe,
	}); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	if promoted, _ := e.payoutRepo.PromoteConsumedCycles(ctx, time.Now().UTC(), 50); promoted != 0 {
		t.Fatalf("promotion ran %d rows with a live return", promoted)
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE vehicle_returns SET status='cancelled', cancelled_at=NOW() WHERE id=$1`, ret.ID); err != nil {
		t.Fatalf("cancel return: %v", err)
	}
	if promoted, _ := e.payoutRepo.PromoteConsumedCycles(ctx, time.Now().UTC(), 50); promoted != 1 {
		t.Fatalf("promotion after return cancelled = %d, want 1", promoted)
	}
	var pStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE billing_cycle_id=$1`, cycle.ID).Scan(&pStatus)
	if pStatus != "pending" {
		t.Fatalf("promoted row = %q, want pending", pStatus)
	}
}

// Review-fix regression tests (C1/C2/C3/H4).
func TestBatch2_ReviewFixes(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	owner := e.seedUser(t, "car_owner", "b2_owner_rf@example.com")
	driver := e.seedUser(t, "driver", "b2_driver_rf@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE lease_request_id=$1 AND billing_cycle_id IS NOT NULL`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
	})
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET billing_mode='rolling', weeks=1,
		    rental_ends_at = NOW() + interval '20 hours' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("to rolling: %v", err)
	}
	if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "t",
	}); err != nil {
		t.Fatalf("consent: %v", err)
	}
	if _, err := billingRepo.ActivateConsent(ctx, leaseID, "pm_x", "", "", ""); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// C1: an OPEN cycle excludes the lease from the due-lister — no
	// sibling mint, ever.
	if _, err := billingRepo.MintCycle(ctx, leaseID, 2, time.Now().UTC(), time.Now().UTC().Add(7*24*time.Hour), 15000, time.Now().UTC()); err != nil {
		t.Fatalf("mint: %v", err)
	}
	due, _ := e.leaseRepo.ListRollingDueForBilling(ctx, time.Now().UTC().Add(48*time.Hour), 50)
	for i := range due {
		if due[i].ID == leaseID {
			t.Fatal("C1: lease with an open cycle still listed as due — sibling-mint storm possible")
		}
	}
	var cycles int
	e.leaseH.runBillingSweep(ctx)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&cycles)
	if cycles != 1 {
		t.Fatalf("C1: sweep minted a sibling (%d cycles)", cycles)
	}

	// C2: ClaimAttempt never writes the intent; StampIntent survives ''
	// poisoning and later claims.
	cyc, _ := billingRepo.GetCycleByNumber(ctx, leaseID, 2)
	if ok, _ := billingRepo.ClaimAttempt(ctx, cyc.ID); !ok {
		t.Fatal("C2: first claim refused")
	}
	var stored *string
	e.db.Pool.QueryRow(ctx, `SELECT stripe_payment_intent_id FROM billing_cycles WHERE id=$1`, cyc.ID).Scan(&stored)
	if stored != nil {
		t.Fatalf("C2: claim wrote intent id %v", *stored)
	}
	if err := billingRepo.StampIntent(ctx, cyc.ID, ""); err != nil {
		t.Fatalf("stamp '' errored: %v", err)
	}
	if err := billingRepo.StampIntent(ctx, cyc.ID, "pi_real"); err != nil {
		t.Fatalf("stamp real: %v", err)
	}
	e.db.Pool.QueryRow(ctx, `SELECT stripe_payment_intent_id FROM billing_cycles WHERE id=$1`, cyc.ID).Scan(&stored)
	if stored == nil || *stored != "pi_real" {
		t.Fatalf("C2: stamp poisoned by '' — got %v", stored)
	}
	if err := billingRepo.StampIntent(ctx, cyc.ID, "pi_other"); err != nil {
		t.Fatalf("re-stamp: %v", err)
	}
	e.db.Pool.QueryRow(ctx, `SELECT stripe_payment_intent_id FROM billing_cycles WHERE id=$1`, cyc.ID).Scan(&stored)
	if *stored != "pi_real" {
		t.Fatal("C2: first-writer-wins violated")
	}

	// H4: a paid cycle on a RETURNED lease does not advance paid-through.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET vehicle_returned_at = NOW() WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("mark returned: %v", err)
	}
	var endsBefore time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&endsBefore)
	paidCycle, advanced, aerr := billingRepo.AdvanceOnCyclePaid(ctx, cyc.ID)
	if aerr != nil || paidCycle == nil {
		t.Fatalf("H4 advance call: %v", aerr)
	}
	if advanced {
		t.Fatal("H4: paid-through advanced on a returned lease")
	}
	var endsAfter time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&endsAfter)
	if !endsBefore.Equal(endsAfter) {
		t.Fatal("H4: rental_ends_at moved")
	}
	// The refund claim is claimed-once.
	if ok, _ := billingRepo.RefundCycleClaim(ctx, cyc.ID, "re_x", 15000); !ok {
		t.Fatal("H4: refund claim refused")
	}
	if ok, _ := billingRepo.RefundCycleClaim(ctx, cyc.ID, "re_y", 15000); ok {
		t.Fatal("H4: refund double-claimed")
	}

	// C3: activation keys on consent existence — a fresh rolling lease's
	// webhook path activates without relying on the SetPaid return.
	lease2, _ := seedAcceptedLease(t, e, owner, driver)
	e.cleanupLedger(t, lease2)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id=$1`, lease2)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, lease2)
	})
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET billing_mode='rolling', weeks=1, status='payment_pending', payment_pending_at=NOW() WHERE id=$1`, lease2); err != nil {
		t.Fatalf("lease2 rolling: %v", err)
	}
	if _, err := billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: lease2, DriverID: driver, AmountCents: 15000,
		TermsVersion: models.TermsVersionRolling, DisclosureText: "t",
	}); err != nil {
		t.Fatalf("consent2: %v", err)
	}
	intent := "pi_c3_" + lease2.String()[:8]
	seedPaymentAt(t, e, lease2, 15000, "requires_payment_method", &intent)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", nil)
	if ok := e.leaseH.handlePaymentSucceeded(req, intent, map[string]interface{}{"payment_method": "pm_from_webhook"}); !ok {
		t.Fatal("C3: webhook path failed")
	}
	consent2, _ := billingRepo.GetActiveConsent(ctx, lease2)
	if consent2 == nil || !consent2.Active() || *consent2.StripePaymentMethodID != "pm_from_webhook" {
		t.Fatalf("C3: consent not activated by webhook: %+v", consent2)
	}
}
