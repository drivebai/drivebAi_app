package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// DB-gated tests for the money dead-end batch (items 2–4). Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestDeadEnd -v
//
// Stripe is nil throughout: item 3's permanent-failure path triggers on a
// payment row with NO PaymentIntent (which never reaches Stripe), and item
// 4's sweep skips the PI dance when no intent exists.

// Item 2: the "Refund delayed" notice claims exactly once, and a revived
// return may notify once again (new episode).
func TestDeadEnd_RefundDelayNoticeClaimedOnce(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "de_owner_n@example.com")
	driver := e.seedUser(t, "driver", "de_driver_n@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)

	claimed, err := e.returnRepo.ClaimRefundDelayNotice(ctx, ret.ID)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v/%v, want true", claimed, err)
	}
	claimed, _ = e.returnRepo.ClaimRefundDelayNotice(ctx, ret.ID)
	if claimed {
		t.Error("second claim succeeded — the driver would be spammed every sweep tick")
	}

	// A fresh episode (cancel → revive) resets the claim.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE vehicle_returns SET status='cancelled', cancelled_at=NOW() WHERE id=$1`, ret.ID); err != nil {
		t.Fatalf("force cancel: %v", err)
	}
	if _, err := e.returnRepo.ReviveCancelled(ctx, ret.ID, driver, time.Now().UTC(), 1, 100, 200); err != nil {
		t.Fatalf("revive: %v", err)
	}
	claimed, _ = e.returnRepo.ClaimRefundDelayNotice(ctx, ret.ID)
	if !claimed {
		t.Error("revived return could not claim a fresh notice")
	}
}

