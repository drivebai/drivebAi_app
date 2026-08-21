package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// DB-gated tests for the rental-lifecycle batch mechanics. Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/repository/ -run TestRentalLifecycle -v
//
// The database must be migrated through 000048.

func lifecycleTestDB(t *testing.T) *database.DB {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run lifecycle tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func seedLifecycleUser(t *testing.T, db *database.DB, role, email string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Pool.Exec(context.Background(), `
		INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
		VALUES ($1, $2, 'x', $3, 'Life', 'Cycle', TRUE, 'created')`, id, email, role)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func seedLifecycleCar(t *testing.T, db *database.DB, ownerID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.Pool.Exec(context.Background(), `
		INSERT INTO cars (
			id, owner_id, title, description, make, model, year, body_type, fuel_type, mileage,
			is_for_rent, weekly_rent_price, is_for_sale, currency,
			status, is_paused, is_approved, rented_weeks, total_earned, created_at, updated_at
		) VALUES ($1, $2, 'Lifecycle Test Car', '', 'Test', $4, 2024, 'sedan', 'gas', 1000,
		          TRUE, 300, FALSE, 'USD', $3, FALSE, TRUE, 0, 0, NOW(), NOW())`, id, ownerID, status, "C-"+id.String()[:8])
	if err != nil {
		t.Fatalf("seed car: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(context.Background(), `DELETE FROM cars WHERE id = $1`, id)
	})
	return id
}

func seedLifecycleLease(t *testing.T, db *database.DB, carID, ownerID, driverID uuid.UUID, status string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	chatID := uuid.New()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO chats (id, car_id, driver_id, owner_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (car_id, driver_id, owner_id) DO NOTHING`, chatID, carID, driverID, ownerID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		SELECT id FROM chats WHERE car_id = $1 AND driver_id = $2 AND owner_id = $3`,
		carID, driverID, ownerID).Scan(&chatID); err != nil {
		t.Fatalf("resolve chat: %v", err)
	}

	id := uuid.New()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO lease_requests (id, chat_id, listing_id, owner_id, driver_id, status, weekly_price, currency, weeks, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 300, 'USD', 1, NOW() + INTERVAL '24 hours', NOW(), NOW())`,
		id, chatID, carID, ownerID, driverID, status); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_lease_request_id = NULL WHERE reserved_by_lease_request_id = $1`, id)
		db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE id = $1`, id)
		db.Pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
	})
	return id
}

// Accept must refuse a car occupied through any door: another lease's
// reservation, a purchase reservation, rented or sold status.
func TestRentalLifecycle_AcceptGuards(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewLeaseRequestRepository(db)

	owner := seedLifecycleUser(t, db, "car_owner", "lc_owner_accept@example.com")
	driver := seedLifecycleUser(t, db, "driver", "lc_driver_accept@example.com")

	// Happy path: available car → accept reserves it.
	car := seedLifecycleCar(t, db, owner, "available")
	lease := seedLifecycleLease(t, db, car, owner, driver, "requested")
	if _, err := repo.AcceptLeaseRequest(ctx, lease, owner); err != nil {
		t.Fatalf("accept on available car must succeed: %v", err)
	}
	var reservedBy *uuid.UUID
	db.Pool.QueryRow(ctx, `SELECT reserved_by_lease_request_id FROM cars WHERE id = $1`, car).Scan(&reservedBy)
	if reservedBy == nil || *reservedBy != lease {
		t.Fatalf("car not reserved by accepted lease")
	}

	// Sold car → CAR_NOT_AVAILABLE.
	soldCar := seedLifecycleCar(t, db, owner, "sold")
	soldLease := seedLifecycleLease(t, db, soldCar, owner, driver, "requested")
	if _, err := repo.AcceptLeaseRequest(ctx, soldLease, owner); models.GetAPIError(err) != models.ErrCarNotAvailable {
		t.Errorf("accept on sold car: want ErrCarNotAvailable, got %v", err)
	}

	// Purchase-reserved car → CAR_NOT_AVAILABLE.
	prCar := seedLifecycleCar(t, db, owner, "available")
	if _, err := db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_purchase_request_id = $1 WHERE id = $2`, uuid.New(), prCar); err != nil {
		t.Skipf("cannot fake purchase reservation (FK): %v", err)
	}
	prLease := seedLifecycleLease(t, db, prCar, owner, driver, "requested")
	if _, err := repo.AcceptLeaseRequest(ctx, prLease, owner); models.GetAPIError(err) != models.ErrCarNotAvailable {
		t.Errorf("accept on purchase-reserved car: want ErrCarNotAvailable, got %v", err)
	}
}

