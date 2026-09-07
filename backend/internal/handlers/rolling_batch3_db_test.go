package handlers

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Batch-3 tests. The client's requirement, verbatim: "the same double-run
// fingerprint proof you built in Batch 2, extended to: full-term return,
// early return, pickup no-show refund, and a disputed return resolved both
// ways — all four proven identical with the flag off and on." Plus direct
// DB-level proofs of the rolling settlement itself (Stripe nil — every
// asserted rolling path either moves no Stripe money by design or replays
// a crash window where the Stripe move already happened).

// returnPathFingerprint is the superset shape for all return paths; fields
// that don't apply to a variant stay zero-valued in BOTH runs.
type returnPathFingerprint struct {
	LeaseStatus        string
	LeaseRefundStatus  string // pickup no-show: 'unrecoverable' (nil intent)
	ReturnExists       bool
	ReturnStatus       string
	ReturnRefundState  string
	PaidCents          int64
	RefundCents        int64
	UsedDays           int
	VehicleReturnedSet bool
	HaltReason         string // MUST stay "" — halts are billing_mode-gated
	CarStatus          string
	PayoutCount        int
	PayoutKept         int64
	PayoutFee          int64
	PayoutOwner        int64
	PayoutStatus       string
	PayoutSource       string
	TicketCount        int
	CyclesCount        int // MUST be 0 — rolling tables untouched
	ConsentsCount      int
}