// Item 3: a refund with no PaymentIntent on record is PERMANENT — one
// unrecoverable mark, one ticket, no retry hammering — and the settle
// endpoint is the product exit: close forces $0, completes the return,
// and releases the car. No SQL rescues.
func TestDeadEnd_UnrecoverableRefundHasProductExit(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "de_owner_u@example.com")
	driver := e.seedUser(t, "driver", "de_driver_u@example.com")
	admin := e.seedUser(t, "admin", "de_admin_u@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)
	e.seedPayment(t, leaseID, 30000) // amount recorded, but NO payment_intent_id
	e.cleanupLedger(t, leaseID)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if ret.RefundAmountCents <= 0 {
		t.Fatal("test needs a positive refund snapshot")
	}

	// Owner confirms → issueRefund finds a payment with no PI → permanent.
	rr = httptest.NewRecorder()
	e.returnH.OwnerConfirm(rr, returnReq(t, owner, ret.ID, `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner confirm: %d (%s)", rr.Code, rr.Body.String())
	}

	ret, _ = e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if ret.Status != models.VehicleReturnOwnerConfirmed {
		t.Fatalf("status = %s, want owner_confirmed", ret.Status)
	}
	if ret.RefundStatus == nil || *ret.RefundStatus != models.VehicleReturnRefundUnrecoverable {
		t.Fatalf("refund_status = %v, want unrecoverable", ret.RefundStatus)
	}

	// Exactly one live ticket, linked to the return.
	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets WHERE vehicle_return_id=$1 AND status NOT IN ('resolved','closed')`, ret.ID).Scan(&tickets)
	if tickets != 1 {
		t.Errorf("live tickets = %d, want exactly 1", tickets)
	}

	// The retry sweep must NOT pick it up.
	stuck, _ := e.returnRepo.ListStuckRefunds(ctx, time.Now().UTC().Add(time.Hour), 50)
	for _, s := range stuck {
		if s.ID == ret.ID {
			t.Error("unrecoverable row still in the retry sweep — would hammer a dead PI forever")
		}
	}

	// A non-zero override is refused — Stripe cannot move this money.
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"close","driver_refund_cents":5000,"note":"trying to refund through a dead PI"}`))
	if rr.Code != http.StatusConflict {
		t.Errorf("non-zero override = %d, want 409", rr.Code)
	}

	// The product exit: close with 0 → completed, car released.
	rr = httptest.NewRecorder()
	e.returnH.AdminSettleRent(rr, settleReq(t, admin, leaseID,
		`{"resolution":"close","driver_refund_cents":0,"note":"Refund unprocessable at Stripe; repaid the driver manually via check #1042."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("settle close: %d (%s)", rr.Code, rr.Body.String())
	}
	final, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if final.Status != models.VehicleReturnCompleted || final.RefundAmountCents != 0 {
		t.Errorf("final = %s/%d, want completed/0", final.Status, final.RefundAmountCents)
	}
	var carStatus string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id=$1`, carID).Scan(&carStatus)
	if carStatus != "available" {
		t.Errorf("car = %s, want available", carStatus)
	}
}

// Item 4: payment_pending has three exits now — driver cancel, owner
// decline, and the 24h sweep — and each releases the car's reservation.
func TestDeadEnd_PaymentPendingExits(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "de_owner_p@example.com")
	driver := e.seedUser(t, "driver", "de_driver_p@example.com")
	e.seedLicense(t, driver)

	mkPending := func() (uuid.UUID, uuid.UUID) {
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
			t.Fatalf("decode: %v", err)
		}
		id := created.LeaseRequest.ID
		// The handler notifies the owner on a goroutine whose INSERT holds a
		// KEY SHARE lock on the lease; the sweep's FOR UPDATE SKIP LOCKED
		// skips the row while that INSERT is in flight. Wait for it, so the
		// test measures the sweep and not a race with its own fixture.
		for i := 0; i < 100; i++ {
			var n int
			_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE related_lease_request_id = $1`, id).Scan(&n)
			if n > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, id, owner); err != nil {
			t.Fatalf("accept: %v", err)
		}
		if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET status='payment_pending', payment_pending_at=NOW() WHERE id=$1`, id); err != nil {
			t.Fatalf("force payment_pending: %v", err)
		}
		return id, car
	}

	assertReleased := func(label string, carID uuid.UUID, wantStatus string, leaseID uuid.UUID) {
		var status string
		e.db.Pool.QueryRow(ctx, `SELECT status FROM lease_requests WHERE id=$1`, leaseID).Scan(&status)
		if status != wantStatus {
			t.Errorf("%s: lease = %s, want %s", label, status, wantStatus)
		}
		var reserved *string
		e.db.Pool.QueryRow(ctx, `SELECT reserved_by_lease_request_id::text FROM cars WHERE id=$1`, carID).Scan(&reserved)
		if reserved != nil {
			t.Errorf("%s: car still reserved by %s", label, *reserved)
		}
	}

	// Exit 1: driver cancels mid-payment-window.
	l1, c1 := mkPending()
	rr := httptest.NewRecorder()
	e.leaseH.CancelLeaseRequest(rr, leaseActionReq(t, driver, l1))
	if rr.Code != http.StatusOK {
		t.Fatalf("driver cancel: %d (%s)", rr.Code, rr.Body.String())
	}
	assertReleased("driver-cancel", c1, "cancelled", l1)

	// Exit 2: owner declines mid-payment-window.
	l2, c2 := mkPending()
	rr = httptest.NewRecorder()
	e.leaseH.DeclineLeaseRequest(rr, leaseActionReq(t, owner, l2))
	if rr.Code != http.StatusOK {
		t.Fatalf("owner decline: %d (%s)", rr.Code, rr.Body.String())
	}
	assertReleased("owner-decline", c2, "declined", l2)

	// Exit 3: the sweep expires a stale window and releases the car.
	l3, c3 := mkPending()
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET payment_pending_at = NOW() - INTERVAL '25 hours' WHERE id=$1`, l3); err != nil {
		t.Fatalf("age lease: %v", err)
	}
	e.leaseH.runPaymentPendingSweep(ctx)
	assertReleased("sweep", c3, "expired", l3)

	// Race serializer: a lease the webhook already flipped to paid cannot
	// be claimed by the sweep.
	l4, _ := mkPending()
	if _, err := e.leaseRepo.SetPaid(ctx, l4); err != nil {
		t.Fatalf("set paid: %v", err)
	}
	if _, err := e.leaseRepo.ClaimPaymentExpiry(ctx, l4); err == nil {
		t.Error("claim succeeded on a PAID lease — the sweep could kill a paid rental")
	}
}