// ConfirmPickup must stamp rental_ends_at = pickup + weeks*7d in the same
// transaction that flips the car to rented.
func TestRentalLifecycle_ConfirmPickupStampsTermEnd(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewLeaseRequestRepository(db)

	owner := seedLifecycleUser(t, db, "car_owner", "lc_owner_pickup@example.com")
	driver := seedLifecycleUser(t, db, "driver", "lc_driver_pickup@example.com")
	car := seedLifecycleCar(t, db, owner, "available")
	lease := seedLifecycleLease(t, db, car, owner, driver, "requested")

	if _, err := repo.AcceptLeaseRequest(ctx, lease, owner); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := repo.SetPaid(ctx, lease); err != nil {
		t.Fatalf("set paid: %v", err)
	}
	lr, err := repo.ConfirmPickup(ctx, lease, driver)
	if err != nil {
		t.Fatalf("confirm pickup: %v", err)
	}

	var endsAt *time.Time
	var carStatus string
	db.Pool.QueryRow(ctx, `SELECT rental_ends_at FROM lease_requests WHERE id = $1`, lease).Scan(&endsAt)
	db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id = $1`, car).Scan(&carStatus)
	if endsAt == nil {
		t.Fatal("rental_ends_at not stamped")
	}
	want := lr.PickupConfirmedAt.AddDate(0, 0, 7) // weeks=1
	if !endsAt.Equal(want) {
		t.Errorf("rental_ends_at = %v, want %v", endsAt, want)
	}
	if carStatus != "rented" {
		t.Errorf("car status = %s, want rented", carStatus)
	}
}

// Term claims: overdue rows are claimed exactly once (idempotent re-run),
// rows exactly on the tick are claimed (inclusive end), not-yet-ended rows
// are untouched.
func TestRentalLifecycle_TermClaimsIdempotent(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewLeaseRequestRepository(db)

	owner := seedLifecycleUser(t, db, "car_owner", "lc_owner_term@example.com")
	driver := seedLifecycleUser(t, db, "driver", "lc_driver_term@example.com")

	mk := func(email string, endOffset time.Duration) uuid.UUID {
		car := seedLifecycleCar(t, db, owner, "rented")
		lease := seedLifecycleLease(t, db, car, owner, driver, "paid")
		if _, err := db.Pool.Exec(ctx, `
			UPDATE lease_requests
			SET pickup_confirmed_at = NOW() - INTERVAL '7 days',
			    rental_ends_at = $2
			WHERE id = $1`, lease, time.Now().UTC().Add(endOffset)); err != nil {
			t.Fatalf("arm lease: %v", err)
		}
		_ = email
		return lease
	}

	now := time.Now().UTC()
	overdueLease := mk("a", -1*time.Hour)    // past the end → overdue
	exactLease := mk("b", 0)                 // exactly on the tick → overdue (inclusive)
	activeLease := mk("c", 72*time.Hour)     // far future → untouched
	endingSoonLease := mk("d", 12*time.Hour) // inside T-24h → ending soon

	// A lease with an IN-FLIGHT return must be invisible to every phase —
	// nagging "the car isn't marked returned" at a driver who already
	// tapped the button is the false-copy disease this batch cures.
	returningLease := mk("e", -1*time.Hour)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO vehicle_returns
			(id, lease_request_id, car_id, owner_id, driver_id, status,
			 driver_initiated_at, pickup_confirmed_at, returned_at, rental_weeks, paid_amount_cents,
			 used_days, refund_amount_cents, created_at, updated_at)
		SELECT gen_random_uuid(), lr.id, lr.listing_id, lr.owner_id, lr.driver_id, 'driver_initiated',
		       NOW(), lr.pickup_confirmed_at, NOW(), lr.weeks, 0, 7, 0, NOW(), NOW()
		FROM lease_requests lr WHERE lr.id = $1`, returningLease); err != nil {
		t.Fatalf("seed in-flight return: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id = $1`, returningLease)
	})

	claimed, err := repo.ClaimTermOverdue(ctx, now.Add(time.Second), 50)
	if err != nil {
		t.Fatalf("claim overdue: %v", err)
	}
	got := map[uuid.UUID]bool{}
	for _, c := range claimed {
		got[c.ID] = true
	}
	if !got[overdueLease] || !got[exactLease] {
		t.Errorf("overdue claim missed rows: got %v", got)
	}
	if got[activeLease] || got[endingSoonLease] {
		t.Errorf("overdue claim touched non-overdue rows: got %v", got)
	}
	if got[returningLease] {
		t.Errorf("overdue claim must skip a lease with an in-flight return")
	}

	// Second run: no duplicate side effects.
	again, err := repo.ClaimTermOverdue(ctx, now.Add(time.Minute), 50)
	if err != nil {
		t.Fatalf("re-claim overdue: %v", err)
	}
	for _, c := range again {
		if c.ID == overdueLease || c.ID == exactLease {
			t.Errorf("lease %s claimed twice", c.ID)
		}
	}

	// Ending-soon picks only the T-24h row.
	soon, err := repo.ClaimTermEndingSoon(ctx, now, 50)
	if err != nil {
		t.Fatalf("claim ending soon: %v", err)
	}
	soonSet := map[uuid.UUID]bool{}
	for _, c := range soon {
		soonSet[c.ID] = true
	}
	if !soonSet[endingSoonLease] {
		t.Errorf("ending-soon claim missed the T-12h row")
	}
	if soonSet[activeLease] {
		t.Errorf("ending-soon claim touched the far-future row")
	}
}

