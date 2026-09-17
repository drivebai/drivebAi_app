package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Monthly rehearsal lines (recurring-only, 2026-09-17). Same harness, same
// real Stripe test clocks as the weekly lines; the only difference is the
// interval the consent was shown on. Runs only under REHEARSAL=1.
//
//	REHEARSAL=1 STRIPE_SECRET_KEY=sk_test_… TEST_DATABASE_URL=… \
//	  go test ./internal/handlers/ -run 'Batch5_Monthly' -v
const (
	rehearsalMonthlyCents  = 60000 // $600.00 = weekly 150.00 × RentMonthWeeks
	rehearsalMonthlyDays   = 28
	rehearsalMonthlyPerDay = rehearsalMonthlyCents / rehearsalMonthlyDays // 2142 (floor)
	monthlyLen             = 28 * 24 * time.Hour
)

// startMonthlyRental is startRollingRental with the interval the lease and
// consent record set to monthly. The consent text is the monthly package.
func (e *rehearsalEnv) startMonthlyRental(t *testing.T, tag, cardToken string, clockID string, now time.Time) *rehearsalLease {
	t.Helper()
	ctx := context.Background()
	runID := uuid.New().String()[:8]
	owner := e.seedUser(t, "car_owner", "rhm_o_"+tag+"_"+runID+"@example.com")
	driver := e.seedUser(t, "driver", "rhm_d_"+tag+"_"+runID+"@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM charge_disputes WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_amendment_offers WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
	})

	cust := e.call(t, "POST", "customers", url.Values{
		"test_clock": {clockID},
		"email":      {"rhm_" + tag + "_" + runID + "@example.com"},
	})
	customerID := str(cust, "id")
	if customerID == "" {
		t.Fatalf("create clocked customer: %v", cust)
	}
	if err := repository.NewUserRepository(e.db).SetStripeCustomerID(ctx, driver, customerID); err != nil {
		t.Fatalf("bind customer: %v", err)
	}
	pmID, pmBrand, pmLast4 := e.newCardPMDetail(t, cardToken)
	e.attachPM(t, pmID, customerID)

	// What the app's checkout produces for a MONTHLY listing: the lease is
	// rolling on the monthly interval and the consent records that interval
	// with the monthly package (v2) and the monthly amount.
	// In production the interval is set at request creation, BEFORE pickup,
	// so ConfirmPickup already anchors rental_ends_at one month out. The
	// fixture confirmed pickup as a weekly lease, so re-anchor it exactly as
	// ConfirmPickup would have (lease_request_repository.go ConfirmPickup).
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE lease_requests SET billing_mode='rolling', billing_interval='monthly', weeks=1,
		        rental_ends_at = pickup_confirmed_at + INTERVAL '28 days' WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("set monthly rolling: %v", err)
	}
	text, version := models.RollingDisclosureFor("monthly", rehearsalMonthlyCents)
	if _, err := e.billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: rehearsalMonthlyCents,
		BillingInterval: "monthly", TermsVersion: version, DisclosureText: text,
	}); err != nil {
		t.Fatalf("consent: %v", err)
	}
	var storedInterval string
	e.db.Pool.QueryRow(ctx, `SELECT billing_interval FROM lease_billing_consents WHERE lease_request_id=$1 AND revoked_at IS NULL`, leaseID).Scan(&storedInterval)
	if storedInterval != "monthly" {
		t.Fatalf("consent stored interval %q, want monthly", storedInterval)
	}

	pi := e.bookingCharge(t, customerID, pmID, rehearsalMonthlyCents, nil)
	intentID := str(pi, "id")
	seedPaymentAt(t, e.payoutEnv, leaseID, rehearsalMonthlyCents, "succeeded", &intentID)
	if ok, err := e.billingRepo.ActivateConsent(ctx, leaseID, str(pi, "payment_method"), pmBrand, pmLast4, "fp_rehearsal_m"); err != nil || !ok {
		t.Fatalf("activate consent: ok=%v err=%v", ok, err)
	}
	var pickup time.Time
	e.db.Pool.QueryRow(ctx, `SELECT pickup_confirmed_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&pickup)

	e.leaseH.runBillingSweep(ctx) // bootstrap month 1
	return &rehearsalLease{leaseID: leaseID, carID: carID, owner: owner, driver: driver,
		customerID: customerID, pmID: pmID, clockID: clockID, pickup: pickup}
}

// Month 1 is cycled at 28 days and month 2 charges itself for the monthly
// amount, moving paid-through by exactly 28 days.
func TestBatch5_MonthlyLine1_RecurringCharge(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	clock := e.createTestClock(t, now)
	L := e.startMonthlyRental(t, "m1", cardSuccess, clock, now)

	cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
	if len(cycles) != 1 || cycles[0].Status != models.CyclePaid || cycles[0].AmountCents != rehearsalMonthlyCents {
		t.Fatalf("month 1 not cycled as paid %d: %+v", rehearsalMonthlyCents, cycles)
	}
	if span := cycles[0].PeriodEnd.Sub(cycles[0].PeriodStart); span != monthlyLen {
		t.Fatalf("month 1 spans %v, want %v", span, monthlyLen)
	}

	e.ageLease(t, L.leaseID, monthlyLen)
	before := e.paidThrough(t, L.leaseID)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, rehearsalMonthlyDays))
	cycle := e.sweepAndSettle(t, L.leaseID)
	if cycle == nil || cycle.Status != models.CyclePaid {
		t.Fatalf("month 2 did not charge automatically: %+v", cycle)
	}
	if cycle.AmountCents != rehearsalMonthlyCents {
		t.Errorf("month 2 charged %d, want %d", cycle.AmountCents, rehearsalMonthlyCents)
	}
	pi := e.call(t, "GET", "payment_intents/"+strOrEmpty(cycle.StripePaymentIntentID), nil)
	if str(pi, "status") != "succeeded" || num(pi, "amount") != rehearsalMonthlyCents {
		t.Errorf("stripe: status=%s amount=%d, want succeeded %d", str(pi, "status"), num(pi, "amount"), rehearsalMonthlyCents)
	}
	if got := e.paidThrough(t, L.leaseID).Sub(before); got != monthlyLen {
		t.Errorf("paid-through moved %v, want %v", got, monthlyLen)
	}
	t.Logf("month 2: cycle #%d paid $%.2f | pi=%s | paid-through +%v", cycle.CycleNumber, float64(cycle.AmountCents)/100, str(pi, "id"), monthlyLen)
}

// The decline ladder is the same 4 × 24h regardless of interval; the driver
// keeps the car; exactly one Stripe intent is minted for the failing cycle.
func TestBatch5_MonthlyLine2_DeclineLadder(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	clock := e.createTestClock(t, now)
	L := e.startMonthlyRental(t, "m2", cardSuccess, clock, now)

	badPM := e.newCardPM(t, cardDecline)
	e.attachPM(t, badPM, L.customerID)
	if ok, err := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, badPM, "visa", "0002", "fp_decline_m"); err != nil || !ok {
		t.Fatalf("swap to declining card: %v", err)
	}
	e.ageLease(t, L.leaseID, monthlyLen)

	var last *models.BillingCycle
	for attempt := 1; attempt <= 5; attempt++ {
		e.leaseH.runBillingSweep(ctx)
		cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
		last = cycles[len(cycles)-1]
		lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
		t.Logf("attempt %d: cycle=%s attempts=%d decline=%q delinquent=%v halt=%q",
			attempt, last.Status, last.AttemptCount, strOrEmpty(last.LastDeclineCode), lr.DelinquentSince != nil, strOrEmpty(lr.RenewalHaltedReason))
		if last.Status == models.CycleFailedFinal {
			break
		}
		if _, err := e.db.Pool.Exec(ctx, `UPDATE billing_cycles SET next_attempt_at = NOW() - interval '1 minute' WHERE id=$1`, last.ID); err != nil {
			t.Fatalf("age retry: %v", err)
		}
	}
	if last.Status != models.CycleFailedFinal || last.AttemptCount != models.BillingMaxAttempts {
		t.Errorf("ladder: status=%s attempts=%d, want failed_final after %d", last.Status, last.AttemptCount, models.BillingMaxAttempts)
	}
	lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	if lr.DelinquentSince == nil || strOrEmpty(lr.RenewalHaltedReason) != "delinquent" || lr.Status != models.LeaseStatusPaid {
		t.Errorf("after ladder: delinquent=%v halt=%q status=%s", lr.DelinquentSince != nil, strOrEmpty(lr.RenewalHaltedReason), lr.Status)
	}
	if n := e.countPIsForCycle(t, L.customerID, last.ID); n != 1 {
		t.Errorf("stripe intents for the failing month = %d, want exactly 1", n)
	}
}

// The case named in the task: return on day 20 of a 28-day month. Used
// days = 20; refund = 8 × per-day + the rounding remainder; exactly one
// Stripe refund; the owner keeps 20 × per-day.
func TestBatch5_MonthlyLine3_MidCycleReturnDay20(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	clock := e.createTestClock(t, now)
	L := e.startMonthlyRental(t, "m3", cardSuccess, clock, now)

	e.ageLease(t, L.leaseID, monthlyLen)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, rehearsalMonthlyDays))
	cycle := e.sweepAndSettle(t, L.leaseID)
	if cycle == nil || cycle.Status != models.CyclePaid {
		t.Fatalf("month 2 not paid: %+v", cycle)
	}
	// 20 days into month 2 — one hour short of the boundary, so the
	// ceil(elapsed/86400) rule lands on day 20, not 21 (the same 'and change'
	// the weekly line allows for).
	e.ageLease(t, L.leaseID, 20*24*time.Hour-time.Hour)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, rehearsalMonthlyDays+20))

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("initiate return: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, L.owner, ret.ID, `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner confirm: %d (%s)", rr.Code, rr.Body.String())
	}
	e.leaseH.runBillingSweep(ctx)

	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	after, _ := e.billingRepo.GetCycle(ctx, cycle.ID)
	if after.AmountCents != rehearsalMonthlyCents {
		t.Fatalf("month 2 charged %d, want %d", after.AmountCents, rehearsalMonthlyCents)
	}
	const wantUsedDays = 20
	remainder := int64(rehearsalMonthlyCents % rehearsalMonthlyDays)
	wantRefund := int64((rehearsalMonthlyDays-wantUsedDays)*rehearsalMonthlyPerDay) + remainder
	wantKept := int64(wantUsedDays * rehearsalMonthlyPerDay)
	if ret.UsedDays != wantUsedDays {
		t.Errorf("used days = %d, want %d", ret.UsedDays, wantUsedDays)
	}
	cross := models.ComputeReturnRefundOverDays(after.AmountCents, models.DaysInPeriod(after.PeriodStart, after.PeriodEnd), after.PeriodStart, ret.ReturnedAt)
	if cross.RefundAmountCents != wantRefund {
		t.Errorf("engine formula %d vs independent arithmetic %d", cross.RefundAmountCents, wantRefund)
	}
	refunds := e.call(t, "GET", "refunds?payment_intent="+strOrEmpty(after.StripePaymentIntentID), nil)
	var refundCount int
	var refundTotal int64
	if data, ok := refunds["data"].([]interface{}); ok {
		for _, r := range data {
			refundCount++
			refundTotal += num(r.(map[string]interface{}), "amount")
		}
	}
	if refundCount != 1 || refundTotal != wantRefund {
		t.Errorf("stripe refunded %d refund(s) totalling %d, want exactly 1 of %d", refundCount, refundTotal, wantRefund)
	}
	var kept int64
	var payoutStatus string
	e.db.Pool.QueryRow(ctx, `SELECT gross_kept_cents, status FROM owner_payouts WHERE billing_cycle_id=$1`, after.ID).Scan(&kept, &payoutStatus)
	if kept != wantKept {
		t.Errorf("owner kept %d, want %d (20 used days × %d)", kept, wantKept, rehearsalMonthlyPerDay)
	}
	t.Logf("day-20 return: used=%d refund=$%.2f kept=$%.2f payout=%s", ret.UsedDays, float64(refundTotal)/100, float64(kept)/100, payoutStatus)
}

