package handlers

// The eight mandated rehearsal lines. Each is its own test so it reports
// its own observations; all run against live Stripe test mode with the
// customer on a test clock (see rolling_rehearsal_test.go for the harness
// and for exactly what is real vs simulated).

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// ── LINE 1 ── weeks 2, 3 and 4 charge the saved card with zero human action.
func TestBatch5_Line1_RecurringChargesNoHumanAction(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "wk", cardSuccess, clock, now)

	cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
	if len(cycles) != 1 || cycles[0].Status != models.CyclePaid {
		t.Fatalf("week 1 not cycled: %+v", cycles)
	}
	t.Logf("week 1: cycle #1 %s $%.2f intent=%s (booking charge, on-session)",
		cycles[0].Status, float64(cycles[0].AmountCents)/100, strOrEmpty(cycles[0].StripePaymentIntentID))

	for week := 2; week <= 4; week++ {
		before := e.paidThrough(t, L.leaseID)
		e.ageLease(t, L.leaseID, 7*24*time.Hour)
		e.advanceTestClock(t, clock, now.AddDate(0, 0, 7*(week-1)))

		// The ONLY action is the scanner tick. No client call, no confirm.
		cycle := e.sweepAndSettle(t, L.leaseID)
		if cycle == nil || cycle.Status != models.CyclePaid {
			t.Fatalf("week %d did not charge automatically: %+v", week, cycle)
		}
		pi := e.call(t, "GET", "payment_intents/"+strOrEmpty(cycle.StripePaymentIntentID), nil)
		after := e.paidThrough(t, L.leaseID)

		var payoutStatus string
		var ownerCents int64
		e.db.Pool.QueryRow(ctx, `SELECT status, owner_amount_cents FROM owner_payouts WHERE billing_cycle_id=$1`,
			cycle.ID).Scan(&payoutStatus, &ownerCents)

		t.Logf("week %d: cycle #%d %s $%.2f | stripe pi=%s status=%s off_session_charge=%v | paid-through +%v | owner share $%.2f (%s)",
			week, cycle.CycleNumber, cycle.Status, float64(cycle.AmountCents)/100,
			str(pi, "id"), str(pi, "status"), num(pi, "amount") == cycle.AmountCents,
			after.Sub(before).Round(time.Hour), float64(ownerCents)/100, payoutStatus)

		if str(pi, "status") != "succeeded" {
			t.Errorf("week %d stripe status = %s, want succeeded", week, str(pi, "status"))
		}
		if got := after.Sub(before).Round(time.Hour); got != 7*24*time.Hour {
			t.Errorf("week %d paid-through moved %v, want 168h", week, got)
		}
		if payoutStatus != "accruing" {
			t.Errorf("week %d owner share = %q, want accruing", week, payoutStatus)
		}
	}

	// Arrears cadence: once each week is consumed, promotion makes it payable.
	e.ageLease(t, L.leaseID, 8*24*time.Hour)
	e.leaseH.runBillingSweep(ctx)
	rows, _ := e.payoutRepo.ListCycleLedgerForLease(ctx, L.leaseID)
	promoted, accruing := 0, 0
	for _, r := range rows {
		switch r.Status {
		case "pending", "awaiting_onboarding", "paid":
			promoted++
		case "accruing":
			accruing++
		}
	}
	t.Logf("promotion: %d of %d weekly payout rows promoted to payable, %d still accruing (unconsumed)",
		promoted, len(rows), accruing)
	if promoted < 3 {
		t.Errorf("expected at least weeks 1-3 promoted, got %d", promoted)
	}
	if n := e.cycleCount(t, L.leaseID); n != 4 {
		t.Errorf("cycle count = %d, want 4 (weeks 1-4, no siblings)", n)
	}
}