// Sibling auto-decline: paid lease declines only 'requested' siblings on the
// same listing; a second run is a no-op.
func TestRentalLifecycle_SiblingDecline(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	repo := NewLeaseRequestRepository(db)

	owner := seedLifecycleUser(t, db, "car_owner", "lc_owner_sib@example.com")
	winner := seedLifecycleUser(t, db, "driver", "lc_driver_win@example.com")
	loser := seedLifecycleUser(t, db, "driver", "lc_driver_lose@example.com")

	car := seedLifecycleCar(t, db, owner, "available")
	paidLease := seedLifecycleLease(t, db, car, owner, winner, "paid")
	siblingReq := seedLifecycleLease(t, db, car, owner, loser, "requested")
	siblingDone := seedLifecycleLease(t, db, car, owner, loser, "declined")

	declined, err := repo.DeclineSiblingRequests(ctx, car, paidLease)
	if err != nil {
		t.Fatalf("decline siblings: %v", err)
	}
	if len(declined) != 1 || declined[0].ID != siblingReq {
		t.Fatalf("want exactly the requested sibling declined, got %+v", declined)
	}

	var status string
	db.Pool.QueryRow(ctx, `SELECT status FROM lease_requests WHERE id = $1`, siblingReq).Scan(&status)
	if status != "declined" {
		t.Errorf("sibling status = %s, want declined", status)
	}
	db.Pool.QueryRow(ctx, `SELECT status FROM lease_requests WHERE id = $1`, paidLease).Scan(&status)
	if status != "paid" {
		t.Errorf("paid lease must be untouched, got %s", status)
	}
	_ = siblingDone

	// Idempotent: nothing left to decline.
	declined2, err := repo.DeclineSiblingRequests(ctx, car, paidLease)
	if err != nil {
		t.Fatalf("re-decline: %v", err)
	}
	if len(declined2) != 0 {
		t.Errorf("second run must be a no-op, declined %d", len(declined2))
	}
}

// System tickets: one per entity, enforced by the partial unique indexes —
// the second create returns (nil, nil) instead of a duplicate row.
func TestRentalLifecycle_SystemTicketIdempotent(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	tickets := NewTicketRepository(db)

	owner := seedLifecycleUser(t, db, "car_owner", "lc_owner_ticket@example.com")
	driver := seedLifecycleUser(t, db, "driver", "lc_driver_ticket@example.com")
	car := seedLifecycleCar(t, db, owner, "rented")
	lease := seedLifecycleLease(t, db, car, owner, driver, "paid")

	t.Cleanup(func() {
		db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE lease_request_id = $1`, lease)
	})

	first, err := tickets.CreateSystemTicket(ctx, owner, models.TicketCategoryRenting,
		"Overdue rental — test", "test body", &lease, nil)
	if err != nil {
		t.Fatalf("create system ticket: %v", err)
	}
	if first == nil || first.Status != models.TicketStatusOpen || first.SubmittedAt == nil {
		t.Fatalf("system ticket must be open+submitted, got %+v", first)
	}

	second, err := tickets.CreateSystemTicket(ctx, owner, models.TicketCategoryRenting,
		"Overdue rental — test", "test body", &lease, nil)
	if err != nil {
		t.Fatalf("second create must not error: %v", err)
	}
	if second != nil {
		t.Errorf("second create must be an idempotent no-op, got a row")
	}

	var count int
	db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets WHERE lease_request_id = $1`, lease).Scan(&count)
	if count != 1 {
		t.Errorf("want exactly 1 ticket for the lease, got %d", count)
	}
}

// IsOccupied must see both reservation kinds and read false for a free car.
func TestRentalLifecycle_IsOccupied(t *testing.T) {
	db := lifecycleTestDB(t)
	ctx := context.Background()
	cars := NewCarRepository(db)

	owner := seedLifecycleUser(t, db, "car_owner", "lc_owner_occ@example.com")
	driver := seedLifecycleUser(t, db, "driver", "lc_driver_occ@example.com")

	free := seedLifecycleCar(t, db, owner, "available")
	if occ, err := cars.IsOccupied(ctx, free); err != nil || occ {
		t.Errorf("free car: occ=%v err=%v", occ, err)
	}

	held := seedLifecycleCar(t, db, owner, "available")
	lease := seedLifecycleLease(t, db, held, owner, driver, "accepted")
	if _, err := db.Pool.Exec(ctx, `UPDATE cars SET reserved_by_lease_request_id = $1 WHERE id = $2`, lease, held); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if occ, err := cars.IsOccupied(ctx, held); err != nil || !occ {
		t.Errorf("lease-reserved car: occ=%v err=%v", occ, err)
	}
}