// runReturnPathLifecycle drives one fixed-term lease down the named return
// path with the rolling flag as given, running the billing sweep at every
// stage, and fingerprints the terminal state. Wiring mirrors prod: the
// return handler carries the batch-3 billing deps in BOTH runs — only the
// flag varies.
func runReturnPathLifecycle(t *testing.T, e *payoutEnv, billingRepo *repository.BillingRepository, rollingOn bool, variant, tag string) returnPathFingerprint {
	t.Helper()
	ctx := context.Background()
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, rollingOn)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)

	runID := uuid.New().String()[:8]
	owner := e.seedUser(t, "car_owner", "b3_owner_"+tag+"_"+runID+"@example.com")
	driver := e.seedUser(t, "driver", "b3_driver_"+tag+"_"+runID+"@example.com")
	admin := e.seedUser(t, "admin", "b3_admin_"+tag+"_"+runID+"@example.com")
	e.seedLicense(t, driver)
	car := e.seedCar(t, owner, "available", true, false)

	sweep := func() { e.leaseH.runBillingSweep(ctx) }

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
	if rr.Code != 201 {
		t.Fatalf("[%s/%s] create: %d (%s)", variant, tag, rr.Code, rr.Body.String())
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
	sweep()

	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("[%s/%s] accept: %v", variant, tag, err)
	}
	if _, err := e.leaseRepo.SetPaid(ctx, leaseID); err != nil {
		t.Fatalf("[%s/%s] paid: %v", variant, tag, err)
	}
	seedPaymentAt(t, e, leaseID, 15000, "succeeded", nil)
	sweep()

	confirmOwner := func(retID uuid.UUID) {
		rr = httptest.NewRecorder()
		e.returnH.OwnerConfirm(rr, returnReq(t, owner, retID, `{}`))
		if rr.Code != 200 {
			t.Fatalf("[%s/%s] owner confirm: %d (%s)", variant, tag, rr.Code, rr.Body.String())
		}
	}
	initiate := func() uuid.UUID {
		rr = httptest.NewRecorder()
		e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
		if rr.Code != 201 && rr.Code != 200 {
			t.Fatalf("[%s/%s] initiate: %d (%s)", variant, tag, rr.Code, rr.Body.String())
		}
		ret, err := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
		if err != nil || ret == nil {
			t.Fatalf("[%s/%s] load return: %v", variant, tag, err)
		}
		return ret.ID
	}
	pickupAndAge := func(days int) {
		if _, err := e.leaseRepo.ConfirmPickup(ctx, leaseID, driver); err != nil {
			t.Fatalf("[%s/%s] pickup: %v", variant, tag, err)
		}
		sweep()
		if _, err := e.db.Pool.Exec(ctx, `
			UPDATE lease_requests
			SET pickup_confirmed_at = pickup_confirmed_at - make_interval(days => $2),
			    rental_ends_at = rental_ends_at - make_interval(days => $2)
			WHERE id = $1`, leaseID, days); err != nil {
			t.Fatalf("[%s/%s] age: %v", variant, tag, err)
		}
		sweep()
	}

	switch variant {
	case "full_term":
		pickupAndAge(8)
		confirmOwner(initiate())
	case "early":
		pickupAndAge(2)
		confirmOwner(initiate()) // refund > 0, nil intent → unrecoverable park
	case "no_show":
		// Never picked up; deadline elapses; the expiry claim runs. The
		// refund is a full reversal — nil intent parks it unrecoverable,
		// identically in both runs.
		if _, err := e.db.Pool.Exec(ctx, `
			UPDATE lease_requests SET pickup_deadline_at = NOW() - interval '1 hour' WHERE id = $1`, leaseID); err != nil {
			t.Fatalf("[%s/%s] deadline: %v", variant, tag, err)
		}
		sweep()
		e.leaseH.processExpiredLease(ctx, leaseID)
		sweep()
	case "dispute_accept", "dispute_reject":
		pickupAndAge(2)
		retID := initiate()
		rr = httptest.NewRecorder()
		e.returnH.Dispute(rr, returnReq(t, owner, retID, `{"reason":"The car was not returned to me."}`))
		if rr.Code != 200 {
			t.Fatalf("[%s/%s] dispute: %d (%s)", variant, tag, rr.Code, rr.Body.String())
		}
		sweep()
		resolution := "accept"
		if variant == "dispute_reject" {
			resolution = "reject"
		}
		rr = httptest.NewRecorder()
		e.returnH.AdminResolve(rr, returnReq(t, admin, retID,
			`{"resolution":"`+resolution+`","note":"Batch-3 fingerprint proof resolution."}`))
		if rr.Code != 200 {
			t.Fatalf("[%s/%s] resolve: %d (%s)", variant, tag, rr.Code, rr.Body.String())
		}
	default:
		t.Fatalf("unknown variant %q", variant)
	}
	sweep()

	// ── Fingerprint the terminal state.
	var fp returnPathFingerprint
	lr, err := e.leaseRepo.GetByID(ctx, leaseID)
	if err != nil {
		t.Fatalf("[%s/%s] final lease: %v", variant, tag, err)
	}
	fp.LeaseStatus = string(lr.Status)
	if lr.RefundStatus != nil {
		fp.LeaseRefundStatus = *lr.RefundStatus
	}
	fp.VehicleReturnedSet = lr.VehicleReturnedAt != nil
	if lr.RenewalHaltedReason != nil {
		fp.HaltReason = *lr.RenewalHaltedReason
	}
	if ret, rerr := e.returnRepo.GetByLeaseRequestID(ctx, leaseID); rerr == nil && ret != nil {
		fp.ReturnExists = true
		fp.ReturnStatus = string(ret.Status)
		if ret.RefundStatus != nil {
			fp.ReturnRefundState = string(*ret.RefundStatus)
		}
		fp.PaidCents = ret.PaidAmountCents
		fp.RefundCents = ret.RefundAmountCents
		fp.UsedDays = ret.UsedDays
	}
	e.db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id=$1`, car).Scan(&fp.CarStatus)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE lease_request_id=$1`, leaseID).Scan(&fp.PayoutCount)
	if fp.PayoutCount == 1 {
		e.db.Pool.QueryRow(ctx, `
			SELECT gross_kept_cents, fee_cents, owner_amount_cents, status, source
			FROM owner_payouts WHERE lease_request_id=$1`, leaseID).
			Scan(&fp.PayoutKept, &fp.PayoutFee, &fp.PayoutOwner, &fp.PayoutStatus, &fp.PayoutSource)
	}
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1`, leaseID).Scan(&fp.TicketCount)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&fp.CyclesCount)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID).Scan(&fp.ConsentsCount)
	return fp
}

// TestBatch3_ReturnPathsByteIdentical is the client-required double-run
// proof over every return path batch 3 touched.
func TestBatch3_ReturnPathsByteIdentical(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)

	variants := []string{"full_term", "early", "no_show", "dispute_accept", "dispute_reject"}
	for _, variant := range variants {
		variant := variant
		t.Run(variant, func(t *testing.T) {
			off := runReturnPathLifecycle(t, e, billingRepo, false, variant, "off")
			on := runReturnPathLifecycle(t, e, billingRepo, true, variant, "on")
			if !reflect.DeepEqual(off, on) {
				t.Fatalf("%s DIFFERS with the rolling flag:\n off=%+v\n  on=%+v", variant, off, on)
			}
			// Absolute expectations so two identically-wrong runs can't pass.
			if off.CyclesCount != 0 || off.ConsentsCount != 0 {
				t.Errorf("rolling tables touched: cycles=%d consents=%d", off.CyclesCount, off.ConsentsCount)
			}
			if off.HaltReason != "" {
				t.Errorf("fixed-term lease got a renewal halt: %q", off.HaltReason)
			}
			switch variant {
			case "full_term":
				if off.ReturnStatus != "completed" || off.RefundCents != 0 || off.PayoutCount != 1 ||
					off.PayoutKept != 15000 || off.PayoutFee != 1500 || off.PayoutOwner != 13500 ||
					off.PayoutSource != "return_completed" || !off.VehicleReturnedSet {
					t.Errorf("full_term shape wrong: %+v", off)
				}
			case "early":
				if off.ReturnStatus != "owner_confirmed" || off.ReturnRefundState != "unrecoverable" ||
					off.RefundCents <= 0 || off.PayoutCount != 0 || off.VehicleReturnedSet {
					t.Errorf("early shape wrong: %+v", off)
				}
			case "no_show":
				if off.LeaseStatus != "expired_refunded" || off.LeaseRefundStatus != "unrecoverable" ||
					off.ReturnExists || off.PayoutCount != 0 {
					t.Errorf("no_show shape wrong: %+v", off)
				}
			case "dispute_accept":
				if off.ReturnStatus != "owner_confirmed" || off.ReturnRefundState != "unrecoverable" ||
					off.RefundCents <= 0 || off.PayoutCount != 0 {
					t.Errorf("dispute_accept shape wrong: %+v", off)
				}
			case "dispute_reject":
				if off.ReturnStatus != "cancelled" || off.LeaseStatus != "paid" ||
					off.VehicleReturnedSet || off.PayoutCount != 0 {
					t.Errorf("dispute_reject shape wrong: %+v", off)
				}
			}
		})
	}
}

// ─── Direct rolling-settlement proofs (Stripe nil) ──────────────────────────

type rollingFixture struct {
	leaseID uuid.UUID
	carID   uuid.UUID
	owner   uuid.UUID
	driver  uuid.UUID
	pickup  time.Time
}

// seedRollingLease converts a seeded active rental to rolling with pickup
// `daysAgo` in the past and paid-through at `paidThroughDays` after pickup,
// then registers cleanup for every rolling table.
func seedRollingLease(t *testing.T, e *payoutEnv, tag string, daysAgo, paidThroughDays int) rollingFixture {
	t.Helper()
	ctx := context.Background()
	runID := uuid.New().String()[:8]
	owner := e.seedUser(t, "car_owner", "b3r_owner_"+tag+"_"+runID+"@example.com")
	driver := e.seedUser(t, "driver", "b3r_driver_"+tag+"_"+runID+"@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id = $1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id = $1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id = $1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id = $1`, leaseID)
	})
	var pickup time.Time
	if err := e.db.Pool.QueryRow(ctx, `
		UPDATE lease_requests
		SET billing_mode = 'rolling', weeks = 1,
		    pickup_confirmed_at = NOW() - make_interval(days => $2),
		    rental_ends_at = NOW() - make_interval(days => $2) + make_interval(days => $3)
		WHERE id = $1
		RETURNING pickup_confirmed_at`, leaseID, daysAgo, paidThroughDays).Scan(&pickup); err != nil {
		t.Fatalf("seed rolling lease: %v", err)
	}
	return rollingFixture{leaseID: leaseID, carID: carID, owner: owner, driver: driver, pickup: pickup}
}

