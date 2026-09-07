package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// DB-gated tests for batch 1 (dispute/refund webhooks, audit M2). Stripe is
// nil: the reversal call is nil-guarded, so the LOST path exercises the
// claim/ledger mechanics without a network call. Run with TEST_DATABASE_URL
// (scratch migrated through 000053).

func disputeEnv(t *testing.T) (*payoutEnv, *repository.ChargeDisputeRepository) {
	e := newPayoutEnv(t)
	dr := repository.NewChargeDisputeRepository(e.db)
	e.leaseH.SetDisputeDependencies(dr, e.payoutRepo)
	e.leaseH.SetReturnRepositoryForDisputes(e.returnRepo)
	return e, dr
}

func disputeObj(dpID, chargeID, intentID, status string, amount int64) map[string]interface{} {
	return map[string]interface{}{
		"id": dpID, "charge": chargeID, "payment_intent": intentID,
		"status": status, "reason": "fraudulent", "amount": float64(amount), "currency": "usd",
	}
}

func webhookReq() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", nil)
}

// Open → withhold + ticket, claimed-once under redelivery; WON → release.
func TestBatch1_DisputeOpenWithholdsThenWonReleases(t *testing.T) {
	e, dr := disputeEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "b1_owner_w@example.com")
	driver := e.seedUser(t, "driver", "b1_driver_w@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	intent := "pi_b1_won_" + leaseID.String()[:8]
	seedPaymentAt(t, e, leaseID, 15000, "succeeded", &intent)

	// A pending payout row for the lease (the thing the dispute must park).
	e.payoutH.SettleRentalPayout(ctx, leaseID, owner, 15000, models.PayoutSourceReturnCompleted, nil)
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM charge_disputes WHERE lease_request_id=$1`, leaseID) })

	dp := disputeObj("dp_b1_won", "ch_b1_won", intent, "needs_response", 15000)
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.created", dp); !ok {
		t.Fatal("dispute created handling returned not-ok")
	}

	var status string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE lease_request_id=$1`, leaseID).Scan(&status)
	if status != "withheld" {
		t.Fatalf("payout status = %q, want withheld", status)
	}
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("live tickets = %d, want 1", tickets)
	}

	// Redelivery: no duplicate side effects.
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.created", dp); !ok {
		t.Fatal("redelivery returned not-ok")
	}
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("after redelivery: live tickets = %d, want 1", tickets)
	}

	// WON: withheld row returns to pending; dispute closes claimed-once.
	dpWon := disputeObj("dp_b1_won", "ch_b1_won", intent, "won", 15000)
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.closed", dpWon); !ok {
		t.Fatal("dispute won handling returned not-ok")
	}
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE lease_request_id=$1`, leaseID).Scan(&status)
	if status != "pending" {
		t.Fatalf("after won: payout status = %q, want pending", status)
	}
	d, _ := dr.GetByStripeID(ctx, "dp_b1_won")
	if d == nil || d.Outcome == nil || *d.Outcome != "won" || d.ClosedAt == nil {
		t.Fatalf("dispute row not closed-won: %+v", d)
	}
	// Closed redelivery: benign.
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.closed", dpWon); !ok {
		t.Fatal("closed redelivery returned not-ok")
	}
}

// LOST on a PAID payout: reversal side effects claim exactly once (Stripe
// nil → no network call; the ledger flip is exercised via MarkReversed
// directly since the reversal id comes from Stripe in prod).
func TestBatch1_DisputeLostClaimsOnce(t *testing.T) {
	e, dr := disputeEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "b1_owner_l@example.com")
	driver := e.seedUser(t, "driver", "b1_driver_l@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	intent := "pi_b1_lost_" + leaseID.String()[:8]
	seedPaymentAt(t, e, leaseID, 15000, "succeeded", &intent)
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM charge_disputes WHERE lease_request_id=$1`, leaseID) })

	// Paid payout row funded by the disputed charge.
	e.payoutH.SettleRentalPayout(ctx, leaseID, owner, 15000, models.PayoutSourceReturnCompleted, nil)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE owner_payouts SET status='paid', source_charge_id='ch_b1_lost',
		    stripe_transfer_id='tr_b1_lost', paid_at=NOW() WHERE lease_request_id=$1`, leaseID); err != nil {
		t.Fatalf("force paid: %v", err)
	}

	dp := disputeObj("dp_b1_lost", "ch_b1_lost", intent, "under_review", 15000)
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.created", dp); !ok {
		t.Fatal("created not-ok")
	}
	dpLost := disputeObj("dp_b1_lost", "ch_b1_lost", intent, "lost", 15000)
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.closed", dpLost); !ok {
		t.Fatal("lost not-ok")
	}

	d, _ := dr.GetByStripeID(ctx, "dp_b1_lost")
	if d == nil || d.Outcome == nil || *d.Outcome != "lost" || !d.ReversalDone {
		t.Fatalf("dispute not closed-lost with reversal claimed: %+v", d)
	}
	// The paid row was untouched by open-withholding (paid is exempt).
	var status string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE lease_request_id=$1`, leaseID).Scan(&status)
	if status != "paid" {
		t.Fatalf("paid row status = %q (stripe nil → reversal skipped, row stays paid until MarkReversed)", status)
	}
	// The ledger flip itself: claimed-once by status scope.
	var rowID uuid.UUID
	e.db.Pool.QueryRow(ctx, `SELECT id FROM owner_payouts WHERE lease_request_id=$1`, leaseID).Scan(&rowID)
	ok, err := e.payoutRepo.MarkReversed(ctx, rowID, "trr_test", 13500, "dispute_lost")
	if err != nil || !ok {
		t.Fatalf("MarkReversed = %v/%v, want true", ok, err)
	}
	ok, _ = e.payoutRepo.MarkReversed(ctx, rowID, "trr_test2", 13500, "dispute_lost")
	if ok {
		t.Fatal("second MarkReversed claimed — double clawback recorded")
	}
	// Redelivery of lost: no second reversal claim (flag already true).
	if ok := e.leaseH.handleChargeDispute(webhookReq(), "charge.dispute.closed", dpLost); !ok {
		t.Fatal("lost redelivery not-ok")
	}
}