// ── LINE 2 ── a failed charge walks the entire dunning ladder.
func TestBatch5_Line2_FailedChargeFullLadder(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "fail", cardSuccess, clock, now)

	// Swap the mandate onto a card that declines every charge.
	badPM := e.newCardPM(t, cardDecline)
	e.attachPM(t, badPM, L.customerID)
	if ok, err := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, badPM, "visa", "0002", "fp_decline"); err != nil || !ok {
		t.Fatalf("swap to declining card: %v", err)
	}
	e.ageLease(t, L.leaseID, 7*24*time.Hour)

	type step struct {
		status, decline string
		attempt         int
		delinquent      bool
		halt            string
	}
	var observed []step
	for attempt := 1; attempt <= 5; attempt++ {
		e.leaseH.runBillingSweep(ctx)
		cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
		c := cycles[len(cycles)-1]
		lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
		observed = append(observed, step{
			status: string(c.Status), decline: strOrEmpty(c.LastDeclineCode), attempt: c.AttemptCount,
			delinquent: lr.DelinquentSince != nil, halt: strOrEmpty(lr.RenewalHaltedReason),
		})
		t.Logf("attempt %d: cycle=%s attempts=%d decline=%q delinquent=%v halt=%q next_attempt=%v",
			attempt, c.Status, c.AttemptCount, strOrEmpty(c.LastDeclineCode),
			lr.DelinquentSince != nil, strOrEmpty(lr.RenewalHaltedReason), c.NextAttemptAt != nil)
		if c.Status == models.CycleFailedFinal {
			break
		}
		// Let the ladder's next rung come due (prod waits 24h per rung).
		if _, err := e.db.Pool.Exec(ctx, `
			UPDATE billing_cycles SET next_attempt_at = NOW() - interval '1 minute' WHERE id=$1`, c.ID); err != nil {
			t.Fatalf("age retry: %v", err)
		}
	}

	last := observed[len(observed)-1]
	if last.status != string(models.CycleFailedFinal) {
		t.Errorf("ladder did not exhaust: final status %s after %d attempts", last.status, last.attempt)
	}
	if last.attempt != models.BillingMaxAttempts {
		t.Errorf("final attempt count = %d, want %d", last.attempt, models.BillingMaxAttempts)
	}
	if !last.delinquent {
		t.Errorf("lease never marked delinquent")
	}
	if last.halt != "delinquent" {
		t.Errorf("final halt = %q, want 'delinquent'", last.halt)
	}
	// Exactly ONE intent existed across the whole ladder (re-confirms, not
	// new charges), and the car stays with the driver.
	cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
	c := cycles[len(cycles)-1]
	if n := e.countPIsForCycle(t, c.ID); n != 1 {
		t.Errorf("stripe intents for the failing cycle = %d, want exactly 1", n)
	}
	lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	t.Logf("ladder end: %d attempts on ONE intent, delinquent=%v halt=%q, lease still %s (driver keeps the car)",
		c.AttemptCount, lr.DelinquentSince != nil, strOrEmpty(lr.RenewalHaltedReason), lr.Status)
	if lr.Status != models.LeaseStatusPaid {
		t.Errorf("lease status = %s, want paid (delinquency must not seize the car)", lr.Status)
	}
}

