package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
)

// Plate/VIN search tests. DB-gated; the database must be migrated through
// 000046 (car_plate). Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/repository/ -run TestAdminCarSearch -v

func TestNormalizeVehicleKey(t *testing.T) {
	cases := map[string]string{
		"ABC-123":            "abc123",
		"abc 123":            "abc123",
		" A b C 1-2-3 ":      "abc123",
		"1HGCM82633A004352":  "1hgcm82633a004352",
		"!!!":                "",
		"":                   "",
		"Дplate":             "plate", // non-ASCII folds away, ASCII survives
	}
	for in, want := range cases {
		if got := normalizeVehicleKey(in); got != want {
			t.Errorf("normalizeVehicleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func carSearchTestDB(t *testing.T) *database.DB {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated ≥000046, disposable DB) to run car search tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestAdminCarSearch_PlateAndVIN(t *testing.T) {
	db := carSearchTestDB(t)
	ctx := context.Background()
	repo := NewAdminRepository(db)

	ownerID := uuid.New()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
		VALUES ($1, $2, 'x', 'car_owner', 'Search', 'Owner', TRUE, 'created')`,
		ownerID, "carsearch_owner@example.com"); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	t.Cleanup(func() { db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, ownerID) })

	seedCar := func(title, vin, plate string) uuid.UUID {
		id := uuid.New()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO cars (
				id, owner_id, title, description, make, model, year, body_type, fuel_type, mileage,
				is_for_rent, weekly_rent_price, is_for_sale, currency,
				status, is_paused, is_approved, rented_weeks, total_earned,
				vin, plate, created_at, updated_at
			) VALUES ($1, $2, $3, '', 'SearchMake', $4, 2024, 'sedan', 'gas', 1000,
			          TRUE, 300, FALSE, 'USD', 'available', FALSE, TRUE, 0, 0,
			          NULLIF($5,''), NULLIF($6,''), NOW(), NOW())`,
			id, ownerID, title, "M-"+id.String()[:8], vin, plate); err != nil {
			t.Fatalf("seed car %s: %v", title, err)
		}
		t.Cleanup(func() { db.Pool.Exec(ctx, `DELETE FROM cars WHERE id = $1`, id) })
		return id
	}

	withVIN := seedCar("Search Car VIN", "1HGCM82633A004352", "")
	withPlate := seedCar("Search Car Plate", "", "ABC-1234")
	bare := seedCar("Search Car Bare", "", "")

	find := func(query string) map[uuid.UUID]bool {
		t.Helper()
		page, err := repo.ListCars(ctx, query, 1, 200)
		if err != nil {
			t.Fatalf("ListCars(%q): %v", query, err)
		}
		got := map[uuid.UUID]bool{}
		for _, c := range page.Items {
			got[c.ID] = true
		}
		return got
	}

	// Exact plate, stored with a hyphen, queried in three spellings.
	for _, q := range []string{"ABC-1234", "abc 1234", "ABC1234"} {
		got := find(q)
		if !got[withPlate] {
			t.Errorf("plate query %q missed the plated car", q)
		}
		if got[withVIN] || got[bare] {
			t.Errorf("plate query %q matched unrelated cars", q)
		}
	}

	// Partial VIN — the last 8 and last 6 characters, mixed case.
	for _, q := range []string{"3A004352", "a004352", "004352"} {
		got := find(q)
		if !got[withVIN] {
			t.Errorf("partial VIN query %q missed the VIN car", q)
		}
		if got[withPlate] {
			t.Errorf("partial VIN query %q matched the plated car", q)
		}
	}

	// Full VIN with separators the admin pasted in.
	if got := find("1HGCM826-33A 004352"); !got[withVIN] {
		t.Error("full VIN with separators missed the VIN car")
	}

	// No match.
	if got := find("ZZZ9999999"); got[withVIN] || got[withPlate] || got[bare] {
		t.Error("no-match query returned rows")
	}

	// Existing behaviour unbroken: title search still works, and an
	// all-punctuation query must not match everything.
	if got := find("Search Car Bare"); !got[bare] {
		t.Error("title search regressed")
	}
	if got := find("!!!"); got[withVIN] || got[withPlate] || got[bare] {
		t.Error("all-punctuation query must not match via the key predicate")
	}
}