// A dispute on month 2 withholds the owner's payout and halts renewals,
// exactly as on a weekly cycle.
func TestBatch5_MonthlyLine4_Dispute(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	clock := e.createTestClock(t, now)
	L := e.startMonthlyRental(t, "m4", cardSuccess, clock, now)

	dpPM := e.newCardPM(t, cardDispute)
	e.attachPM(t, dpPM, L.customerID)
	if ok, _ := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, dpPM, "visa", "0259", "fp_dispute_m"); !ok {
		t.Fatalf("swap to dispute card")
	}
	e.ageLease(t, L.leaseID, monthlyLen)
	cycle := e.sweepAndSettle(t, L.leaseID)
	if cycle == nil || cycle.Status != models.CyclePaid {
		t.Fatalf("month 2 charge did not settle: %+v", cycle)
	}
	pi := e.call(t, "GET", "payment_intents/"+strOrEmpty(cycle.StripePaymentIntentID), nil)
	chargeID := str(pi, "latest_charge")
	var dispute map[string]interface{}
	for i := 0; i < 20 && dispute == nil; i++ {
		out := e.call(t, "GET", "disputes?charge="+chargeID, nil)
		if data, ok := out["data"].([]interface{}); ok && len(data) > 0 {
			dispute = data[0].(map[string]interface{})
		} else {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	if dispute == nil {
		t.Fatalf("stripe never raised a dispute for charge %s", chargeID)
	}
	if code := e.deliverWebhook(t, "charge.dispute.created", dispute); code != http.StatusOK {
		t.Fatalf("dispute.created webhook → %d", code)
	}
	var payoutStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE billing_cycle_id=$1`, cycle.ID).Scan(&payoutStatus)
	if payoutStatus != "withheld" {
		t.Errorf("owner payout = %q, want withheld while the dispute is open", payoutStatus)
	}
	lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	if strOrEmpty(lr.RenewalHaltedReason) != "dispute" {
		t.Errorf("halt = %q, want 'dispute'", strOrEmpty(lr.RenewalHaltedReason))
	}
	t.Logf("month 2 disputed: %s status=%s payout=%s halt=%s", str(dispute, "id"), str(dispute, "status"), payoutStatus, strOrEmpty(lr.RenewalHaltedReason))
}