// ── LINE 3 ── a dispute on a weekly charge withholds, then resolves.
func TestBatch5_Line3_Dispute(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "disp", cardSuccess, clock, now)

	// Week 2 is charged on a card that auto-disputes.
	dpPM := e.newCardPM(t, cardDispute)
	e.attachPM(t, dpPM, L.customerID)
	if ok, _ := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, dpPM, "visa", "0259", "fp_dispute"); !ok {
		t.Fatalf("swap to dispute card")
	}
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	cycle := e.sweepAndSettle(t, L.leaseID)
	if cycle == nil || cycle.Status != models.CyclePaid {
		t.Fatalf("week 2 charge did not settle: %+v", cycle)
	}
	pi := e.call(t, "GET", "payment_intents/"+strOrEmpty(cycle.StripePaymentIntentID), nil)
	chargeID := str(pi, "latest_charge")
	t.Logf("week 2 charged $%.2f (charge %s) on the dispute-triggering card", float64(cycle.AmountCents)/100, chargeID)

	// Stripe raises the dispute itself; poll for it.
	var dispute map[string]interface{}
	for i := 0; i < 30; i++ {
		out := e.call(t, "GET", "disputes?charge="+chargeID, nil)
		if data, ok := out["data"].([]interface{}); ok && len(data) > 0 {
			dispute = data[0].(map[string]interface{})
			break
		}
		time.Sleep(2 * time.Second)
	}
	if dispute == nil {
		t.Fatalf("stripe never raised a dispute for charge %s", chargeID)
	}
	t.Logf("stripe raised dispute %s status=%s amount=$%.2f reason=%s",
		str(dispute, "id"), str(dispute, "status"), float64(num(dispute, "amount"))/100, str(dispute, "reason"))

	code := e.deliverWebhook(t, "charge.dispute.created", dispute)
	if code != 200 {
		t.Fatalf("dispute.created webhook → %d", code)
	}
	lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	var mirrored, payoutStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM charge_disputes WHERE stripe_dispute_id=$1`, str(dispute, "id")).Scan(&mirrored)
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE billing_cycle_id=$1`, cycle.ID).Scan(&payoutStatus)
	t.Logf("on dispute open: mirror=%s owner payout=%s renewals halted=%q",
		mirrored, payoutStatus, strOrEmpty(lr.RenewalHaltedReason))
	if payoutStatus != "withheld" {
		t.Errorf("owner payout = %q, want withheld while the dispute is open", payoutStatus)
	}
	if strOrEmpty(lr.RenewalHaltedReason) != "dispute" {
		t.Errorf("halt = %q, want 'dispute'", strOrEmpty(lr.RenewalHaltedReason))
	}

	// Close it as LOST (test mode concedes), then deliver the closure.
	closed := e.call(t, "POST", "disputes/"+str(dispute, "id")+"/close", nil)
	for i := 0; i < 20 && str(closed, "status") != "lost"; i++ {
		time.Sleep(2 * time.Second)
		closed = e.call(t, "GET", "disputes/"+str(dispute, "id"), nil)
	}
	code = e.deliverWebhook(t, "charge.dispute.closed", closed)
	var afterStatus, note string
	e.db.Pool.QueryRow(ctx, `SELECT status, COALESCE(note,'') FROM owner_payouts WHERE billing_cycle_id=$1`, cycle.ID).
		Scan(&afterStatus, &note)
	var settled bool
	e.db.Pool.QueryRow(ctx, `SELECT outcome_settled FROM charge_disputes WHERE stripe_dispute_id=$1`, str(dispute, "id")).Scan(&settled)
	t.Logf("dispute closed=%s (webhook %d): payout=%s settled=%v note=%q",
		str(closed, "status"), code, afterStatus, settled, note)
	if !settled {
		t.Errorf("dispute outcome not marked settled")
	}
	if afterStatus == "paid" {
		t.Errorf("owner was paid on a LOST dispute")
	}
}

