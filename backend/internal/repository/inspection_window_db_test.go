package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
)

// The inspection window. Until migration 000059 the deadline was written,
// returned to clients and read by nothing, so the only exit was the 7-day
// Stripe hold lapsing after the buyer already had the keys.
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/repository/ -run TestInspectionWindow -v
func inspectionEnv(t *testing.T) (*database.DB, *PurchaseRequestRepository, func(deadline time.Time) uuid.UUID, func()) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated >=000059) to run inspection window tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx := context.Background()
	sellerID, buyerID, carID, chatID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []struct {
		id   uuid.UUID
		role string
	}{{sellerID, "car_owner"}, {buyerID, "driver"}} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
			VALUES ($1, $3, 'x', $2, 'I', 'W', TRUE, 'created')`, u.id, u.role, "insp_"+u.id.String()+"@example.com"); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO cars (id, owner_id, make, model, year, title, weekly_rent_price, sale_price, is_for_sale, currency, status)
		VALUES ($1,$2,'Toyota','Camry',2020,'Inspection Car',300,900000,TRUE,'USD','available')`, carID, sellerID); err != nil {
		t.Fatalf("seed car: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO chats (id, car_id, driver_id, owner_id) VALUES ($1,$2,$3,$4)`,
		chatID, carID, buyerID, sellerID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	seed := func(deadline time.Time) uuid.UUID {
		var id uuid.UUID
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO purchase_requests
				(car_id, seller_id, buyer_id, chat_id, offer_amount_cents, currency, status, expires_at,
				 keys_handed_over_at, inspection_deadline_at, payment_intent_id, payment_status)
			VALUES ($1,$2,$3,$4, 900000, 'USD', 'awaiting_inspection', NOW() + INTERVAL '30 days',
				 NOW() - INTERVAL '2 days', $5, 'pi_insp_'||gen_random_uuid()::text, 'requires_capture')
			RETURNING id`, carID, sellerID, buyerID, chatID, deadline).Scan(&id); err != nil {
			t.Fatalf("seed purchase: %v", err)
		}
		return id
	}
	cleanup := func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE chat_id=$1`, chatID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM chats WHERE id=$1`, chatID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM cars WHERE id=$1`, carID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, sellerID, buyerID)
		db.Close()
	}
	return db, NewPurchaseRequestRepository(db), seed, cleanup
}

func TestInspectionWindow_ExpiryAutoAcceptsOnceOnly(t *testing.T) {
	_, repo, seed, cleanup := inspectionEnv(t)
	defer cleanup()
	ctx := context.Background()

	id := seed(time.Now().UTC().Add(-1 * time.Hour)) // deadline already passed

	listed, err := repo.ListInspectionExpired(ctx, time.Now().UTC(), 50)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	var found bool
	for _, p := range listed {
		if p.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("an expired inspection window was not listed — the sweep would never see it")
	}

	claimed, err := repo.ClaimInspectionAutoAccept(ctx, id)
	if err != nil || claimed == nil {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	if string(claimed.Status) != "inspection_accepted" {
		t.Errorf("status = %s, want inspection_accepted", claimed.Status)
	}
	if claimed.InspectionAutoAcceptedAt == nil {
		t.Error("auto-acceptance was not recorded — 'who accepted this sale' must stay answerable")
	}

	// A second sweep (or a second instance) must not claim it again.
	twice, err := repo.ClaimInspectionAutoAccept(ctx, id)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if twice != nil {
		t.Error("the window was claimed twice — a sale could be captured twice")
	}
}

func TestInspectionWindow_BuyerActionBeatsTheSweep(t *testing.T) {
	_, repo, seed, cleanup := inspectionEnv(t)
	defer cleanup()
	ctx := context.Background()
	id := seed(time.Now().UTC().Add(-1 * time.Hour))
	// The buyer rejects in the same second the sweep runs.
	if _, err := repo.db.Pool.Exec(ctx,
		`UPDATE purchase_requests SET status='inspection_rejected' WHERE id=$1`, id); err != nil {
		t.Fatalf("buyer rejects: %v", err)
	}
	claimed, err := repo.ClaimInspectionAutoAccept(ctx, id)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed != nil {
		t.Error("the sweep overrode a buyer who had already rejected")
	}
}

func TestInspectionWindow_WarningsClaimedOnce(t *testing.T) {
	_, repo, seed, cleanup := inspectionEnv(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()

	// Deadline 90 minutes out: inside the 2h warning, and inside 24h too.
	id := seed(now.Add(90 * time.Minute))

	first, err := repo.ClaimInspectionWarning(ctx, "inspection_warned_2h_at", 2*time.Hour, now, 50)
	if err != nil {
		t.Fatalf("claim 2h: %v", err)
	}
	var got bool
	for _, p := range first {
		if p.ID == id {
			got = true
		}
	}
	if !got {
		t.Fatal("the 2-hour warning was not claimed for a row inside the window")
	}
	second, err := repo.ClaimInspectionWarning(ctx, "inspection_warned_2h_at", 2*time.Hour, now, 50)
	if err != nil {
		t.Fatalf("re-claim 2h: %v", err)
	}
	for _, p := range second {
		if p.ID == id {
			t.Error("the same warning was sent twice")
		}
	}

	// A row already past its deadline must not be warned — no "2 hours left"
	// in the same tick that completes the sale. Its own car and buyer: only
	// one active purchase per car is allowed.
	_, repo2, seed2, cleanup2 := inspectionEnv(t)
	defer cleanup2()
	past := seed2(now.Add(-30 * time.Minute))
	_ = repo2
	late, err := repo.ClaimInspectionWarning(ctx, "inspection_warned_24h_at", 24*time.Hour, now, 50)
	if err != nil {
		t.Fatalf("claim 24h: %v", err)
	}
	for _, p := range late {
		if p.ID == past {
			t.Error("warned a purchase whose window had already closed")
		}
	}
}

func TestInspectionWindow_RejectsUnknownColumn(t *testing.T) {
	_, repo, _, cleanup := inspectionEnv(t)
	defer cleanup()
	// The column name is interpolated into SQL; only the two stamps are legal.
	if _, err := repo.ClaimInspectionWarning(context.Background(), "status", time.Hour, time.Now(), 10); err == nil {
		t.Error("an arbitrary column name was accepted into an interpolated UPDATE")
	}
}
