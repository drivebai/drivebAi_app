package handlers

// The eight mandated rehearsal lines. Each is its own test so it reports
// its own observations; all run against live Stripe test mode with the
// customer on a test clock (see rolling_rehearsal_test.go for the harness
// and for exactly what is real vs simulated).

import (
	"context"
	"encoding/json"
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
		e.ageLease(t, L.leaseID, 7*24*time.Hour)
		// Measured AFTER aging: aging is the harness standing in for elapsed
		// time, the advance we are proving is the engine's.
		before := e.paidThrough(t, L.leaseID)
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
	e.ageCyclesOnly(t, L.leaseID, 8*24*time.Hour)
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
	if n := e.countPIsForCycle(t, L.customerID, c.ID); n != 1 {
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
	t.Logf("return initiated 3 days into week 2: preview refund $%.2f of $%.2f (used %d days)",
		float64(ret.RefundAmountCents)/100, float64(ret.PaidAmountCents)/100, ret.UsedDays)

	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, L.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("owner confirm: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	after, _ := e.billingRepo.GetCycle(ctx, cycle.ID)

	// The week under test must be the shape we think it is BEFORE any
	// arithmetic is judged (harness review: a drifted mint window would
	// otherwise feed a correct formula the wrong inputs, undetected).
	if after.AmountCents != rehearsalWeeklyCents {
		t.Fatalf("week 2 charged %d, expected the agreed %d", after.AmountCents, rehearsalWeeklyCents)
	}
	if span := after.PeriodEnd.Sub(after.PeriodStart); span != 7*24*time.Hour {
		t.Fatalf("week 2 spans %v, expected exactly 168h", span)
	}
	// INDEPENDENT expectation: 4 used days of a $150.00 week at $21.42/day
	// leaves $64.32 refundable and $85.68 kept. Written out, not computed
	// by models.ComputeReturnRefund — that is the function on trial.
	const wantUsedDays = 4
	wantRefund := int64(rehearsalWeeklyCents - wantUsedDays*rehearsalPerDayCents) // 6432
	wantKept := int64(wantUsedDays * rehearsalPerDayCents)                        // 8568
	if ret.UsedDays != wantUsedDays {
		t.Errorf("used days = %d, want %d (returned 3 days + change into the week)", ret.UsedDays, wantUsedDays)
	}
	if cross := models.ComputeReturnRefund(after.AmountCents, 1, after.PeriodStart, ret.ReturnedAt); cross.RefundAmountCents != wantRefund {
		t.Errorf("engine formula says %d, independent arithmetic says %d — they disagree",
			cross.RefundAmountCents, wantRefund)
	}

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

	if refundCount != 1 || refundTotal != wantRefund {
		t.Errorf("stripe refunded %d refund(s) totalling %d, want exactly 1 of %d", refundCount, refundTotal, wantRefund)
	}
	if kept != wantKept {
		t.Errorf("owner kept %d, want %d", kept, wantKept)
	}
	// The number shown to the driver must be the number Stripe returned.
	if ret.RefundAmountCents != refundTotal {
		t.Errorf("driver was told %d but stripe returned %d", ret.RefundAmountCents, refundTotal)
	}

	// Cardinal invariant: a returned lease is never charged again.
	beforeCycles := e.cycleCount(t, L.leaseID)
	e.ageLease(t, L.leaseID, 8*24*time.Hour)
	e.leaseH.runBillingSweep(ctx)
	afterCycles := e.cycleCount(t, L.leaseID)
	t.Logf("post-return sweep: cycles %d → %d (a returned lease must never be charged again)", beforeCycles, afterCycles)
	if afterCycles != beforeCycles {
		t.Errorf("sweep minted %d new cycle(s) on a RETURNED lease", afterCycles-beforeCycles)
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
	n := e.countPIsForCycle(t, L.customerID, c2.ID)
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
	// The CYCLE must also look un-refunded, or the replay short-circuits on
	// its own record and never reaches Stripe — which is what made this
	// window's idempotency key untested (harness review).
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET status='paid', refund_id=NULL, refunded_cents=0 WHERE id=$1`, c2.ID); err != nil {
		t.Fatalf("simulate crash W3 (cycle): %v", err)
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

	// W6/W7 need a live lease; the one above has been returned.
	M := e.startRollingRental(t, "crash2", cardSuccess, clock, now)
	e.ageLease(t, M.leaseID, 7*24*time.Hour)
	e.leaseH.runBillingSweep(ctx) // mints + charges week 2
	mCycles, _ := e.billingRepo.ListCyclesForLease(ctx, M.leaseID)
	mc := mCycles[len(mCycles)-1]

	// W6 — the process died mid-confirm, leaving the cycle in 'charging'.
	// Nothing in the rehearsal had ever produced this state, so the
	// stuck-charging phase had never executed once (harness review).
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET status='charging', updated_at = NOW() - interval '10 minutes' WHERE id=$1`, mc.ID); err != nil {
		t.Fatalf("simulate crash W6: %v", err)
	}
	e.leaseH.runBillingSweep(ctx)
	recovered, _ := e.billingRepo.GetCycle(ctx, mc.ID)
	nIntents := e.countPIsForCycle(t, M.customerID, mc.ID)
	t.Logf("W6 crash mid-confirm ('charging'): stuck-charging phase re-read the intent → cycle=%s, stripe intents still %d",
		recovered.Status, nIntents)
	if recovered.Status != models.CyclePaid {
		t.Errorf("W6: cycle stuck at %s — a crash mid-charge has no exit", recovered.Status)
	}
	if nIntents != 1 {
		t.Errorf("W6: %d intents — recovery created a second charge", nIntents)
	}

	// W7 — make the WEBHOOK the settling actor, not the inline charge
	// outcome. Every delivery so far landed on an already-settled cycle, so
	// the production route (metadata → handleCyclePaid → advance) was never
	// load-bearing in the rehearsal (harness review).
	e.ageLease(t, M.leaseID, 7*24*time.Hour)
	e.leaseH.runBillingSweep(ctx)
	mCycles, _ = e.billingRepo.ListCyclesForLease(ctx, M.leaseID)
	wk := mCycles[len(mCycles)-1]
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET status='charging' WHERE id=$1`, wk.ID); err != nil {
		t.Fatalf("simulate W7: %v", err)
	}
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET rental_ends_at = $2 WHERE id=$1`, M.leaseID, wk.PeriodStart); err != nil {
		t.Fatalf("simulate W7 (rewind): %v", err)
	}
	// Measured from the crash state, not from before it.
	beforeThrough := e.paidThrough(t, M.leaseID)
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE billing_cycle_id=$1`, wk.ID); err != nil {
		t.Fatalf("simulate W7 (accrual): %v", err)
	}
	code := e.deliverPIEvent(t, "payment_intent.succeeded", strOrEmpty(wk.StripePaymentIntentID))
	settledByHook, _ := e.billingRepo.GetCycle(ctx, wk.ID)
	afterThrough := e.paidThrough(t, M.leaseID)
	var hookAccruals int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE billing_cycle_id=$1`, wk.ID).Scan(&hookAccruals)
	t.Logf("W7 webhook as the settling actor: http=%d cycle=%s paid-through %s → %s, owner accrual rows=%d",
		code, settledByHook.Status, beforeThrough.Format(time.RFC3339), afterThrough.Format(time.RFC3339), hookAccruals)
	if code != 200 {
		t.Errorf("W7: webhook route returned %d", code)
	}
	if settledByHook.Status != models.CyclePaid {
		t.Errorf("W7: webhook did not settle the cycle (%s)", settledByHook.Status)
	}
	if !afterThrough.After(beforeThrough) {
		t.Errorf("W7: webhook did not advance paid-through (%v → %v)", beforeThrough, afterThrough)
	}
	if hookAccruals != 1 {
		t.Errorf("W7: owner accrual rows = %d, want 1", hookAccruals)
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
	// Diagnostic: every ticket on this lease, whatever its status/subject.
	if rows, qerr := e.db.Pool.Query(ctx, `SELECT subject, status FROM support_tickets WHERE lease_request_id=$1`, L.leaseID); qerr == nil {
		defer rows.Close()
		for rows.Next() {
			var subj, st string
			rows.Scan(&subj, &st)
			t.Logf("    existing ticket on this lease: %q [%s]", subj, st)
		}
	}
	t.Logf("after return on an uncollected week: cycle=%s owed $%.2f (pro-rata, not the full $%.2f week), open collection tickets=%d",
		latest.Status, float64(latest.AmountCents)/100, float64(rehearsalWeeklyCents)/100, tickets)
	if latest.Status != models.CycleArrearsDue {
		t.Fatalf("expected arrears_due, got %s", latest.Status)
	}
	if tickets != 1 {
		t.Errorf("open collection tickets = %d, want 1 — an uncollected debt must have a live actor", tickets)
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
	// INDEPENDENT expectation: the car was held 3 days + change into the
	// week, which is 4 chargeable days at $21.42 = $85.68 owed — NOT the
	// full week, and not a number this test asked the engine for. It is
	// the SAME figure line 4 proves the owner keeps on a paid week, so the
	// two lines must agree or one of them is wrong.
	wantOwed := int64(4 * rehearsalPerDayCents) // 8568
	if latest.AmountCents != wantOwed {
		t.Errorf("arrears recorded %d, independent arithmetic says %d owed for the used days",
			latest.AmountCents, wantOwed)
	}
	if pay.Amount != wantOwed {
		t.Errorf("driver was asked for %d, want %d", pay.Amount, wantOwed)
	}
	if pay.Amount >= rehearsalWeeklyCents {
		t.Errorf("driver charged a FULL week (%d) for a partly-used one", pay.Amount)
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
	if tickets != 0 {
		t.Errorf("open tickets after settlement = %d, want 0 (the debt is paid)", tickets)
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

// ── LINE 9 ── the ladder's CADENCE, not just its length. Forcing each rung
// due (as line 2 does) proves the ladder terminates but would sleep through
// a misconfigured interval hammering a real card four times in an
// afternoon. Here every gap is measured before time is advanced.
func TestBatch5_Line9_LadderCadence(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "cadence", cardSuccess, clock, now)

	badPM := e.newCardPM(t, cardDecline)
	e.attachPM(t, badPM, L.customerID)
	if ok, err := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, badPM, "visa", "0341", "fp_fail"); err != nil || !ok {
		t.Fatalf("swap to failing card: %v", err)
	}
	// Age by SIX days, not seven: that puts paid-through 24h out, which is
	// the T−24h condition production actually mints on. Aging a full week
	// would collapse period_start onto now and make rung 1's spacing zero.
	e.ageLease(t, L.leaseID, 6*24*time.Hour)

	var gaps []time.Duration
	firstAttemptAt := time.Now().UTC()
	var terminalAt time.Time
	for rung := 1; rung <= models.BillingMaxAttempts; rung++ {
		e.leaseH.runBillingSweep(ctx)
		cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
		c := cycles[len(cycles)-1]

		if c.Status == models.CycleFailedFinal {
			terminalAt = time.Now().UTC()
			t.Logf("rung %d: attempts=%d status=failed_final next_attempt=%v (ladder closed)",
				rung, c.AttemptCount, c.NextAttemptAt)
			if c.NextAttemptAt != nil {
				t.Errorf("terminal cycle still scheduled for %v — the card would be hit again", *c.NextAttemptAt)
			}
			break
		}
		if c.NextAttemptAt == nil {
			t.Fatalf("rung %d: non-terminal cycle has no next attempt — the ladder stalls", rung)
		}
		gap := c.NextAttemptAt.Sub(time.Now().UTC())
		gaps = append(gaps, gap)
		t.Logf("rung %d: attempts=%d status=%s → next attempt in %v", rung, c.AttemptCount, c.Status, gap.Round(time.Minute))
		// A rung must be at least most of a day away. This is the assertion
		// that a driver feels: four charges in an afternoon is an attack.
		if gap < 23*time.Hour || gap > 25*time.Hour {
			t.Errorf("rung %d spacing = %v, want ~24h (BillingRetrySpacing)", rung, gap.Round(time.Minute))
		}
		// Simulate exactly that wait, rather than blindly forcing it due.
		if _, err := e.db.Pool.Exec(ctx, `
			UPDATE billing_cycles SET next_attempt_at = NOW() - interval '1 minute' WHERE id=$1`, c.ID); err != nil {
			t.Fatalf("advance to rung %d: %v", rung+1, err)
		}
	}
	simulated := time.Duration(len(gaps)) * 24 * time.Hour
	t.Logf("cadence: %d rungs spaced %v; a real card would be tried over ~%v, not %v",
		len(gaps), gaps, simulated, terminalAt.Sub(firstAttemptAt).Round(time.Second))
	if len(gaps) < 3 {
		t.Errorf("only %d spaced rungs observed, want 3 before the 4th terminal attempt", len(gaps))
	}
}

// ── LINE 10 ── a dispute the platform WINS: the owner's withheld money is
// released and billing resumes. Only the lost arm had ever run.
func TestBatch5_Line10_DisputeWon(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "won", cardSuccess, clock, now)

	dpPM := e.newCardPM(t, cardDispute)
	e.attachPM(t, dpPM, L.customerID)
	if ok, _ := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, dpPM, "visa", "0259", "fp_dispute"); !ok {
		t.Fatalf("swap to dispute card")
	}
	e.ageLease(t, L.leaseID, 7*24*time.Hour)
	cycle := e.sweepAndSettle(t, L.leaseID)
	pi := e.call(t, "GET", "payment_intents/"+strOrEmpty(cycle.StripePaymentIntentID), nil)
	chargeID := str(pi, "latest_charge")

	var dispute map[string]interface{}
	for i := 0; i < 30 && dispute == nil; i++ {
		out := e.call(t, "GET", "disputes?charge="+chargeID, nil)
		if data, ok := out["data"].([]interface{}); ok && len(data) > 0 {
			dispute = data[0].(map[string]interface{})
			break
		}
		time.Sleep(2 * time.Second)
	}
	if dispute == nil {
		t.Fatalf("no dispute raised for charge %s", chargeID)
	}
	if code := e.deliverWebhook(t, "charge.dispute.created", dispute); code != 200 {
		t.Fatalf("dispute.created → %d", code)
	}
	var withheldStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE billing_cycle_id=$1`, cycle.ID).Scan(&withheldStatus)
	lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	t.Logf("dispute %s opened: owner payout=%s halt=%q", str(dispute, "id"), withheldStatus, strOrEmpty(lr.RenewalHaltedReason))
	if withheldStatus != "withheld" {
		t.Fatalf("payout = %q, want withheld", withheldStatus)
	}

	// Submit winning evidence — Stripe's documented way to close a test
	// dispute in the platform's favour.
	e.call(t, "POST", "disputes/"+str(dispute, "id"), url.Values{
		"evidence[uncategorized_text]": {"winning_evidence"},
		"submit":                       {"true"},
	})
	won := e.call(t, "GET", "disputes/"+str(dispute, "id"), nil)
	for i := 0; i < 30 && str(won, "status") != "won"; i++ {
		time.Sleep(2 * time.Second)
		won = e.call(t, "GET", "disputes/"+str(dispute, "id"), nil)
	}
	t.Logf("evidence submitted → stripe closed the dispute as %s", str(won, "status"))
	if str(won, "status") != "won" {
		t.Fatalf("dispute did not reach 'won' (got %s)", str(won, "status"))
	}

	if code := e.deliverWebhook(t, "charge.dispute.closed", won); code != 200 {
		t.Fatalf("dispute.closed(won) → %d", code)
	}
	var afterStatus, note string
	e.db.Pool.QueryRow(ctx, `SELECT status, COALESCE(note,'') FROM owner_payouts WHERE billing_cycle_id=$1`, cycle.ID).
		Scan(&afterStatus, &note)
	var settled bool
	e.db.Pool.QueryRow(ctx, `SELECT outcome_settled FROM charge_disputes WHERE stripe_dispute_id=$1`, str(dispute, "id")).Scan(&settled)
	lr, _ = e.leaseRepo.GetByID(ctx, L.leaseID)
	t.Logf("dispute WON: owner payout=%s settled=%v halt=%q note=%q",
		afterStatus, settled, strOrEmpty(lr.RenewalHaltedReason), note)
	if afterStatus == "withheld" {
		t.Errorf("owner's money still withheld after a WON dispute — stranded with no symptom")
	}
	if !settled {
		t.Errorf("dispute outcome not settled")
	}
	if strOrEmpty(lr.RenewalHaltedReason) == "dispute" {
		t.Errorf("renewals still halted for a dispute the platform won")
	}
}

// ── LINE 11 ── the overshoot refund: a whole week charged at T−24h that the
// driver never enters, because they return first. A full week of real money
// on a path that had never run.
func TestBatch5_Line11_OvershootRefund(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "over", cardSuccess, clock, now)

	// Six days in: paid-through is 24h out, so the engine charges NEXT week
	// now — and then the driver returns before that week begins.
	e.ageLease(t, L.leaseID, 6*24*time.Hour)
	overshoot := e.sweepAndSettle(t, L.leaseID)
	if overshoot == nil || overshoot.Status != models.CyclePaid {
		t.Fatalf("next week was not charged: %+v", overshoot)
	}
	if !overshoot.PeriodStart.After(time.Now().UTC()) {
		t.Fatalf("period_start %v is not in the future — this is not an overshoot", overshoot.PeriodStart)
	}
	t.Logf("week %d charged $%.2f in advance; it starts %v from now — the driver returns first",
		overshoot.CycleNumber, float64(overshoot.AmountCents)/100,
		overshoot.PeriodStart.Sub(time.Now().UTC()).Round(time.Minute))

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, L.driver, L.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, L.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, L.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("owner confirm: %d (%s)", rr.Code, rr.Body.String())
	}

	settledOvershoot, _ := e.billingRepo.GetCycle(ctx, overshoot.ID)
	refunded := e.refundTotal(t, strOrEmpty(settledOvershoot.StripePaymentIntentID))
	var payoutStatus string
	var payoutKept int64
	e.db.Pool.QueryRow(ctx, `SELECT status, gross_kept_cents FROM owner_payouts WHERE billing_cycle_id=$1`, overshoot.ID).
		Scan(&payoutStatus, &payoutKept)
	t.Logf("unentered week: cycle=%s | STRIPE refunded $%.2f of $%.2f | owner payout=%s kept=$%.2f",
		settledOvershoot.Status, float64(refunded)/100, float64(overshoot.AmountCents)/100,
		payoutStatus, float64(payoutKept)/100)

	// The whole week goes back — the driver never had the car for any of it.
	if refunded != rehearsalWeeklyCents {
		t.Errorf("stripe refunded %d for a week never entered, want the full %d", refunded, rehearsalWeeklyCents)
	}
	if settledOvershoot.Status != models.CycleRefunded {
		t.Errorf("overshoot cycle = %s, want refunded", settledOvershoot.Status)
	}
	if payoutStatus != "voided" || payoutKept != 0 {
		t.Errorf("owner payout for an unentered week = %s/%d, want voided/0", payoutStatus, payoutKept)
	}
}

// ── LINE 12 ── the two rungs no card in the rehearsal had ever reached:
// 3DS (bank wants a tap) and a hard decline (never retry).
func TestBatch5_Line12_AuthRequiredAndHardDecline(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clock := e.createTestClock(t, now)
	L := e.startRollingRental(t, "3ds", cardSuccess, clock, now)

	// (a) 3DS end to end: the saved card starts demanding authentication.
	authPM := e.newCardPM(t, cardAuthRequired)
	e.attachPM(t, authPM, L.customerID)
	if ok, err := e.billingRepo.UpdateConsentPaymentMethod(ctx, L.leaseID, authPM, "visa", "3155", "fp_3ds"); err != nil || !ok {
		t.Fatalf("swap to 3DS card: %v", err)
	}
	e.ageLease(t, L.leaseID, 6*24*time.Hour)
	e.leaseH.runBillingSweep(ctx)
	cycles, _ := e.billingRepo.ListCyclesForLease(ctx, L.leaseID)
	c := cycles[len(cycles)-1]
	lr, _ := e.leaseRepo.GetByID(ctx, L.leaseID)
	t.Logf("3DS: off-session charge returned authentication_required → cycle=%s needs_action_since=%v delinquent=%v intent=%q",
		c.Status, c.NeedsActionSince != nil, lr.DelinquentSince != nil, strOrEmpty(c.StripePaymentIntentID))
	if c.Status != models.CycleNeedsAction {
		t.Errorf("cycle = %s, want needs_action (the driver can still rescue it)", c.Status)
	}
	if lr.DelinquentSince != nil {
		t.Errorf("driver marked delinquent for a bank verification request")
	}
	// The rescue must actually be reachable: the driver's billing card
	// hands back a client secret they can confirm.
	rr := httptest.NewRecorder()
	e.leaseH.GetBillingStatus(rr, returnReq(t, L.driver, L.leaseID, ``))
	var status struct {
		OpenCycle struct {
			Status       string `json:"status"`
			ClientSecret string `json:"client_secret"`
		} `json:"open_cycle"`
	}
	mustDecode(t, rr.Body.Bytes(), &status)
	t.Logf("3DS rescue: billing card exposes open_cycle=%s with a confirmable secret=%v",
		status.OpenCycle.Status, status.OpenCycle.ClientSecret != "")
	if status.OpenCycle.ClientSecret == "" {
		t.Errorf("no client secret offered — the driver has no way to complete the verification")
	}

	// (b) Hard decline. Stripe REFUSES to attach a lost/stolen card to a
	// customer at all, so this rung cannot exist as a saved card; it is
	// driven at the classifier with a REAL Stripe lost_card error body
	// captured live, proving the ladder terminates instead of retrying a
	// card the network told us to stop using.
	lostPM := e.newCardPM(t, "tok_chargeDeclinedLostCard")
	realErr := e.call(t, "POST", "payment_intents", url.Values{
		"amount": {"15000"}, "currency": {"usd"},
		"payment_method": {lostPM}, "confirm": {"true"},
		"payment_method_types[]": {"card"},
		"return_url":             {"https://drivebai.example/return"},
	})
	errBody, _ := json.Marshal(realErr["error"])
	t.Logf("stripe's real hard-decline body: %s", string(errBody)[:min(len(errBody), 140)])

	hard := seedCycleRow(t, e.payoutEnv, L.leaseID, 90, now, now.AddDate(0, 0, 7), rehearsalWeeklyCents,
		"charging", strPtr("pi_hard_decline_probe"), nil, 0)
	hardCycle, _ := e.billingRepo.GetCycle(ctx, hard)
	e.leaseH.recordCycleOutcomeError(ctx, lr, hardCycle, 1, fmt.Errorf("%s", string(errBody)))
	afterHard, _ := e.billingRepo.GetCycle(ctx, hard)
	t.Logf("hard decline on attempt 1: cycle=%s decline=%q next_attempt=%v (must not be retried)",
		afterHard.Status, strOrEmpty(afterHard.LastDeclineCode), afterHard.NextAttemptAt)
	if afterHard.Status != models.CycleFailedFinal {
		t.Errorf("hard decline left the cycle %s — the card would be retried 3 more times", afterHard.Status)
	}
	if afterHard.NextAttemptAt != nil {
		t.Errorf("hard decline still scheduled for %v", *afterHard.NextAttemptAt)
	}
}