func seedCycleRow(t *testing.T, e *payoutEnv, leaseID uuid.UUID, n int, start, end time.Time, amount int64, status string, intentID, refundID *string, refundedCents int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO billing_cycles
			(id, lease_request_id, cycle_number, period_start, period_end, amount_cents,
			 status, stripe_payment_intent_id, refunded_cents, refund_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW(),NOW())`,
		id, leaseID, n, start, end, amount, status, intentID, refundedCents, refundID); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	return id
}

func seedAccruing(t *testing.T, e *payoutEnv, f rollingFixture, cycleID uuid.UUID, amount int64, start, end time.Time) {
	t.Helper()
	fee, ownerShare := models.ComputePayoutSplit(amount, payoutTestFeeBPS)
	if _, _, err := e.payoutRepo.CreateCycleAccruing(context.Background(), &models.OwnerPayout{
		LeaseRequestID:   f.leaseID,
		OwnerID:          f.owner,
		GrossKeptCents:   amount,
		FeeBPS:           payoutTestFeeBPS,
		FeeCents:         fee,
		OwnerAmountCents: ownerShare,
		Currency:         "USD",
		BillingCycleID:   &cycleID,
		PeriodStart:      &start,
		PeriodEnd:        &end,
	}); err != nil {
		t.Fatalf("seed accruing: %v", err)
	}
}

// The marquee no-Stripe rolling return: full week used, cycle paid and
// accrued → return completes with $0 refund, the accrual rewrites to a
// pending 'return_completed' payout with the full-week split, and NO
// legacy (cycle-less) ledger row appears.
func TestBatch3_RollingFullWeekReturn(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)

	f := seedRollingLease(t, e, "full", 8, 7) // picked up 8d ago, paid through 7d
	c1 := seedCycleRow(t, e, f.leaseID, 1, f.pickup, f.pickup.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_full_c1"), nil, 0)
	seedAccruing(t, e, f, c1, 15000, f.pickup, f.pickup.AddDate(0, 0, 7))

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	if ret.RefundAmountCents != 0 || ret.PaidAmountCents != 15000 {
		t.Fatalf("rolling preview: paid=%d refund=%d, want 15000/0", ret.PaidAmountCents, ret.RefundAmountCents)
	}
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, f.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("confirm: %d (%s)", rr.Code, rr.Body.String())
	}

	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	if ret.Status != models.VehicleReturnCompleted {
		t.Fatalf("return status = %s, want completed", ret.Status)
	}
	var kept, fee, ownerCents int64
	var pStatus, pSource string
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT gross_kept_cents, fee_cents, owner_amount_cents, status, source
		FROM owner_payouts WHERE billing_cycle_id = $1`, c1).
		Scan(&kept, &fee, &ownerCents, &pStatus, &pSource); err != nil {
		t.Fatalf("cycle payout row: %v", err)
	}
	if kept != 15000 || fee != 1500 || ownerCents != 13500 || pStatus != "pending" || pSource != "return_completed" {
		t.Errorf("final-week payout = %d/%d/%d %s/%s, want 15000/1500/13500 pending/return_completed",
			kept, fee, ownerCents, pStatus, pSource)
	}
	var legacyRows int
	e.db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM owner_payouts WHERE lease_request_id = $1 AND billing_cycle_id IS NULL`, f.leaseID).Scan(&legacyRows)
	if legacyRows != 0 {
		t.Errorf("legacy payout row created for a rolling lease (%d) — double-count", legacyRows)
	}
	lr, _ := e.leaseRepo.GetByID(ctx, f.leaseID)
	if lr.VehicleReturnedAt == nil {
		t.Errorf("vehicle_returned_at not stamped")
	}
}

// Returned mid-week during an UNPAID week: nothing refunds, nothing pays
// out for that week, and the cycle becomes arrears_due CUT TO THE USED
// DAYS (design §7 — return never blocked on debt, collection on-session
// only). The prior week's accrual survives untouched, and admin waive is
// the arrears write-off.
func TestBatch3_RollingUnpaidFinalWeekArrears(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	admin := e.seedUser(t, "admin", "b3r_admin3_"+uuid.New().String()[:8]+"@example.com")

	f := seedRollingLease(t, e, "unpaid", 10, 7) // week 2 in progress, unpaid
	c1 := seedCycleRow(t, e, f.leaseID, 1, f.pickup, f.pickup.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_up_c1"), nil, 0)
	seedAccruing(t, e, f, c1, 15000, f.pickup, f.pickup.AddDate(0, 0, 7))
	c2Start := f.pickup.AddDate(0, 0, 7)
	c2 := seedCycleRow(t, e, f.leaseID, 2, c2Start, c2Start.AddDate(0, 0, 7), 15000, "failed_final", strPtr("pi_b3_up_c2"), nil, 0)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	if ret.RefundAmountCents != 0 {
		t.Fatalf("unpaid week previewed a refund: %d", ret.RefundAmountCents)
	}
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, f.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("confirm: %d (%s)", rr.Code, rr.Body.String())
	}

	wantOwed := 15000 - models.ComputeReturnRefund(15000, 1, c2Start, ret.ReturnedAt).RefundAmountCents
	var c2Status string
	var c2Amount int64
	e.db.Pool.QueryRow(ctx, `SELECT status, amount_cents FROM billing_cycles WHERE id=$1`, c2).Scan(&c2Status, &c2Amount)
	if c2Status != "arrears_due" || c2Amount != wantOwed {
		t.Errorf("unpaid final cycle = %s/%d, want arrears_due/%d (pro-rata used days)", c2Status, c2Amount, wantOwed)
	}
	var c1Payout string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE billing_cycle_id=$1`, c1).Scan(&c1Payout)
	if c1Payout != "accruing" {
		t.Errorf("week-1 accrual disturbed: %s", c1Payout)
	}
	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	if ret.Status != models.VehicleReturnCompleted || ret.RefundAmountCents != 0 {
		t.Errorf("return = %s refund=%d, want completed/0", ret.Status, ret.RefundAmountCents)
	}

	// The arrears exit: admin waive writes it off.
	rr = httptest.NewRecorder()
	e.leaseH.AdminWaiveBillingCycle(rr, returnReq(t, admin, c2, `{"note":"write-off for the arrears test"}`))
	if rr.Code != 200 {
		t.Fatalf("waive arrears: %d (%s)", rr.Code, rr.Body.String())
	}
	e.db.Pool.QueryRow(ctx, `SELECT status FROM billing_cycles WHERE id=$1`, c2).Scan(&c2Status)
	if c2Status != "waived" {
		t.Errorf("arrears cycle after waive = %s, want waived", c2Status)
	}
}

