package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// DB-gated tests for the C4 any-buyer reservation guard. They exercise the
// REAL predicate against a migrated Postgres so the state matrix — which
// statuses block a second buyer and, critically, which ones RELEASE the car
// when a purchase falls through — is proven, not assumed.
//
// Run with:
//
//	TEST_DATABASE_URL="postgres://…/drivebai_verify?sslmode=disable" \
//	  go test ./internal/repository/ -run TestPurchaseReservation -v

func reservationTestDB(t *testing.T) *database.DB {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run reservation-guard tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// seedPurchaseFixture creates seller + buyer + car + chat + one purchase row
// (status 'requested') and returns the ids. Cleanup cascades from the users.
func seedPurchaseFixture(t *testing.T, db *database.DB) (carID, purchaseID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	var sellerID, buyerID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users (email, role, first_name, last_name)
		VALUES ($1,'car_owner','Res','Seller') RETURNING id`, "res_seller_"+suffix+"@example.com").Scan(&sellerID); err != nil {
		t.Fatalf("seed seller: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users (email, role, first_name, last_name)
		VALUES ($1,'driver','Res','Buyer') RETURNING id`, "res_buyer_"+suffix+"@example.com").Scan(&buyerID); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}
	t.Cleanup(func() {
		// purchase_requests FKs are RESTRICT — delete children first.
		db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE seller_id = $1`, sellerID)
		db.Pool.Exec(ctx, `DELETE FROM chats WHERE owner_id = $1`, sellerID)
		db.Pool.Exec(ctx, `DELETE FROM cars WHERE owner_id = $1`, sellerID)
		db.Pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{sellerID, buyerID})
	})

	if err := db.Pool.QueryRow(ctx, `INSERT INTO cars (owner_id, title, make, model, year, is_for_sale, sale_price, status, is_approved)
		VALUES ($1,'Res Test Car','Test','Car',2020,true,5000,'available',true) RETURNING id`, sellerID).Scan(&carID); err != nil {
		t.Fatalf("seed car: %v", err)
	}
	var chatID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `INSERT INTO chats (car_id, driver_id, owner_id)
		VALUES ($1,$2,$3) RETURNING id`, carID, buyerID, sellerID).Scan(&chatID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO purchase_requests (car_id, seller_id, buyer_id, chat_id, offer_amount_cents, expires_at)
		VALUES ($1,$2,$3,$4,500000,NOW() + interval '1 day') RETURNING id`, carID, sellerID, buyerID, chatID).Scan(&purchaseID); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	return carID, purchaseID
}

