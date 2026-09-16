package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// The Nissan Sentra class: a paid lease whose pickup deadline was never armed
// holds its car's reservation forever, because every scanner query requires
// pickup_deadline_at IS NOT NULL. These tests pin the owner's exit.

// stalePaidLease builds the exact shape: paid, never picked up, NULL deadline,
// holding the car's reservation, aged past the release floor.
func (e *payoutEnv) stalePaidLease(t *testing.T, owner, driver uuid.UUID, age time.Duration) (leaseID, carID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	carID = e.seedCar(t, owner, "available", true, false)
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO lease_requests (chat_id, listing_id, owner_id, driver_id, status, weekly_price, currency, weeks,
			message, expires_at, created_at, updated_at)
		VALUES ((SELECT id FROM chats WHERE car_id=$1 AND driver_id=$2 AND owner_id=$3
		         UNION ALL SELECT gen_random_uuid() LIMIT 1),
		        $1,$3,$2,'paid',350,'USD',1,'', NOW() + interval '1 day',
		        NOW() - $4::interval, NOW() - $4::interval)
		RETURNING id`, carID, driver, owner, durText(age)).Scan(&leaseID); err != nil {
		// chats row may not exist; create it then retry
		var chatID uuid.UUID
		if cerr := e.db.Pool.QueryRow(ctx,
			`INSERT INTO chats (car_id, driver_id, owner_id) VALUES ($1,$2,$3) RETURNING id`,
			carID, driver, owner).Scan(&chatID); cerr != nil {
			t.Fatalf("seed chat: %v", cerr)
		}
		if err2 := e.db.Pool.QueryRow(ctx, `
			INSERT INTO lease_requests (chat_id, listing_id, owner_id, driver_id, status, weekly_price, currency, weeks,
				message, expires_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'paid',350,'USD',1,'', NOW() + interval '1 day', NOW() - $5::interval, NOW() - $5::interval)
			RETURNING id`, chatID, carID, owner, driver, durText(age)).Scan(&leaseID); err2 != nil {
			t.Fatalf("seed stale lease: %v", err2)
		}
	}
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE cars SET reserved_by_lease_request_id = $2 WHERE id = $1`, carID, leaseID); err != nil {
		t.Fatalf("reserve car: %v", err)
	}
	// The release floor is measured on the PAYMENT's age, so the fixture has
	// to carry one — a lease with no succeeded payment is not releasable.
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO payments (lease_request_id, provider, amount, currency, platform_fee_amount, status, created_at, updated_at)
		VALUES ($1, 'stripe', 35000, 'USD', 3500, 'succeeded', NOW() - $2::interval, NOW() - $2::interval)`,
		leaseID, durText(age)); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_lease_request_id = NULL WHERE id = $1`, carID)
		e.db.Pool.Exec(ctx, `DELETE FROM payments WHERE lease_request_id = $1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE id = $1`, leaseID)
	})
	return leaseID, carID
}

func durText(d time.Duration) string {
	return time.Duration(d).String()
}

func (e *payoutEnv) carReservedBy(t *testing.T, carID uuid.UUID) *uuid.UUID {
	t.Helper()
	var v *uuid.UUID
	if err := e.db.Pool.QueryRow(context.Background(),
		`SELECT reserved_by_lease_request_id FROM cars WHERE id = $1`, carID).Scan(&v); err != nil {
		t.Fatalf("car reservation: %v", err)
	}
	return v
}

// The scanner genuinely cannot see this row — that is the whole defect.
func TestStalePickupHoldIsInvisibleToTheScanner(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "sph_o_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "sph_d_"+run+"@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.stalePaidLease(t, owner, driver, 200*24*time.Hour)

	expired, err := e.leaseRepo.ListExpiredAwaitingPickup(ctx, time.Now().UTC(), 200)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	for _, c := range expired {
		if c.ID == leaseID {
			t.Fatal("the scanner can see it — this test no longer describes the defect")
		}
	}
	if _, cerr := e.leaseRepo.ClaimForExpiry(ctx, leaseID); cerr == nil {
		t.Fatal("ClaimForExpiry claimed a NULL-deadline lease; the guard changed")
	}
	if got := e.carReservedBy(t, carID); got == nil || *got != leaseID {
		t.Fatal("fixture is wrong: the car is not held by the stale lease")
	}

	// But it IS visible on the surface built to show it.
	holds, err := e.leaseRepo.ListStalePickupHolds(ctx, nil, models.LeaseOwnerReleaseMinAge, 200)
	if err != nil {
		t.Fatalf("list stale holds: %v", err)
	}
	found := false
	for _, h := range holds {
		if h.LeaseRequestID == leaseID {
			found = true
			if h.CarID != carID || h.OwnerID != owner {
				t.Errorf("hold reports the wrong car/owner: %+v", h)
			}
		}
	}
	if !found {
		t.Error("a stale pickup hold is invisible to ListStalePickupHolds — the owner would never be told")
	}
}