// Crash-window replay: both cycles already carry their Stripe refunds
// (partial on the final week, full on the overshoot) — the settlement
// finishes the LEDGER only: rewrite kept on the final week, void the
// overshoot accrual, finalize the row with the trued totals.
func TestBatch3_RollingReplayFinishesLedger(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)

	// Returned 3 days into week 1 while week 2's T−24h charge had landed.
	f := seedRollingLease(t, e, "replay", 3, 11)
	c1End := f.pickup.AddDate(0, 0, 7)
	c1 := seedCycleRow(t, e, f.leaseID, 1, f.pickup, c1End, 15000, "partially_refunded", strPtr("pi_b3_rp_c1"), strPtr("re_b3_rp_c1"), 6426)
	seedAccruing(t, e, f, c1, 15000, f.pickup, c1End)
	c2 := seedCycleRow(t, e, f.leaseID, 2, c1End, c1End.AddDate(0, 0, 7), 15000, "refunded", strPtr("pi_b3_rp_c2"), strPtr("re_b3_rp_c2"), 15000)
	seedAccruing(t, e, f, c2, 15000, c1End, c1End.AddDate(0, 0, 7))

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, f.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("confirm: %d (%s)", rr.Code, rr.Body.String())
	}

	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	if ret.Status != models.VehicleReturnCompleted {
		t.Fatalf("return = %s, want completed", ret.Status)
	}
	if ret.PaidAmountCents != 30000 || ret.RefundAmountCents != 6426+15000 {
		t.Errorf("trued snapshot = %d/%d, want 30000/21426", ret.PaidAmountCents, ret.RefundAmountCents)
	}
	if ret.RefundID == nil || *ret.RefundID != "re_b3_rp_c1" {
		t.Errorf("primary refund id = %v, want the final week's re_b3_rp_c1", ret.RefundID)
	}
	kept := int64(15000 - 6426)
	wantFee, wantOwner := models.ComputePayoutSplit(kept, payoutTestFeeBPS)
	var gotKept, gotFee, gotOwner int64
	var pStatus, pSource string
	e.db.Pool.QueryRow(ctx, `
		SELECT gross_kept_cents, fee_cents, owner_amount_cents, status, source
		FROM owner_payouts WHERE billing_cycle_id=$1`, c1).Scan(&gotKept, &gotFee, &gotOwner, &pStatus, &pSource)
	if gotKept != kept || gotFee != wantFee || gotOwner != wantOwner || pStatus != "pending" || pSource != "return_completed" {
		t.Errorf("final-week rewrite = %d/%d/%d %s/%s, want %d/%d/%d pending/return_completed",
			gotKept, gotFee, gotOwner, pStatus, pSource, kept, wantFee, wantOwner)
	}
	var c2PayoutStatus string
	var c2Kept int64
	e.db.Pool.QueryRow(ctx, `SELECT status, gross_kept_cents FROM owner_payouts WHERE billing_cycle_id=$1`, c2).Scan(&c2PayoutStatus, &c2Kept)
	if c2PayoutStatus != "voided" || c2Kept != 0 {
		t.Errorf("overshoot accrual = %s/%d, want voided/0", c2PayoutStatus, c2Kept)
	}
}

// Initiate previews rolling money per cycle: two paid weeks, return early
// in week 2 — the preview prices against CYCLE 2's period, where the
// legacy weeks=1 formula (past term) would preview $0.
func TestBatch3_RollingInitiatePreviewUsesCycles(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)

	f := seedRollingLease(t, e, "preview", 9, 14) // 2 days into week 2
	c1End := f.pickup.AddDate(0, 0, 7)
	seedCycleRow(t, e, f.leaseID, 1, f.pickup, c1End, 15000, "paid", strPtr("pi_b3_pv_c1"), nil, 0)
	seedCycleRow(t, e, f.leaseID, 2, c1End, c1End.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_pv_c2"), nil, 0)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	want := models.ComputeReturnRefund(15000, 1, c1End, ret.ReturnedAt)
	if ret.RefundAmountCents != want.RefundAmountCents || ret.RefundAmountCents <= 0 {
		t.Errorf("preview refund = %d, want cycle-2 pro-rata %d (>0)", ret.RefundAmountCents, want.RefundAmountCents)
	}
	if ret.PaidAmountCents != 15000 || ret.UsedDays != want.UsedDays {
		t.Errorf("preview paid/used = %d/%d, want 15000/%d", ret.PaidAmountCents, ret.UsedDays, want.UsedDays)
	}
}