func setPurchaseStatus(t *testing.T, db *database.DB, id uuid.UUID, status string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`UPDATE purchase_requests SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
}

// TestPurchaseReservation_StateMatrix proves exactly which purchase states
// reserve the car against other buyers. `requested` must NOT block (sellers
// may field several offers); every terminal state must not block (that IS the
// release path — decline/cancel/expiry/refund free the car with no cleanup).
func TestPurchaseReservation_StateMatrix(t *testing.T) {
	db := reservationTestDB(t)
	repo := NewPurchaseRequestRepository(db)
	carID, purchaseID := seedPurchaseFixture(t, db)
	ctx := context.Background()

	cases := []struct {
		status string
		blocks bool
	}{
		{"requested", false}, // an offer alone must not lock the car
		{"accepted", true},
		{"bos_pending_seller", true},
		{"bos_pending_buyer", true},
		{"bos_signed", true},
		{"payment_authorized", true},
		{"handover_scheduled", true},
		{"awaiting_inspection", true},
		{"inspection_accepted", true},
		{"inspection_rejected", true}, // disputed — admin may uphold the sale
		// Terminal = released. These are the client's "car must free up if
		// A's purchase falls through" paths.
		{"declined", false},
		{"cancelled", false},
		{"expired", false},
		{"expired_auth", false},
		{"rejected_refunded", false},
		{"rejected_upheld", false}, // car is sold at this point anyway
		{"completed", false},       // car flips to 'sold'; sold-guard owns it
	}
	for _, tc := range cases {
		setPurchaseStatus(t, db, purchaseID, tc.status)
		blocked, err := repo.HasBlockingPurchase(ctx, carID, uuid.Nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.status, err)
		}
		if blocked != tc.blocks {
			t.Errorf("status %q: HasBlockingPurchase = %v, want %v", tc.status, blocked, tc.blocks)
		}
	}
}

// TestPurchaseReservation_ReleaseTransition walks the exact scenario from the
// review: buyer A's purchase is accepted (buyer B blocked), then falls
// through (declined) — buyer B must be unblocked with no cleanup step.
func TestPurchaseReservation_ReleaseTransition(t *testing.T) {
	db := reservationTestDB(t)
	repo := NewPurchaseRequestRepository(db)
	carID, purchaseID := seedPurchaseFixture(t, db)
	ctx := context.Background()

	setPurchaseStatus(t, db, purchaseID, "accepted")
	if blocked, _ := repo.HasBlockingPurchase(ctx, carID, uuid.Nil); !blocked {
		t.Fatal("accepted purchase must block other buyers")
	}
	setPurchaseStatus(t, db, purchaseID, "declined")
	if blocked, _ := repo.HasBlockingPurchase(ctx, carID, uuid.Nil); blocked {
		t.Fatal("declined purchase must release the car for other buyers")
	}
}

// TestPurchaseReservation_ExcludesOwnRow: Accept must not be blocked by the
// very row it is accepting.
func TestPurchaseReservation_ExcludesOwnRow(t *testing.T) {
	db := reservationTestDB(t)
	repo := NewPurchaseRequestRepository(db)
	carID, purchaseID := seedPurchaseFixture(t, db)
	ctx := context.Background()

	setPurchaseStatus(t, db, purchaseID, "accepted")
	if blocked, _ := repo.HasBlockingPurchase(ctx, carID, purchaseID); blocked {
		t.Fatal("excluding the row's own id must not report it as blocking")
	}
	if blocked, _ := repo.HasBlockingPurchase(ctx, carID, uuid.Nil); !blocked {
		t.Fatal("sanity: without exclusion the accepted row must block")
	}
}

// TestMarkPaymentFailed_RecordsAndNeverDowngrades: payment_failed webhook
// writes payment_status='failed' without touching the purchase status, and a
// late/duplicate failure event can never downgrade a succeeded payment.
func TestMarkPaymentFailed_RecordsAndNeverDowngrades(t *testing.T) {
	db := reservationTestDB(t)
	repo := NewPurchaseRequestRepository(db)
	_, purchaseID := seedPurchaseFixture(t, db)
	ctx := context.Background()

	intent := "pi_test_" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(ctx,
		`UPDATE purchase_requests SET status = 'bos_signed', payment_intent_id = $2 WHERE id = $1`,
		purchaseID, intent); err != nil {
		t.Fatalf("arm intent: %v", err)
	}

	p, err := repo.MarkPaymentFailed(ctx, intent)
	if err != nil || p == nil {
		t.Fatalf("MarkPaymentFailed: p=%v err=%v", p, err)
	}
	var payStatus, status string
	if err := db.Pool.QueryRow(ctx,
		`SELECT COALESCE(payment_status,''), status FROM purchase_requests WHERE id = $1`, purchaseID).
		Scan(&payStatus, &status); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if payStatus != "failed" {
		t.Errorf("payment_status = %q, want failed", payStatus)
	}
	if status != string(models.PurchaseStatusBOSSigned) {
		t.Errorf("purchase status = %q, want bos_signed untouched (buyer retries from the same CTA)", status)
	}

	// Never downgrade a success.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE purchase_requests SET payment_status = 'succeeded' WHERE id = $1`, purchaseID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	p2, err := repo.MarkPaymentFailed(ctx, intent)
	if err != nil {
		t.Fatalf("second MarkPaymentFailed: %v", err)
	}
	if p2 != nil {
		t.Error("late payment_failed after success must be a no-op (nil row)")
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT payment_status FROM purchase_requests WHERE id = $1`, purchaseID).Scan(&payStatus); err != nil {
		t.Fatalf("refetch2: %v", err)
	}
	if payStatus != "succeeded" {
		t.Errorf("payment_status downgraded to %q — must remain succeeded", payStatus)
	}
}