// charge.refunded: recognized refunds no-op; an external one withholds
// unpaid payouts and opens a reconciliation ticket.
func TestBatch1_ExternalRefundWithholdsAndTickets(t *testing.T) {
	e, _ := disputeEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "b1_owner_r@example.com")
	driver := e.seedUser(t, "driver", "b1_driver_r@example.com")
	e.seedLicense(t, driver)

	// Recognized: lease carries our own refund_id → nothing happens.
	leaseA, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseA)
	intentA := "pi_b1_recog_" + leaseA.String()[:8]
	seedPaymentAt(t, e, leaseA, 15000, "succeeded", &intentA)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET refund_id='re_ours' WHERE id=$1`, leaseA); err != nil {
		t.Fatalf("set refund id: %v", err)
	}
	objA := map[string]interface{}{"id": "ch_recog", "payment_intent": intentA, "amount_refunded": float64(15000)}
	if ok := e.leaseH.handleChargeRefunded(webhookReq(), objA); !ok {
		t.Fatal("recognized refund not-ok")
	}
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseA).Scan(&tickets)
	if tickets != 0 {
		t.Fatalf("recognized refund opened %d tickets, want 0", tickets)
	}

	// External: no refund of record → withhold + ticket.
	leaseB, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseB)
	intentB := "pi_b1_ext_" + leaseB.String()[:8]
	seedPaymentAt(t, e, leaseB, 15000, "succeeded", &intentB)
	e.payoutH.SettleRentalPayout(ctx, leaseB, owner, 15000, models.PayoutSourceReturnCompleted, nil)
	objB := map[string]interface{}{"id": "ch_ext", "payment_intent": intentB, "amount_refunded": float64(15000)}
	if ok := e.leaseH.handleChargeRefunded(webhookReq(), objB); !ok {
		t.Fatal("external refund not-ok")
	}
	var status string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM owner_payouts WHERE lease_request_id=$1`, leaseB).Scan(&status)
	if status != "withheld" {
		t.Fatalf("payout after external refund = %q, want withheld", status)
	}
	e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`, leaseB).Scan(&tickets)
	if tickets != 1 {
		t.Fatalf("external refund tickets = %d, want 1", tickets)
	}
}