// The owner's exit: releases the car, ends the lease, and is refused for
// everyone and everything it must be refused for.
func TestOwnerCanReleaseAStalePickupHold(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "rel_o_"+run+"@example.com")
	stranger := e.seedUser(t, "car_owner", "rel_x_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "rel_d_"+run+"@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.stalePaidLease(t, owner, driver, 200*24*time.Hour)

	// Not the owner -> no claim, nothing moves.
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, leaseID, stranger, models.LeaseOwnerReleaseMinAge); err == nil {
		t.Fatal("a stranger released someone else's car")
	}
	if got := e.carReservedBy(t, carID); got == nil || *got != leaseID {
		t.Fatal("a refused release still touched the reservation")
	}

	// The owner can.
	lr, err := e.leaseRepo.ClaimForOwnerRelease(ctx, leaseID, owner, models.LeaseOwnerReleaseMinAge)
	if err != nil || lr == nil {
		t.Fatalf("owner release: %v", err)
	}
	if lr.Status != models.LeaseStatusExpiredRefunded {
		t.Errorf("status = %s, want expired_refunded (same terminal state the scanner uses)", lr.Status)
	}
	if got := e.carReservedBy(t, carID); got != nil {
		t.Errorf("the car is still reserved by %s — the release did not free it", got)
	}
	// Claimed-once.
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, leaseID, owner, models.LeaseOwnerReleaseMinAge); err == nil {
		t.Error("the release claimed the same lease twice")
	}
	// And it drops off the visibility list.
	holds, _ := e.leaseRepo.ListStalePickupHolds(ctx, nil, models.LeaseOwnerReleaseMinAge, 200)
	for _, h := range holds {
		if h.LeaseRequestID == leaseID {
			t.Error("a released hold is still listed as stale")
		}
	}
}

