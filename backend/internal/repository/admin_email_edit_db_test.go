package repository

import (
	"context"
	"os"
	"testing"

	"github.com/drivebai/backend/internal/database"
)

// DB-gated tests for the admin email edit (client point 2): the
// excluding-self uniqueness semantics and the atomic verified-flag reset.
//
// Run with:
//
//	TEST_DATABASE_URL="postgres://…/drivebai_verify?sslmode=disable" \
//	  go test ./internal/repository/ -run TestAdminEmailEdit -v

func TestAdminEmailEdit_UniquenessAndVerifiedReset(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	ctx := context.Background()
	users := NewUserRepository(db)

	var aID, bID string
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO users (email, role, first_name, last_name, is_email_verified)
		VALUES ('email_edit_a@example.com', 'driver', 'A', 'A', TRUE) RETURNING id`).Scan(&aID); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `
		INSERT INTO users (email, role, first_name, last_name)
		VALUES ('email_edit_b@example.com', 'driver', 'B', 'B') RETURNING id`).Scan(&bID); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	a, b := mustUUID(t, aID), mustUUID(t, bID)
	t.Cleanup(func() {
		db.Pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1, $2)`, a, b)
	})

	// Round-tripping your own email must not read as a conflict…
	if taken, err := users.EmailExistsExcludingUser(ctx, "email_edit_a@example.com", a); err != nil || taken {
		t.Errorf("own email must not conflict (taken=%v err=%v)", taken, err)
	}
	// …but someone else's must (this is what the handler maps to 409).
	if taken, err := users.EmailExistsExcludingUser(ctx, "email_edit_b@example.com", a); err != nil || !taken {
		t.Errorf("another user's email must conflict (taken=%v err=%v)", taken, err)
	}
	// Case-insensitive at the app layer: uppercase input still conflicts.
	if taken, err := users.EmailExistsExcludingUser(ctx, "EMAIL_EDIT_B@EXAMPLE.COM", a); err != nil || !taken {
		t.Errorf("uppercase input must still conflict (taken=%v err=%v)", taken, err)
	}

	// Email write must reset is_email_verified in the same statement.
	newEmail := "email_edit_a_new@example.com"
	if err := users.UpdateProfileFields(ctx, a, nil, nil, nil, &newEmail); err != nil {
		t.Fatalf("update email: %v", err)
	}
	var gotEmail string
	var verified bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT email, is_email_verified FROM users WHERE id = $1`, a).Scan(&gotEmail, &verified); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if gotEmail != newEmail {
		t.Errorf("email not written: %q", gotEmail)
	}
	if verified {
		t.Error("is_email_verified must reset to FALSE when the email changes")
	}

	// A nil email leaves both email and the verified flag untouched.
	name := "Renamed"
	if err := users.UpdateProfileFields(ctx, b, &name, nil, nil, nil); err != nil {
		t.Fatalf("name-only update: %v", err)
	}
	var bEmail string
	if err := db.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, b).Scan(&bEmail); err != nil {
		t.Fatalf("reload b: %v", err)
	}
	if bEmail != "email_edit_b@example.com" {
		t.Errorf("nil email must leave the column untouched, got %q", bEmail)
	}
}