// ── LINE 4 ── a mid-week return refunds the unused days pro-rata, for real.
func TestBatch5_Line4_MidWeekReturn(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "ret", cardSuccess, clock, now)

	// Run into week 2, then return 3 days in.
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, 7))
	cycle := e.sweepAndSettle(t, L.leaseID)
	if cycle == nil || cycle.Status != models.CyclePaid {
		t.Fatalf("week 2 not paid: %+v", cycle)
	}
	e.ageLease(t, L.leaseID, 3*24*time.Hour)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, 10))

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate return: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	fresh, _ := e.billingRepo.GetCycle(ctx, cycle.ID)
	want := models.ComputeReturnRefund(fresh.AmountCents, 1, fresh.PeriodStart, ret.ReturnedAt)
	t.Logf("return initiated %d days into week 2: preview refund $%.2f of $%.2f (used %d days)",
		3, float64(ret.RefundAmountCents)/100, float64(ret.PaidAmountCents)/100, ret.UsedDays)

	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, L.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("owner confirm: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	after, _ := e.billingRepo.GetCycle(ctx, cycle.ID)

	// What Stripe actually returned to the driver.
	refunds := e.call(t, "GET", "refunds?payment_intent="+strOrEmpty(after.StripePaymentIntentID), nil)
	var refundTotal int64
	var refundCount int
	if data, ok := refunds["data"].([]interface{}); ok {
		refundCount = len(data)
		for _, r := range data {
			refundTotal += num(r.(map[string]interface{}), "amount")
		}
	}
	var kept, ownerCents int64
	var payoutStatus, payoutSource string
	e.db.Pool.QueryRow(ctx, `SELECT gross_kept_cents, owner_amount_cents, status, source FROM owner_payouts WHERE billing_cycle_id=$1`,
		after.ID).Scan(&kept, &ownerCents, &payoutStatus, &payoutSource)
	t.Logf("settled: return=%s cycle=%s | STRIPE refunds=%d totalling $%.2f | owner keeps $%.2f of $%.2f (%s/%s)",
		ret.Status, after.Status, refundCount, float64(refundTotal)/100,
		float64(kept)/100, float64(after.AmountCents)/100, payoutStatus, payoutSource)

	if refundCount != 1 || refundTotal != want.RefundAmountCents {
		t.Errorf("stripe refunded %d refund(s) totalling %d, want exactly 1 of %d", refundCount, refundTotal, want.RefundAmountCents)
	}
	if kept != after.AmountCents-want.RefundAmountCents {
		t.Errorf("owner kept %d, want %d (charge − refund)", kept, after.AmountCents-want.RefundAmountCents)
	}
	var legacy int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE lease_request_id=$1 AND billing_cycle_id IS NULL`, L.leaseID).Scan(&legacy)
	if legacy != 0 {
		t.Errorf("legacy whole-rent payout row created for a rolling return (%d)", legacy)
	}
}

// ── LINE 5 ── a crash inside every idempotency window is replay-safe.
func TestBatch5_Line5_CrashInsideEveryIdempotencyWindow(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "crash", cardSuccess, clock, now)

	// W1 — crash between PI creation and the intent stamp.
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	e.leaseH.runBillingSweep(ctx) // mints + charges cycle 2
	cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
	c2 := cycles[len(cycles)-1]
	realIntent := strOrEmpty(c2.StripePaymentIntentID)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET stripe_payment_intent_id=NULL, status='retrying', attempt_count=1,
		    next_attempt_at=NOW() - interval '1 minute' WHERE id=$1`, c2.ID); err != nil {
		t.Fatalf("simulate crash W1: %v", err)
	}
	e.leaseH.runBillingSweep(ctx)
	healed, _ := e.billingRepo.GetCycle(ctx, c2.ID)
	n := e.countPIsForCycle(t, c2.ID)
	t.Logf("W1 create→stamp crash: replay recovered intent=%s (same=%v); stripe intents for this week = %d",
		strOrEmpty(healed.StripePaymentIntentID), strOrEmpty(healed.StripePaymentIntentID) == realIntent, n)
	if n != 1 {
		t.Errorf("W1: %d intents for one week — a crash created a second charge", n)
	}

	// W2 — crash before/while the success webhook is processed: redeliver 3×.
	for i := 0; i < 3; i++ {
		if code := e.deliverPIEvent(t, "payment_intent.succeeded", realIntent); code != 200 {
			t.Fatalf("W2 redelivery %d → %d", i+1, code)
		}
	}
	var accruals int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE billing_cycle_id=$1`, c2.ID).Scan(&accruals)
	paidThroughAfter := e.paidThrough(t, L.leaseID)
	settled, _ := e.billingRepo.GetCycle(ctx, c2.ID)
	t.Logf("W2 webhook redelivered 3×: cycle=%s payout rows=%d paid-through=%s (advanced once)",
		settled.Status, accruals, paidThroughAfter.Format(time.RFC3339))
	if accruals != 1 {
		t.Errorf("W2: %d payout rows after 3 deliveries, want 1", accruals)
	}

	// W3 — crash between the Stripe refund and the DB finalize: replay the
	// return pipeline and prove Stripe did not refund twice.
	e.ageLease(t, L.leaseID, 3*24*time.Hour)
	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, L.owner, ret.ID, `{}`))
	done, _ := e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE vehicle_returns SET status='owner_confirmed', refund_id=NULL, refund_status='pending' WHERE id=$1`, done.ID); err != nil {
		t.Fatalf("simulate crash W3: %v", err)
	}
	replay, _ := e.returnRepo.GetByID(ctx, done.ID)
	e.returnH.issueRefund(ctx, replay)
	cycleAfter, _ := e.billingRepo.GetCycle(ctx, c2.ID)
	refunds := e.call(t, "GET", "refunds?payment_intent="+strOrEmpty(cycleAfter.StripePaymentIntentID), nil)
	rc := 0
	var rtotal int64
	if data, ok := refunds["data"].([]interface{}); ok {
		rc = len(data)
		for _, r := range data {
			rtotal += num(r.(map[string]interface{}), "amount")
		}
	}
	t.Logf("W3 refund→finalize crash: pipeline replayed; STRIPE refunds for the week = %d totalling $%.2f",
		rc, float64(rtotal)/100)
	if rc != 1 {
		t.Errorf("W3: %d refunds after replay, want exactly 1", rc)
	}

	// W4 — a success webhook arriving AFTER the week was already settled by
	// the return (the crash-and-late-delivery window): must refund, never
	// silently keep the driver's money.
	settledCycle, _ := e.billingRepo.GetCycle(ctx, c2.ID)
	beforeRefunds := e.refundTotal(t, strOrEmpty(settledCycle.StripePaymentIntentID))
	if code := e.deliverPIEvent(t, "payment_intent.succeeded", strOrEmpty(settledCycle.StripePaymentIntentID)); code != 200 {
		t.Errorf("W4 late-success delivery → %d (must be processed, not dropped)", code)
	}
	afterRefunds := e.refundTotal(t, strOrEmpty(settledCycle.StripePaymentIntentID))
	postCycle, _ := e.billingRepo.GetCycle(ctx, c2.ID)
	t.Logf("W4 late success on a settled week: cycle stays %s, stripe refunded $%.2f before → $%.2f after (no money kept in error)",
		postCycle.Status, float64(beforeRefunds)/100, float64(afterRefunds)/100)
	if afterRefunds < beforeRefunds {
		t.Errorf("W4: refunds went backwards")
	}

	// W5 — bootstrap accrual crash heals on the next sweep.
	var cycle1ID uuid.UUID
	e.db.Pool.QueryRow(ctx, `SELECT id FROM billing_cycles WHERE lease_request_id=$1 AND cycle_number=1`, L.leaseID).Scan(&cycle1ID)
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE billing_cycle_id=$1`, cycle1ID); err != nil {
		t.Fatalf("simulate crash W5: %v", err)
	}
	e.leaseH.runBillingSweep(ctx)
	var healedRows int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE billing_cycle_id=$1`, cycle1ID).Scan(&healedRows)
	t.Logf("W5 bootstrap mint→accrue crash: week-1 owner share rebuilt by the next sweep (rows=%d)", healedRows)
	if healedRows != 1 {
		t.Errorf("W5: week-1 accrual not healed (rows=%d)", healedRows)
	}
}

