package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// Regression guard for the build-35 prerequisite. ListForChat feeds the chat
// screen, and the chat card branches on billing_mode to decide whether paying
// must go through the weekly authorization screen. The column was missing from
// this query's SELECT list, so every rolling lease read as fixed_term and the
// driver could have been charged without ever seeing the mandate. This is the
// kind of bug a new column hits in exactly one of fourteen scan lists.
//
// Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/repository/ -run TestListForChatCarriesBillingMode -v
func TestListForChatCarriesBillingMode(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	repo := NewLeaseRequestRepository(db)

	ownerID, driverID, carID, chatID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []struct {
		id    uuid.UUID
		email string
		role  string
	}{{ownerID, "bm_owner_" + ownerID.String() + "@example.com", "car_owner"}, {driverID, "bm_driver_" + driverID.String() + "@example.com", "driver"}} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
			VALUES ($1, $2, 'x', $3, 'B', 'M', TRUE, 'created')`, u.id, u.email, u.role); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO cars (id, owner_id, make, model, year, title, weekly_rent_price, currency, status)
		VALUES ($1, $2, 'Toyota', 'Corolla', 2020, 'BM Test Car', 300, 'USD', 'available')`,
		carID, ownerID); err != nil {
		t.Fatalf("seed car: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO chats (id, car_id, driver_id, owner_id) VALUES ($1, $2, $3, $4)`,
		chatID, carID, driverID, ownerID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE chat_id = $1`, chatID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM cars WHERE id = $1`, carID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1, $2)`, ownerID, driverID)
	})

	for _, mode := range []models.LeaseBillingMode{models.BillingModeRolling, models.BillingModeFixedTerm} {
		// One active request per listing: retire the previous one first.
		if _, err := db.Pool.Exec(ctx, `UPDATE lease_requests SET status = 'cancelled' WHERE chat_id = $1`, chatID); err != nil {
			t.Fatalf("retire prior lease: %v", err)
		}
		lr, err := repo.CreateLeaseRequest(ctx, &models.LeaseRequest{
			ChatID: chatID, ListingID: carID, OwnerID: ownerID, DriverID: driverID,
			WeeklyPrice: 300, Currency: "USD", Weeks: 1, BillingMode: mode,
		})
		if err != nil {
			t.Fatalf("create %s lease: %v", mode, err)
		}
		list, lerr := repo.ListForChat(ctx, chatID)
		if lerr != nil {
			t.Fatalf("list: %v", lerr)
		}
		var found *models.LeaseRequestResponse
		for i := range list {
			if list[i].ID == lr.ID {
				found = &list[i]
			}
		}
		if found == nil {
			t.Fatalf("%s lease missing from ListForChat", mode)
		}
		if found.BillingMode != mode {
			t.Errorf("ListForChat billing_mode = %q, want %q — the chat card branches on this", found.BillingMode, mode)
		}
	}
}