// The admin money guards: whole-rent settlement and refund overrides refuse
// rolling leases; the per-cycle waive works, is claimed-once, and lifts a
// delinquency halt.
func TestBatch3_AdminGuardsAndWaive(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)
	admin := e.seedUser(t, "admin", "b3r_admin_"+uuid.New().String()[:8]+"@example.com")

	f := seedRollingLease(t, e, "guards", 10, 7)
	e.seedPayment(t, f.leaseID, 15000)
	c1 := seedCycleRow(t, e, f.leaseID, 1, f.pickup, f.pickup.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_gd_c1"), nil, 0)
	c2 := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), f.pickup.AddDate(0, 0, 14), 15000, "failed_final", strPtr("pi_b3_gd_c2"), nil, 0)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET delinquent_since = NOW(), renewal_halted_reason = 'delinquent' WHERE id = $1`, f.leaseID); err != nil {
		t.Fatalf("mark delinquent: %v", err)
	}

	// Whole-rent settlement refuses rolling.
	rr := httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, f.leaseID,
		`{"resolution":"close","note":"should be refused for rolling"}`))
	if rr.Code != 409 {
		t.Fatalf("settle on rolling = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}

	// Refund override on a rolling disputed return refuses.
	rr = httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.Dispute(rr, returnReq(t, f.owner, ret.ID, `{"reason":"Car was not returned to me."}`))
	if rr.Code != 200 {
		t.Fatalf("dispute: %d (%s)", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, admin, ret.ID,
		`{"resolution":"accept","driver_refund_cents":5000,"note":"override must be refused"}`))
	if rr.Code != 409 {
		t.Fatalf("rolling override = %d, want 409 (%s)", rr.Code, rr.Body.String())
	}

	// Reject clears the return_initiated slot… which here is owned by
	// 'delinquent', so the claim-scoped clear must leave it alone.
	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, admin, ret.ID,
		`{"resolution":"reject","note":"testing reject on rolling"}`))
	if rr.Code != 200 {
		t.Fatalf("reject: %d (%s)", rr.Code, rr.Body.String())
	}
	var halt *string
	e.db.Pool.QueryRow(ctx, `SELECT renewal_halted_reason FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&halt)
	if halt == nil || *halt != "delinquent" {
		t.Errorf("delinquent halt disturbed by reject: %v", halt)
	}

	// Waive the unpaid week: cycle → waived, delinquency + halt lifted.
	rr = httptest.NewRecorder()
	e.leaseH.AdminWaiveBillingCycle(rr, returnReq(t, admin, c2, `{"note":"forgiven for the guard test"}`))
	if rr.Code != 200 {
		t.Fatalf("waive: %d (%s)", rr.Code, rr.Body.String())
	}
	var c2Status string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM billing_cycles WHERE id=$1`, c2).Scan(&c2Status)
	if c2Status != "waived" {
		t.Errorf("cycle = %s, want waived", c2Status)
	}
	var delinquentSince *time.Time
	e.db.Pool.QueryRow(ctx, `SELECT delinquent_since, renewal_halted_reason FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&delinquentSince, &halt)
	if delinquentSince != nil || halt != nil {
		t.Errorf("delinquency not lifted: since=%v halt=%v", delinquentSince, halt)
	}

	// Claimed-once: a second waive and a paid-cycle waive both refuse.
	rr = httptest.NewRecorder()
	e.leaseH.AdminWaiveBillingCycle(rr, returnReq(t, admin, c2, `{"note":"double waive"}`))
	if rr.Code != 409 {
		t.Errorf("second waive = %d, want 409", rr.Code)
	}
	rr = httptest.NewRecorder()
	e.leaseH.AdminWaiveBillingCycle(rr, returnReq(t, admin, c1, `{"note":"paid cycle waive"}`))
	if rr.Code != 409 {
		t.Errorf("paid-cycle waive = %d, want 409", rr.Code)
	}
}

// A rejected return on a rolling lease resumes billing: the
// return_initiated halt clears (and only that reason).
func TestBatch3_RejectClearsReturnHalt(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)
	admin := e.seedUser(t, "admin", "b3r_admin2_"+uuid.New().String()[:8]+"@example.com")

	f := seedRollingLease(t, e, "reject", 3, 7)
	seedCycleRow(t, e, f.leaseID, 1, f.pickup, f.pickup.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_rj_c1"), nil, 0)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	var halt *string
	e.db.Pool.QueryRow(ctx, `SELECT renewal_halted_reason FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&halt)
	if halt == nil || *halt != "return_initiated" {
		t.Fatalf("initiate did not halt rolling billing: %v", halt)
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.Dispute(rr, returnReq(t, f.owner, ret.ID, `{"reason":"Car was not returned to me."}`))
	if rr.Code != 200 {
		t.Fatalf("dispute: %d (%s)", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, admin, ret.ID,
		`{"resolution":"reject","note":"driver still has the car"}`))
	if rr.Code != 200 {
		t.Fatalf("reject: %d (%s)", rr.Code, rr.Body.String())
	}
	e.db.Pool.QueryRow(ctx, `SELECT renewal_halted_reason FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&halt)
	if halt != nil {
		t.Errorf("halt not cleared after reject: %v", *halt)
	}
}