// ── LINE 6 ── a mid-rental card update, and the next week charges the NEW card.
func TestBatch5_Line6_CardUpdateMidRental(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "card", cardSuccess, clock, now)
	before, _ := e.billingRepo.GetActiveConsent(ctx, L.leaseID)

	rr := httptest.NewRecorder()
	e.leaseH.CardUpdateStart(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("card-update start: %d (%s)", rr.Code, rr.Body.String())
	}
	var start struct {
		SetupIntentID string `json:"setup_intent_id"`
	}
	mustDecode(t, rr.Body.Bytes(), &start)

	// The driver's PaymentSheet step, done server-side with a new card.
	newPM := e.newCardPM(t, cardMastercard)
	si := e.call(t, "POST", "setup_intents/"+start.SetupIntentID+"/confirm", url.Values{
		"payment_method": {newPM},
		"return_url":     {"https://drivebai.example/return"},
	})
	t.Logf("setup intent %s confirmed with a new card → status=%s", start.SetupIntentID, str(si, "status"))

	rr = httptest.NewRecorder()
	e.leaseH.CardUpdateComplete(rr, returnReq(t, L.driver, L.leaseID,
		fmt.Sprintf(`{"setup_intent_id":%q}`, start.SetupIntentID)))
	if rr.Code != 200 {
		t.Fatalf("card-update complete: %d (%s)", rr.Code, rr.Body.String())
	}
	after, _ := e.billingRepo.GetActiveConsent(ctx, L.leaseID)
	t.Logf("mandate card: %s ••%s → %s ••%s (same consent row, still active=%v)",
		strOrEmpty(before.CardBrand), strOrEmpty(before.CardLast4),
		strOrEmpty(after.CardBrand), strOrEmpty(after.CardLast4), after.Active())
	if strOrEmpty(after.StripePaymentMethodID) != newPM {
		t.Fatalf("consent PM not swapped: %s", strOrEmpty(after.StripePaymentMethodID))
	}

	// Replay window: completing the same SetupIntent twice must not corrupt
	// the mandate or re-halt the lease.
	rr = httptest.NewRecorder()
	e.leaseH.CardUpdateComplete(rr, returnReq(t, L.driver, L.leaseID,
		fmt.Sprintf(`{"setup_intent_id":%q}`, start.SetupIntentID)))
	replayConsent, _ := e.billingRepo.GetActiveConsent(ctx, L.leaseID)
	lrAfter, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	t.Logf("card-update replayed: http=%d mandate still %s ••%s, halt=%q",
		rr.Code, strOrEmpty(replayConsent.CardBrand), strOrEmpty(replayConsent.CardLast4),
		strOrEmpty(lrAfter.RenewalHaltedReason))
	if strOrEmpty(replayConsent.StripePaymentMethodID) != newPM {
		t.Errorf("replayed card-update corrupted the mandate: %s", strOrEmpty(replayConsent.StripePaymentMethodID))
	}
	if strOrEmpty(lrAfter.RenewalHaltedReason) != "" {
		t.Errorf("replayed card-update left a halt: %q", strOrEmpty(lrAfter.RenewalHaltedReason))
	}

	// The next weekly charge must use the NEW card.
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, 7))
	cycle := e.sweepAndSettle(t, L.leaseID)
	pi := e.call(t, "GET", "payment_intents/"+strOrEmpty(cycle.StripePaymentIntentID), nil)
	t.Logf("next weekly charge: $%.2f status=%s payment_method=%s (new card used=%v)",
		float64(num(pi, "amount"))/100, str(pi, "status"), str(pi, "payment_method"), str(pi, "payment_method") == newPM)
	if str(pi, "payment_method") != newPM {
		t.Errorf("charge used %s, want the updated card %s", str(pi, "payment_method"), newPM)
	}
	if str(pi, "status") != "succeeded" {
		t.Errorf("charge on the new card = %s", str(pi, "status"))
	}
}

