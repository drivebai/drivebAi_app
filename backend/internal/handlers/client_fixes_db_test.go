package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// DB-gated tests for the September client-fix batch, money items first.
// Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestClientFixes -v
//
// Stripe is nil throughout — every asserted path either never reaches
// Stripe (guards) or takes the zero-refund fast path.

// Item 1: an admin resolving a "driver did not return" dispute in the
// OWNER's favour accepts with driver_refund_cents=0 — the return closes,
// the car releases, the driver gets NOTHING back, and the owner's payout
// ledger row carries the full paid amount.
func TestClientFixes_DisputeAcceptZeroRefund(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "cf_owner_z@example.com")
	driver := e.seedUser(t, "driver", "cf_driver_z@example.com")
	admin := e.seedUser(t, "admin", "cf_admin_z@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)
	e.seedPayment(t, leaseID, 30000)
	e.cleanupLedger(t, leaseID)

	// Driver initiates on day one — the snapshot refund is large.
	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, err := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if err != nil || ret == nil {
		t.Fatalf("load return: %v", err)
	}
	if ret.RefundAmountCents <= 0 {
		t.Fatalf("test needs a non-zero snapshot refund, got %d", ret.RefundAmountCents)
	}

	// Owner disputes: the car did not come back.
	rr = httptest.NewRecorder()
	e.returnH.Dispute(rr, returnReq(t, owner, ret.ID, `{"reason":"Driver did not return"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("dispute: %d (%s)", rr.Code, rr.Body.String())
	}

	// Admin accepts WITH the zero override — owner's favour.
	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, admin, ret.ID,
		`{"resolution":"accept","driver_refund_cents":0,"note":"Car not returned; closing with no driver refund."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("admin resolve: %d (%s)", rr.Code, rr.Body.String())
	}

	final, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if final.Status != models.VehicleReturnCompleted {
		t.Errorf("status = %s, want completed", final.Status)
	}
	if final.RefundAmountCents != 0 {
		t.Errorf("refund = %d, want 0 — money went back to a driver who kept the car", final.RefundAmountCents)
	}
	if final.RefundStatus == nil || *final.RefundStatus != models.VehicleReturnRefundNotApplicable {
		t.Errorf("refund_status = %v, want not_applicable (no Stripe call)", final.RefundStatus)
	}
	var carStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id = $1`, carID).Scan(&carStatus)
	if carStatus != "available" {
		t.Errorf("car = %s, want available", carStatus)
	}
	// The owner is settled on the FULL paid amount.
	row, _ := e.payoutRepo.GetByLeaseRequestID(ctx, leaseID)
	if row == nil || row.GrossKeptCents != 30000 {
		t.Errorf("payout ledger kept = %v, want 30000", row)
	}
}

// Item 1: the settle endpoint no longer silently drops the refund override
// on an EXISTING disputed row (the force-accept-with-snapshot defect).
func TestClientFixes_SettleCloseHonorsOverrideOnDisputed(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "cf_owner_s@example.com")
	driver := e.seedUser(t, "driver", "cf_driver_s@example.com")
	admin := e.seedUser(t, "admin", "cf_admin_s@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.seedPayment(t, leaseID, 20000)
	e.cleanupLedger(t, leaseID)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	rr = httptest.NewRecorder()
	e.returnH.Dispute(rr, returnReq(t, owner, ret.ID, `{"reason":"Driver did not return"}`))

	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"close","driver_refund_cents":0,"note":"Car never came back; settling owner in full."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("settle close: %d (%s)", rr.Code, rr.Body.String())
	}
	final, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if final.Status != models.VehicleReturnCompleted || final.RefundAmountCents != 0 {
		t.Errorf("status=%s refund=%d, want completed/0 — override must not be dropped", final.Status, final.RefundAmountCents)
	}
}

// Item 1: the override is bounded and locked — never above the paid
// snapshot, never after refunding has started.
func TestClientFixes_RefundOverrideGuards(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "cf_owner_g@example.com")
	driver := e.seedUser(t, "driver", "cf_driver_g@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.seedPayment(t, leaseID, 10000)
	e.cleanupLedger(t, leaseID)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)

	if _, err := e.returnRepo.UpdateRefundAmount(ctx, ret.ID, 10001); err == nil {
		t.Error("override above paid_amount_cents was accepted")
	}
	if _, err := e.returnRepo.UpdateRefundAmount(ctx, ret.ID, 5000); err != nil {
		t.Errorf("valid override rejected: %v", err)
	}
	// Once the row moves past the pre-refund states, the amount locks.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE vehicle_returns SET status='owner_confirmed' WHERE id=$1`, ret.ID); err != nil {
		t.Fatalf("force state: %v", err)
	}
	if _, err := e.returnRepo.UpdateRefundAmount(ctx, ret.ID, 0); err == nil {
		t.Error("override accepted after resolution started")
	}
}

// Item 2: the paid transition clears a pending price review (the stuck-flag
// race), and a paid lease's own agreed price is immune to LISTING price
// edits — handover still works after the owner changes the listing price.
func TestClientFixes_PriceEditDuringRental(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "cf_owner_p@example.com")
	driver := e.seedUser(t, "driver", "cf_driver_p@example.com")
	e.seedLicense(t, driver)

	// Build a lease up to accepted via the product path.
	car := e.seedCar(t, owner, "available", true, false)
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID uuid.UUID `json:"id"`
		} `json:"lease_request"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	leaseID := created.LeaseRequest.ID
	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id = $1`, leaseID)
	})

	// Owner adjusts the price while accepted → review pending.
	if _, _, err := e.leaseRepo.UpdateOfferedPrice(ctx, leaseID, owner, 50); err != nil {
		t.Fatalf("adjust at accepted: %v", err)
	}
	lr, _ := e.leaseRepo.GetByID(ctx, leaseID)
	if !lr.PriceChangePending {
		t.Fatal("price_change_pending not set by adjust")
	}

	// The payment succeeds anyway (the race): SetPaid must clear the flag.
	paid, err := e.leaseRepo.SetPaid(ctx, leaseID)
	if err != nil {
		t.Fatalf("set paid: %v", err)
	}
	if paid.PriceChangePending {
		t.Error("price_change_pending survived the paid transition — stuck review state")
	}

	// Adjusting once payment is in flight (payment_pending or later) is refused.
	if _, _, err := e.leaseRepo.UpdateOfferedPrice(ctx, leaseID, owner, 40); err == nil {
		t.Error("price adjust allowed on a paid lease")
	}

	// The owner edits the LISTING price — decoupled by design.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE cars SET weekly_rent_price = 50 WHERE id = $1`, car); err != nil {
		t.Fatalf("edit listing price: %v", err)
	}

	// Handover still works: pickup confirms cleanly.
	if _, err := e.leaseRepo.ConfirmPickup(ctx, leaseID, driver); err != nil {
		t.Errorf("pickup after listing price edit failed: %v", err)
	}
	lr, _ = e.leaseRepo.GetByID(ctx, leaseID)
	if lr.PickupConfirmedAt == nil {
		t.Error("pickup not stamped")
	}
}