// The cycle-1 bootstrap: a paid + picked-up rolling lease gets its week-1
// cycle row (paid, correct period + intent) and an accruing payout with
// the weekly split. Idempotent across sweeps.
func TestBatch3_BootstrapMintsCycleOne(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	f := seedRollingLease(t, e, "boot", 0, 7) // fresh pickup
	seedPaymentAt(t, e, f.leaseID, 15000, "succeeded", strPtr("pi_b3_boot_wk1"))

	e.leaseH.runBillingSweep(ctx)
	e.leaseH.runBillingSweep(ctx) // second pass must not duplicate

	var cycleID uuid.UUID
	var status string
	var amount int64
	var intent *string
	var start, end time.Time
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT id, status, amount_cents, stripe_payment_intent_id, period_start, period_end
		FROM billing_cycles WHERE lease_request_id=$1 AND cycle_number=1`, f.leaseID).
		Scan(&cycleID, &status, &amount, &intent, &start, &end); err != nil {
		t.Fatalf("cycle 1 not minted: %v", err)
	}
	if status != "paid" || amount != 15000 || intent == nil || *intent != "pi_b3_boot_wk1" {
		t.Errorf("cycle 1 = %s/%d/%v, want paid/15000/pi_b3_boot_wk1", status, amount, intent)
	}
	if !start.Equal(f.pickup) || !end.Equal(f.pickup.AddDate(0, 0, 7)) {
		t.Errorf("cycle 1 period = [%v,%v], want [pickup, pickup+7d]", start, end)
	}
	var cycleCount, payoutCount int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, f.leaseID).Scan(&cycleCount)
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE billing_cycle_id=$1`, cycleID).Scan(&payoutCount)
	if cycleCount != 1 || payoutCount != 1 {
		t.Errorf("duplicates after double sweep: cycles=%d payouts=%d", cycleCount, payoutCount)
	}
	var pStatus string
	var kept, fee, ownerCents int64
	e.db.Pool.QueryRow(ctx, `
		SELECT status, gross_kept_cents, fee_cents, owner_amount_cents
		FROM owner_payouts WHERE billing_cycle_id=$1`, cycleID).Scan(&pStatus, &kept, &fee, &ownerCents)
	if pStatus != "accruing" || kept != 15000 || fee != 1500 || ownerCents != 13500 {
		t.Errorf("week-1 accrual = %s %d/%d/%d, want accruing 15000/1500/13500", pStatus, kept, fee, ownerCents)
	}
}

// The post-return reconciler's lister: strictly-after-return paid cycles
// only — the boundary week and refunded/other-status cycles stay out.
func TestBatch3_PostReturnListerSelection(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)

	f := seedRollingLease(t, e, "recon", 8, 7)
	returnedAt := f.pickup.AddDate(0, 0, 7)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET vehicle_returned_at = $2 WHERE id = $1`, f.leaseID, returnedAt); err != nil {
		t.Fatalf("stamp return: %v", err)
	}
	seedCycleRow(t, e, f.leaseID, 1, f.pickup, returnedAt, 15000, "paid", strPtr("pi_b3_rc_c1"), nil, 0)                                                        // before return
	boundary := seedCycleRow(t, e, f.leaseID, 2, returnedAt, returnedAt.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_rc_c2"), nil, 0)                         // starts AT return
	after := seedCycleRow(t, e, f.leaseID, 3, returnedAt.Add(time.Hour), returnedAt.Add(time.Hour).AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_rc_c3"), nil, 0) // strictly after
	seedCycleRow(t, e, f.leaseID, 4, returnedAt.AddDate(0, 0, 8), returnedAt.AddDate(0, 0, 15), 15000, "refunded", strPtr("pi_b3_rc_c4"), strPtr("re_b3_rc_c4"), 15000)

	got, err := billingRepo.ListPaidCyclesAfterReturn(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var sawAfter, sawWrong bool
	for _, c := range got {
		if c.LeaseRequestID != f.leaseID {
			continue // other tests' rows
		}
		switch c.ID {
		case after:
			sawAfter = true
		case boundary:
			sawWrong = true
			t.Errorf("boundary cycle (period_start == returned_at) listed — must stay with pro-rata settlement")
		default:
			sawWrong = true
			t.Errorf("unexpected cycle listed: %v (%s)", c.ID, c.Status)
		}
	}
	if !sawAfter {
		t.Errorf("strictly-after paid cycle not listed")
	}
	_ = sawWrong
}

// ─── Batch-3 adversarial-review fixes ───────────────────────────────────────

// Review CRITICAL (backstop half): a paid-signal for a cycle already
// written off must never be ACKed away. With the refund already recorded
// (replay), the webhook ACKs; RecordLateChargeRefund is claimed-once and
// never disturbs the written-off status.
func TestBatch3_ReviewFix_LateChargeOnSettledCycle(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	f := seedRollingLease(t, e, "late", 10, 7)
	// arrears_due cycle whose late refund is already recorded (replay leg).
	cA := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), f.pickup.AddDate(0, 0, 14), 6426, "arrears_due", strPtr("pi_b3_lt_a"), strPtr("re_b3_lt_a"), 15000)
	if !e.leaseH.handleCyclePaid(ctx, cA, "pi_b3_lt_a") {
		t.Errorf("replayed late-charge signal on refunded arrears cycle should ACK")
	}
	var st string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM billing_cycles WHERE id=$1`, cA).Scan(&st)
	if st != "arrears_due" {
		t.Errorf("arrears status disturbed: %s", st)
	}

	// A refunded cycle's redelivery ACKs benignly.
	cB := seedCycleRow(t, e, f.leaseID, 3, f.pickup.AddDate(0, 0, 14), f.pickup.AddDate(0, 0, 21), 15000, "refunded", strPtr("pi_b3_lt_b"), strPtr("re_b3_lt_b"), 15000)
	if !e.leaseH.handleCyclePaid(ctx, cB, "pi_b3_lt_b") {
		t.Errorf("redelivery on refunded cycle should ACK")
	}

	// RecordLateChargeRefund: claimed-once, both written-off statuses.
	cC := seedCycleRow(t, e, f.leaseID, 4, f.pickup.AddDate(0, 0, 21), f.pickup.AddDate(0, 0, 28), 15000, "waived", strPtr("pi_b3_lt_c"), nil, 0)
	if ok, err := billingRepo.RecordLateChargeRefund(ctx, cC, "re_b3_lt_c", 15000); err != nil || !ok {
		t.Fatalf("record late refund: ok=%v err=%v", ok, err)
	}
	if ok, _ := billingRepo.RecordLateChargeRefund(ctx, cC, "re_other", 15000); ok {
		t.Errorf("late refund record must be claimed-once")
	}
	var st2 string
	var refID *string
	e.db.Pool.QueryRow(ctx, `SELECT status, refund_id FROM billing_cycles WHERE id=$1`, cC).Scan(&st2, &refID)
	if st2 != "waived" || refID == nil || *refID != "re_b3_lt_c" {
		t.Errorf("waived cycle after late refund = %s/%v, want waived/re_b3_lt_c", st2, refID)
	}
}