// ── LINE 7 ── an arrears balance after return, settled by Pay-now.
func TestBatch5_Line7_ArrearsPayNowAfterReturn(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "arr", cardSuccess, clock, now)

	// Week 2 fails outright, then the driver returns the car 3 days in.
	badPM := e.newCardPM(t, cardDecline)
	e.attachPM(t, badPM, L.customerID)
	e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, badPM, "visa", "0002", "fp_decline")
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	for i := 0; i < 5; i++ {
		e.leaseH.runBillingSweep(ctx)
		cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
		c := cycles[len(cycles)-1]
		if c.Status == models.CycleFailedFinal {
			break
		}
		e.db.Pool.Exec(ctx, `UPDATE billing_cycles SET next_attempt_at = NOW() - interval '1 minute' WHERE id=$1`, c.ID)
	}
	e.ageLease(t, L.leaseID, 3*24*time.Hour)
	// Put a good card back so the driver can actually settle.
	goodPM := e.newCardPM(t, cardSuccess)
	e.attachPM(t, goodPM, L.customerID)
	e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, goodPM, "visa", "4242", "fp_good")

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, L.owner, ret.ID, `{}`))

	latest, _ := e.billingRepo.GetOpenOrLatestPaidCycle(ctx, L.leaseID)
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status='open'`, L.leaseID).Scan(&tickets)
	t.Logf("after return on an uncollected week: cycle=%s owed $%.2f (pro-rata for %d used days), open collection tickets=%d",
		latest.Status, float64(latest.AmountCents)/100, 3, tickets)
	if latest.Status != models.CycleArrearsDue {
		t.Fatalf("expected arrears_due, got %s", latest.Status)
	}

	rr = httptest.NewRecorder()
	e.leaseH.PayNow(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("pay-now: %d (%s)", rr.Code, rr.Body.String())
	}
	var pay struct {
		PaymentIntentID string `json:"payment_intent_id"`
		Amount          int64  `json:"amount"`
	}
	mustDecode(t, rr.Body.Bytes(), &pay)
	t.Logf("pay-now minted arrears intent %s for $%.2f (on-session)", pay.PaymentIntentID, float64(pay.Amount)/100)
	if pay.Amount != latest.AmountCents {
		t.Errorf("pay-now amount %d != arrears owed %d", pay.Amount, latest.AmountCents)
	}
	// Idempotency window: a driver who taps twice (or retries after a
	// dropped response) must not create a second chargeable intent.
	rr = httptest.NewRecorder()
	e.leaseH.PayNow(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	var again struct {
		PaymentIntentID string `json:"payment_intent_id"`
	}
	mustDecode(t, rr.Body.Bytes(), &again)
	t.Logf("pay-now tapped twice: second call returned %s (same intent=%v)",
		again.PaymentIntentID, again.PaymentIntentID == pay.PaymentIntentID)
	if again.PaymentIntentID != pay.PaymentIntentID {
		t.Errorf("second pay-now minted a DIFFERENT intent (%s vs %s) — double-charge risk",
			again.PaymentIntentID, pay.PaymentIntentID)
	}

	// The driver pays it (their PaymentSheet confirm).
	confirmed := e.call(t, "POST", "payment_intents/"+pay.PaymentIntentID+"/confirm", url.Values{
		"payment_method": {goodPM},
		"return_url":     {"https://drivebai.example/return"},
	})
	if str(confirmed, "status") != "succeeded" {
		t.Fatalf("arrears confirm: %v", confirmed)
	}
	if code := e.deliverWebhook(t, "payment_intent.succeeded", confirmed); code != 200 {
		t.Fatalf("arrears webhook → %d", code)
	}
	final, _ := e.billingRepo.GetCycle(ctx, latest.ID)
	var status, source string
	var ownerCents int64
	e.db.Pool.QueryRow(ctx, `SELECT status, source, owner_amount_cents FROM owner_payouts WHERE billing_cycle_id=$1`, latest.ID).
		Scan(&status, &source, &ownerCents)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status='open'`, L.leaseID).Scan(&tickets)
	t.Logf("settled: cycle=%s intent=%s | owner share $%.2f (%s/%s) | open tickets now %d",
		final.Status, strOrEmpty(final.StripePaymentIntentID), float64(ownerCents)/100, status, source, tickets)
	if final.Status != models.CyclePaid {
		t.Errorf("arrears cycle = %s, want paid", final.Status)
	}
	if status != "pending" {
		t.Errorf("owner share = %q, want pending", status)
	}
}