// A rental that is merely young, or already live, must be untouchable.
func TestOwnerReleaseRefusesLiveAndRecentRentals(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "rr_o_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "rr_d_"+run+"@example.com")
	e.seedLicense(t, driver)

	// Too recent: inside the floor.
	youngLease, youngCar := e.stalePaidLease(t, owner, driver, 2*time.Hour)
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, youngLease, owner, models.LeaseOwnerReleaseMinAge); err == nil {
		t.Error("released a 2-hour-old rental — the age floor is not holding")
	}
	if got := e.carReservedBy(t, youngCar); got == nil {
		t.Error("a refused release freed the car anyway")
	}

	// Already picked up: live rental, never releasable by this path.
	liveLease, liveCar := e.stalePaidLease(t, owner, driver, 200*24*time.Hour)
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE lease_requests SET pickup_confirmed_at = NOW() - interval '10 days' WHERE id = $1`, liveLease); err != nil {
		t.Fatalf("confirm pickup: %v", err)
	}
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, liveLease, owner, models.LeaseOwnerReleaseMinAge); err == nil {
		t.Error("released a rental the driver had already picked up")
	}
	if got := e.carReservedBy(t, liveCar); got == nil {
		t.Error("a live rental's car was freed")
	}
	// And a live rental is not on the stale list.
	holds, _ := e.leaseRepo.ListStalePickupHolds(ctx, nil, models.LeaseOwnerReleaseMinAge, 200)
	for _, h := range holds {
		if h.LeaseRequestID == liveLease || h.LeaseRequestID == youngLease {
			t.Errorf("a live/recent rental appears as a stale hold: %s", h.LeaseRequestID)
		}
	}
}

// The endpoint itself: owner-only, and a refusal explains which reason it is.
func TestOwnerReleaseEndpointGuards(t *testing.T) {
	e := newPayoutEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, false)
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "ep_o_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "ep_d_"+run+"@example.com")
	e.seedLicense(t, driver)
	youngLease, _ := e.stalePaidLease(t, owner, driver, 1*time.Hour)

	rr := httptest.NewRecorder()
	e.leaseH.OwnerReleaseStalePickup(rr, returnReq(t, driver, youngLease, `{}`))
	if rr.Code != http.StatusForbidden && rr.Code != http.StatusConflict {
		t.Fatalf("driver calling owner-release: %d %s, want 403/409", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	e.leaseH.OwnerReleaseStalePickup(rr, returnReq(t, owner, youngLease, `{}`))
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "RELEASE_NOT_ALLOWED" {
		t.Fatalf("owner releasing a 1-hour-old rental: %d %s, want 409 RELEASE_NOT_ALLOWED", rr.Code, rr.Body.String())
	}
}

// The weekly option must be offered to exactly the people who can complete a
// weekly booking — the CTA's answer and the creation call's answer are the
// same predicate, so one can never show what the other refuses.
func TestRollingAllowlistGovernsBothTheOfferAndTheBooking(t *testing.T) {
	e := newPayoutEnv(t)
	pilot, stranger := uuid.New(), uuid.New()

	// Flag off: nobody, list or no list.
	e.leaseH.SetBillingDependencies(repository.NewBillingRepository(e.db), payoutTestFeeBPS, false)
	e.leaseH.SetRollingAllowlist([]uuid.UUID{pilot})
	if e.leaseH.RollingOpenFor(pilot) {
		t.Error("flag off but the pilot was offered weekly rentals")
	}

	// Flag on + empty list = open to everyone (the pre-pilot meaning).
	e.leaseH.SetBillingDependencies(repository.NewBillingRepository(e.db), payoutTestFeeBPS, true)
	e.leaseH.SetRollingAllowlist(nil)
	if !e.leaseH.RollingOpenFor(stranger) {
		t.Error("flag on with no allowlist must stay open to everyone")
	}

	// Flag on + a list = only the list.
	e.leaseH.SetRollingAllowlist([]uuid.UUID{pilot})
	if !e.leaseH.RollingOpenFor(pilot) {
		t.Error("the pilot is not offered weekly rentals")
	}
	if e.leaseH.RollingOpenFor(stranger) {
		t.Error("a driver outside the pilot was offered weekly rentals — the allowlist is not holding")
	}
}

// A seller cannot accept an offer on a car with no title on file. The refusal
// belongs here, not after the keys have changed hands.
func TestSellerMustHaveTheTitleOnFileBeforeAccepting(t *testing.T) {
	e := newSalesGateEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "tt_s_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "tt_b_"+run+"@example.com")
	e.seedLicense(t, buyer)
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE users SET stripe_account_id='acct_title_'||$1::text, payout_status='ready' WHERE id=$1`, seller); err != nil {
		t.Fatalf("ready seller: %v", err)
	}
	car := e.seedCar(t, seller, "available", true, false)
	e.listForSale(t, car, 9000)
	e.purchaseH.SetSalesDisabled(false)
	rr := httptest.NewRecorder()
	e.purchaseH.Create(rr, purchaseCreateReq(t, buyer, car, `{"offer_amount_cents": 5000}`))
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create offer: %d %s", rr.Code, rr.Body.String())
	}
	var pid uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `SELECT id FROM purchase_requests WHERE car_id=$1`, car).Scan(&pid); err != nil {
		t.Fatalf("offer id: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_purchase_request_id=NULL WHERE id=$1`, car)
		e.db.Pool.Exec(ctx, `DELETE FROM purchase_bill_of_sales WHERE purchase_request_id=$1`, pid)
		e.db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id=$1`, pid)
	})

	// No title on file -> refused at ACCEPT, before anyone commits anything.
	rr = httptest.NewRecorder()
	e.purchaseH.Accept(rr, purchaseReq(t, seller, pid, "accept", `{}`))
	if rr.Code != http.StatusConflict || errCodeOf(t, rr) != "TITLE_REQUIRED" {
		t.Fatalf("accept with no title: %d %s, want 409 TITLE_REQUIRED", rr.Code, rr.Body.String())
	}
	var st string
	_ = e.db.Pool.QueryRow(ctx, `SELECT status FROM purchase_requests WHERE id=$1`, pid).Scan(&st)
	if st != "requested" {
		t.Errorf("a refused accept advanced the sale to %s", st)
	}

	// With the title on file the accept goes through.
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO car_documents (car_id, document_type, file_url, file_path, file_name, mime_type, file_size)
		VALUES ($1, 'title', '/uploads/cars/title.pdf', '/uploads/cars/title.pdf', 'title.pdf', 'application/pdf', 1024)`, car); err != nil {
		t.Fatalf("seed title doc: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM car_documents WHERE car_id=$1`, car) })
	rr = httptest.NewRecorder()
	e.purchaseH.Accept(rr, purchaseReq(t, seller, pid, "accept", `{}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("accept with a title on file: %d %s, want 200", rr.Code, rr.Body.String())
	}
}

// The bug the pre-flight review reproduced on its own scratch DB: the floor
// used to be measured on the REQUEST's age, so a request made last week and
// paid thirty seconds ago was both offered to the owner as releasable and
// claimable — refunding a driver who was on their way to collect the car.
func TestOwnerReleaseFloorIsOnThePaymentNotTheRequest(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "clk_o_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "clk_d_"+run+"@example.com")
	e.seedLicense(t, driver)

	// Request is 5 days old; the money is 30 seconds old.
	leaseID, carID := e.stalePaidLease(t, owner, driver, 5*24*time.Hour)
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE payments SET created_at = NOW() - interval '30 seconds', updated_at = NOW() - interval '30 seconds'
		WHERE lease_request_id = $1`, leaseID); err != nil {
		t.Fatalf("age the payment: %v", err)
	}

	holds, err := e.leaseRepo.ListStalePickupHolds(ctx, &owner, models.LeaseOwnerReleaseMinAge, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, h := range holds {
		if h.LeaseRequestID == leaseID {
			t.Error("a rental paid 30 seconds ago was OFFERED to the owner as releasable")
		}
	}
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, leaseID, owner, models.LeaseOwnerReleaseMinAge); err == nil {
		t.Fatal("CLAIMED a rental paid 30 seconds ago — a paying driver's booking would have been cancelled and refunded")
	}
	if got := e.carReservedBy(t, carID); got == nil || *got != leaseID {
		t.Error("the refused release touched the reservation anyway")
	}

	// Age the money past the floor and it becomes releasable.
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE payments SET created_at = NOW() - interval '30 days' WHERE lease_request_id = $1`, leaseID); err != nil {
		t.Fatalf("re-age: %v", err)
	}
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, leaseID, owner, models.LeaseOwnerReleaseMinAge); err != nil {
		t.Fatalf("a 30-day-old paid, never-collected rental should be releasable: %v", err)
	}
}

// The executor may never reach further than the list the owner was shown: a
// lease that holds no car has nothing to release, and releasing it would fire
// a full refund for no stated reason.
func TestOwnerReleaseRefusesALeaseHoldingNoCar(t *testing.T) {
	e := newPayoutEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "nocar_o_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "nocar_d_"+run+"@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.stalePaidLease(t, owner, driver, 200*24*time.Hour)

	// Someone else's reservation now holds the car (or it was cleared).
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE cars SET reserved_by_lease_request_id = NULL WHERE id = $1`, carID); err != nil {
		t.Fatalf("clear reservation: %v", err)
	}
	holds, _ := e.leaseRepo.ListStalePickupHolds(ctx, &owner, models.LeaseOwnerReleaseMinAge, 100)
	for _, h := range holds {
		if h.LeaseRequestID == leaseID {
			t.Error("a lease holding no car is listed as a stale hold")
		}
	}
	if _, err := e.leaseRepo.ClaimForOwnerRelease(ctx, leaseID, owner, models.LeaseOwnerReleaseMinAge); err == nil {
		t.Fatal("released — and refunded — a lease that was holding no car at all")
	}
}

// /me must be scoped in SQL: another owner's holds can never come back, and
// this owner's hold cannot be pushed out of the window by other people's.
func TestStaleHoldsAreScopedToTheCallerInSQL(t *testing.T) {
	e := newPayoutEnv(t)
	run := uuid.NewString()[:8]
	mine := e.seedUser(t, "car_owner", "sc_a_"+run+"@example.com")
	theirs := e.seedUser(t, "car_owner", "sc_b_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "sc_d_"+run+"@example.com")
	e.seedLicense(t, driver)
	// Theirs is older, so a global ORDER BY created_at + LIMIT 1 would return
	// only their row and hide mine.
	theirLease, _ := e.stalePaidLease(t, theirs, driver, 300*24*time.Hour)
	myLease, _ := e.stalePaidLease(t, mine, driver, 100*24*time.Hour)

	got, err := e.leaseRepo.ListStalePickupHolds(context.Background(), &mine, models.LeaseOwnerReleaseMinAge, 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].LeaseRequestID != myLease {
		t.Fatalf("scoped list returned %+v, want exactly my own hold %s", got, myLease)
	}
	for _, h := range got {
		if h.OwnerID != mine || h.LeaseRequestID == theirLease {
			t.Error("another owner's hold leaked into a /me response")
		}
	}
}