// Review HIGH: a crash between the cycle-1 mint and its accrual must not
// lose the owner's week-1 share — the lister keeps listing until the
// payout row exists and the phase's fall-through finishes it.
func TestBatch3_ReviewFix_BootstrapAccrualCrashHeals(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	f := seedRollingLease(t, e, "heal", 0, 7)
	seedPaymentAt(t, e, f.leaseID, 15000, "succeeded", strPtr("pi_b3_heal_wk1"))
	e.leaseH.runBillingSweep(ctx)

	var cycleID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT id FROM billing_cycles WHERE lease_request_id=$1 AND cycle_number=1`, f.leaseID).Scan(&cycleID); err != nil {
		t.Fatalf("cycle 1 missing after sweep: %v", err)
	}
	// Simulate the crash window: the accrual never landed.
	if _, err := e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE billing_cycle_id=$1`, cycleID); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}
	e.leaseH.runBillingSweep(ctx)
	var n int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM owner_payouts WHERE billing_cycle_id=$1`, cycleID).Scan(&n)
	if n != 1 {
		t.Fatalf("accrual not healed by the next sweep: %d rows", n)
	}
}

// Review HIGH: the admin waive endpoint refuses in-flight cycles whose
// charge can still land.
func TestBatch3_ReviewFix_WaiveRefusesInFlight(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	admin := e.seedUser(t, "admin", "b3r_admin4_"+uuid.New().String()[:8]+"@example.com")

	f := seedRollingLease(t, e, "inflight", 8, 7)
	live := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), f.pickup.AddDate(0, 0, 14), 15000, "needs_action", strPtr("pi_b3_if_c2"), nil, 0)
	rr := httptest.NewRecorder()
	e.leaseH.AdminWaiveBillingCycle(rr, returnReq(t, admin, live, `{"note":"must be refused while in flight"}`))
	if rr.Code != 409 {
		t.Errorf("waive of needs_action cycle = %d, want 409 CYCLE_IN_FLIGHT (%s)", rr.Code, rr.Body.String())
	}
}

// Review HIGH: the settlement stamps the FACTUAL return time before it
// reads the ledger, so a mid-settlement charge is refused, not advanced.
func TestBatch3_ReviewFix_ReturnStampPrecedesSettlement(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)

	f := seedRollingLease(t, e, "stamp", 8, 7)
	c1 := seedCycleRow(t, e, f.leaseID, 1, f.pickup, f.pickup.AddDate(0, 0, 7), 15000, "paid", strPtr("pi_b3_st_c1"), nil, 0)
	seedAccruing(t, e, f, c1, 15000, f.pickup, f.pickup.AddDate(0, 0, 7))

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, f.driver, f.leaseID, `{}`))
	if rr.Code != 201 {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, f.leaseID)
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, f.owner, ret.ID, `{}`))
	if rr.Code != 200 {
		t.Fatalf("confirm: %d (%s)", rr.Code, rr.Body.String())
	}
	var stamped time.Time
	e.db.Pool.QueryRow(ctx, `SELECT vehicle_returned_at FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&stamped)
	if !stamped.Equal(ret.ReturnedAt) {
		t.Errorf("vehicle_returned_at = %v, want the factual ReturnedAt %v (not finalize-time NOW)", stamped, ret.ReturnedAt)
	}
}