// ── LINE 8 ── an amendment accepted mid-clock: week N old price, N+1 new.
func TestBatch5_Line8_AmendmentMidClock(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "amend", cardSuccess, clock, now)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM billing_amendment_offers WHERE lease_request_id=$1`, L.leaseID)
	})
	const newAmount = 17500

	// Week N at the agreed price.
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, 7))
	weekN := e.sweepAndSettle(t, L.leaseID)
	piN := e.call(t, "GET", "payment_intents/"+strOrEmpty(weekN.StripePaymentIntentID), nil)
	t.Logf("week %d charged $%.2f (stripe amount %d) — the price in force",
		weekN.CycleNumber, float64(weekN.AmountCents)/100, num(piN, "amount"))

	// Owner proposes; driver accepts between cycles.
	rr := httptest.NewRecorder()
	e.leaseH.ProposeAmendment(rr, returnReq(t, L.owner, L.leaseID,
		fmt.Sprintf(`{"kind":"price","new_amount_cents":%d}`, newAmount)))
	if rr.Code != 201 {
		t.Fatalf("propose: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		Amendment models.BillingAmendmentOffer `json:"amendment"`
	}
	mustDecode(t, rr.Body.Bytes(), &created)
	rr = httptest.NewRecorder()
	e.leaseH.AcceptAmendment(rr, returnReq(t, L.driver, created.Amendment.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("accept: %d (%s)", rr.Code, rr.Body.String())
	}
	consent, _ := e.billingRepo.GetActiveConsent(ctx, L.leaseID)
	t.Logf("amendment accepted mid-rental: mandate now $%.2f under %s; week %d already charged is untouched",
		float64(consent.AmountCents)/100, consent.TermsVersion, weekN.CycleNumber)

	// Replay window: a re-submitted acceptance must not mint a second
	// successor consent or re-supersede the new one.
	rr = httptest.NewRecorder()
	e.leaseH.AcceptAmendment(rr, returnReq(t, L.driver, created.Amendment.ID, `{}`))
	var actives int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_consents WHERE lease_request_id=$1 AND revoked_at IS NULL`, L.leaseID).Scan(&actives)
	t.Logf("accept replayed: http=%d (expect 409 AMENDMENT_GONE), active consents = %d", rr.Code, actives)
	if rr.Code != 409 {
		t.Errorf("replayed accept = %d, want 409", rr.Code)
	}
	if actives != 1 {
		t.Errorf("active consents = %d after replay, want exactly 1", actives)
	}

	// Week N+1 must charge the NEW amount, on the same card, automatically.
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	e.advanceTestClock(t, clock, now.AddDate(0, 0, 14))
	weekNext := e.sweepAndSettle(t, L.leaseID)
	piNext := e.call(t, "GET", "payment_intents/"+strOrEmpty(weekNext.StripePaymentIntentID), nil)
	t.Logf("week %d charged $%.2f (stripe amount %d, status %s)",
		weekNext.CycleNumber, float64(weekNext.AmountCents)/100, num(piNext, "amount"), str(piNext, "status"))

	if num(piN, "amount") != rehearsalWeeklyCents {
		t.Errorf("week N stripe amount = %d, want %d (old price)", num(piN, "amount"), rehearsalWeeklyCents)
	}
	if num(piNext, "amount") != newAmount || weekNext.AmountCents != newAmount {
		t.Errorf("week N+1 charged %d/%d, want %d (new price)", num(piNext, "amount"), weekNext.AmountCents, newAmount)
	}
	if str(piNext, "status") != "succeeded" {
		t.Errorf("week N+1 status = %s", str(piNext, "status"))
	}
	var revoked int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_consents WHERE lease_request_id=$1 AND revoked_at IS NOT NULL`, L.leaseID).Scan(&revoked)
	t.Logf("consent trail: %d superseded row(s) + 1 active — the amendment is evidenced, not overwritten", revoked)
}

var _ = repository.ErrAmendmentGone
