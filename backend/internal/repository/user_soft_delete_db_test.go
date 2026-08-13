package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return id
}

// DB-gated proof of the soft-delete contract (batch item 3): the tombstone
// must FREE the email and phone (the client's actual pain — "identifier is
// taken, they can't re-register"), block the account, and keep the row.
//
// Run with:
//
//	TEST_DATABASE_URL="postgres://…/drivebai_verify?sslmode=disable" \
//	  go test ./internal/repository/ -run TestSoftDeleteUser -v

func softDeleteTestDB(t *testing.T) *database.DB {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run soft-delete tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestSoftDeleteUser_FreesIdentifiersAndBlocks(t *testing.T) {
	db := softDeleteTestDB(t)
	ctx := context.Background()
	admin := NewAdminRepository(db)
	users := NewUserRepository(db)

	email := "tombstone_test@example.com"
	phone := "+15559871234"

	var userID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO users (email, role, first_name, last_name, phone, password_hash)
		VALUES ($1, 'driver', 'Tomb', 'Stone', $2, 'x') RETURNING id`,
		email, phone).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	uid := mustUUID(t, userID)
	t.Cleanup(func() {
		db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1 OR email = $2`, uid, email)
	})

	if err := admin.SoftDeleteUser(ctx, uid); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	// Row survives, anonymized, blocked, stamped.
	var gotEmail, first, last string
	var gotPhone *string
	var blocked bool
	var deletedAt *string
	if err := db.Pool.QueryRow(ctx, `
		SELECT email, first_name, last_name, phone, is_blocked, deleted_at::text
		FROM users WHERE id = $1`, uid).Scan(&gotEmail, &first, &last, &gotPhone, &blocked, &deletedAt); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !strings.HasPrefix(gotEmail, "deleted+") || !strings.HasSuffix(gotEmail, "@deleted.drivebai.com") {
		t.Errorf("email must be tombstoned, got %q", gotEmail)
	}
	if gotPhone != nil {
		t.Errorf("phone must be NULL, got %v", *gotPhone)
	}
	if first != "Deleted" || last != "User" {
		t.Errorf("name must be anonymized, got %q %q", first, last)
	}
	if !blocked {
		t.Error("tombstone must be blocked")
	}
	if deletedAt == nil {
		t.Error("deleted_at must be stamped")
	}

	// THE point: both identifiers are free again.
	if exists, err := users.EmailExists(ctx, email); err != nil || exists {
		t.Errorf("email must be free after delete (exists=%v err=%v)", exists, err)
	}
	if exists, err := users.PhoneExists(ctx, phone); err != nil || exists {
		t.Errorf("phone must be free after delete (exists=%v err=%v)", exists, err)
	}

	// Re-registration with the freed email actually works.
	fresh := &models.User{Email: email, Role: models.RoleDriver, FirstName: "Fresh", LastName: "Start"}
	if err := users.Create(ctx, fresh); err != nil {
		t.Fatalf("re-register with freed email: %v", err)
	}
	db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fresh.ID)

	// Idempotence: second delete reports not-found.
	if err := admin.SoftDeleteUser(ctx, uid); err == nil {
		t.Error("second soft delete must report already-deleted")
	}
}