// Review MEDIUM: the return-aware needs_action TTL settles pro-rata
// arrears from the true return time — and waives outright when the week
// began at/after the return instead of inventing a 1-day-floor debt.
func TestBatch3_ReviewFix_ReturnAwareNeedsActionTTL(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	// Lease returned 3 days into week 2; week 2 sat in needs_action past
	// its TTL with NO intent (nothing to neutralize — the no-Stripe path).
	f := seedRollingLease(t, e, "ttl", 10, 7)
	c2Start := f.pickup.AddDate(0, 0, 7)
	returnedAt := c2Start.AddDate(0, 0, 3)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET vehicle_returned_at = $2 WHERE id = $1`, f.leaseID, returnedAt); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	used := seedCycleRow(t, e, f.leaseID, 2, c2Start, c2Start.AddDate(0, 0, 7), 15000, "needs_action", nil, nil, 0)
	overshoot := seedCycleRow(t, e, f.leaseID, 3, returnedAt.AddDate(0, 0, 4), returnedAt.AddDate(0, 0, 11), 15000, "needs_action", nil, nil, 0)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET needs_action_since = NOW() - interval '80 hours' WHERE id IN ($1, $2)`, used, overshoot); err != nil {
		t.Fatalf("age needs_action: %v", err)
	}

	e.leaseH.runBillingSweep(ctx)

	wantOwed := 15000 - models.ComputeReturnRefund(15000, 1, c2Start, returnedAt).RefundAmountCents
	var usedStatus string
	var usedAmount int64
	e.db.Pool.QueryRow(ctx, `SELECT status, amount_cents FROM billing_cycles WHERE id=$1`, used).Scan(&usedStatus, &usedAmount)
	if usedStatus != "arrears_due" || usedAmount != wantOwed {
		t.Errorf("used week TTL = %s/%d, want arrears_due/%d", usedStatus, usedAmount, wantOwed)
	}
	var overshootStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM billing_cycles WHERE id=$1`, overshoot).Scan(&overshootStatus)
	if overshootStatus != "waived" {
		t.Errorf("never-entered week TTL = %s, want waived (no 1-day-floor debt)", overshootStatus)
	}
	// And the driver was NOT marked delinquent for a finished rental.
	var delinquent *time.Time
	e.db.Pool.QueryRow(ctx, `SELECT delinquent_since FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&delinquent)
	if delinquent != nil {
		t.Errorf("returned lease marked delinquent by TTL: %v", delinquent)
	}
}

// Review HIGH: forgiving a week on a LIVE rental must ADVANCE paid-through
// — otherwise the resumed engine re-mints (re-bills) the exact period just
// forgiven. And the refund_pending float: a paid cycle whose webhook
// refund never landed is picked up by the post-return lister even though
// its period began before the return.
func TestBatch3_ReviewFix_WaiveAdvancesAndPendingFloatListed(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	admin := e.seedUser(t, "admin", "b3r_admin5_"+uuid.New().String()[:8]+"@example.com")

	// Live lease, delinquent on week 2 (failed_final), car kept.
	f := seedRollingLease(t, e, "adv", 10, 7)
	c2End := f.pickup.AddDate(0, 0, 14)
	c2 := seedCycleRow(t, e, f.leaseID, 2, f.pickup.AddDate(0, 0, 7), c2End, 15000, "failed_final", strPtr("pi_b3_adv_c2"), nil, 0)

	rr := httptest.NewRecorder()
	e.leaseH.AdminWaiveBillingCycle(rr, returnReq(t, admin, c2, `{"note":"forgiven on a live rental"}`))
	if rr.Code != 200 {
		t.Fatalf("waive: %d (%s)", rr.Code, rr.Body.String())
	}
	var endsAt time.Time
	e.db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, f.leaseID).Scan(&endsAt)
	if !endsAt.Equal(c2End) {
		t.Errorf("paid-through after live waive = %v, want the forgiven week's end %v (no re-bill)", endsAt, c2End)
	}

	// refund_pending float: paid cycle, period began BEFORE the return,
	// refund_pending marker stamped, no refund — must be listed.
	f2 := seedRollingLease(t, e, "float", 9, 7)
	returned := f2.pickup.AddDate(0, 0, 8)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET vehicle_returned_at=$2 WHERE id=$1`, f2.leaseID, returned); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	floater := seedCycleRow(t, e, f2.leaseID, 2, f2.pickup.AddDate(0, 0, 7), f2.pickup.AddDate(0, 0, 14), 15000, "paid", strPtr("pi_b3_fl_c2"), nil, 0)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET admin_note='refund_pending: charge landed after occupancy ended' WHERE id=$1`, floater); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err := billingRepo.ListPaidCyclesAfterReturn(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, c := range got {
		if c.ID == floater {
			found = true
		}
	}
	if !found {
		t.Errorf("refund_pending float not listed by the post-return reconciler")
	}
}

// Review MEDIUM: 'retrying' on a returned lease has no other closer — the
// retry phase skips returned leases forever. The closer phase settles it
// as pro-rata arrears (no intent here — nothing to neutralize) and an
// overshoot 'failed_final' waives.
func TestBatch3_ReviewFix_ReturnedLeaseCloserPhase(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetDisputeDependencies(repository.NewChargeDisputeRepository(e.db), e.payoutRepo)

	f := seedRollingLease(t, e, "closer", 10, 7)
	c2Start := f.pickup.AddDate(0, 0, 7)
	returnedAt := c2Start.AddDate(0, 0, 3)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET vehicle_returned_at=$2 WHERE id=$1`, f.leaseID, returnedAt); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	stuck := seedCycleRow(t, e, f.leaseID, 2, c2Start, c2Start.AddDate(0, 0, 7), 15000, "retrying", nil, nil, 0)
	ghost := seedCycleRow(t, e, f.leaseID, 3, returnedAt.AddDate(0, 0, 4), returnedAt.AddDate(0, 0, 11), 15000, "failed_final", nil, nil, 0)

	e.leaseH.runBillingSweep(ctx)

	wantOwed := 15000 - models.ComputeReturnRefund(15000, 1, c2Start, returnedAt).RefundAmountCents
	var st string
	var amt int64
	e.db.Pool.QueryRow(ctx, `SELECT status, amount_cents FROM billing_cycles WHERE id=$1`, stuck).Scan(&st, &amt)
	if st != "arrears_due" || amt != wantOwed {
		t.Errorf("retrying-on-returned = %s/%d, want arrears_due/%d", st, amt, wantOwed)
	}
	e.db.Pool.QueryRow(ctx, `SELECT status FROM billing_cycles WHERE id=$1`, ghost).Scan(&st)
	if st != "waived" {
		t.Errorf("failed_final overshoot = %s, want waived", st)
	}
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1`, f.leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Errorf("arrears ticket count = %d, want exactly 1", tickets)
	}
}