// leaseActionReq builds a POST /lease-requests/{id}/(cancel|decline)
// request with the chi param + acting user wired.
func leaseActionReq(t *testing.T, userID, leaseID uuid.UUID) *http.Request {
	t.Helper()
	return returnReq(t, userID, leaseID, `{}`)
}

// Accepted-lease TTL (client decision, Sep 4): warn once at 48h, expire at
// 72h releasing the car, and never touch a lease whose payment started.
func TestDeadEnd_AcceptExpiryTTL(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "de_owner_a@example.com")
	driver := e.seedUser(t, "driver", "de_driver_a@example.com")
	e.seedLicense(t, driver)

	mkAccepted := func(ageHours int) (uuid.UUID, uuid.UUID) {
		car := e.seedCar(t, owner, "available", true, false)
		rr := httptest.NewRecorder()
		e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
		var created struct {
			LeaseRequest struct {
				ID uuid.UUID `json:"id"`
			} `json:"lease_request"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode: %v", err)
		}
		id := created.LeaseRequest.ID
		// The handler notifies the owner on a goroutine whose INSERT holds a
		// KEY SHARE lock on the lease; the sweep's FOR UPDATE SKIP LOCKED
		// skips the row while that INSERT is in flight. Wait for it, so the
		// test measures the sweep and not a race with its own fixture.
		for i := 0; i < 100; i++ {
			var n int
			_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE related_lease_request_id = $1`, id).Scan(&n)
			if n > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, id, owner); err != nil {
			t.Fatalf("accept: %v", err)
		}
		if ageHours > 0 {
			if _, err := e.db.Pool.Exec(ctx,
				`UPDATE lease_requests SET accepted_at = NOW() - ($2::int || ' hours')::interval WHERE id=$1`,
				id, ageHours); err != nil {
				t.Fatalf("age: %v", err)
			}
		}
		return id, car
	}

	// 49h old: warned exactly once, NOT expired.
	l1, c1 := mkAccepted(49)
	e.leaseH.runAcceptExpirySweep(ctx)
	var warned1 *time.Time
	e.db.Pool.QueryRow(ctx, `SELECT accept_expiry_warned_at FROM lease_requests WHERE id=$1`, l1).Scan(&warned1)
	if warned1 == nil {
		t.Fatal("49h lease not warned")
	}
	var st string
	e.db.Pool.QueryRow(ctx, `SELECT status FROM lease_requests WHERE id=$1`, l1).Scan(&st)
	if st != "accepted" {
		t.Fatalf("49h lease = %s, want still accepted", st)
	}
	e.leaseH.runAcceptExpirySweep(ctx)
	var warned2 *time.Time
	e.db.Pool.QueryRow(ctx, `SELECT accept_expiry_warned_at FROM lease_requests WHERE id=$1`, l1).Scan(&warned2)
	if warned2 == nil || !warned2.Equal(*warned1) {
		t.Error("warning re-claimed on second tick — parties would be spammed")
	}
	_ = c1

	// 73h old: expired, car released.
	l2, c2 := mkAccepted(73)
	e.leaseH.runAcceptExpirySweep(ctx)
	e.db.Pool.QueryRow(ctx, `SELECT status FROM lease_requests WHERE id=$1`, l2).Scan(&st)
	if st != "expired" {
		t.Fatalf("73h lease = %s, want expired", st)
	}
	var reserved *string
	e.db.Pool.QueryRow(ctx, `SELECT reserved_by_lease_request_id::text FROM cars WHERE id=$1`, c2).Scan(&reserved)
	if reserved != nil {
		t.Error("expired accepted lease still holds the reservation")
	}

	// Race: payment started (payment_pending) → claim refuses.
	l3, _ := mkAccepted(73)
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET status='payment_pending' WHERE id=$1`, l3); err != nil {
		t.Fatalf("force: %v", err)
	}
	if _, err := e.leaseRepo.ClaimAcceptExpiry(ctx, l3); err == nil {
		t.Error("accept-expiry claimed a lease whose payment started")
	}
}
