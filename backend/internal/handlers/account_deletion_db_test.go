package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// DB-gated tests for self-service account deletion (App Review 5.1.1(v)).
// Reuses the lifecycle test env (newLifecycleEnv). Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestAccountDeletion -v

func (e *lifecycleEnv) deletionUserHandler(t *testing.T) *UserHandler {
	t.Helper()
	logger := discardLogger()
	uh := NewUserHandler(
		repository.NewUserRepository(e.db),
		repository.NewDocumentRepository(e.db),
		repository.NewProfileRepository(e.db),
		repository.NewTokenRepository(e.db),
		nil, t.TempDir(), logger)
	uh.SetAccountDeletionDependencies(
		repository.NewAdminRepository(e.db),
		e.leaseRepo, e.carRepo,
		nil, // stripe: nil-safe (best-effort intent cancel is guarded)
		nil, // wsHub: guarded
		nil, // blockList: guarded
		nil, // notifHandler: guarded
	)
	return uh
}

func deleteAccountReq(t *testing.T, callerID uuid.UUID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/me", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(context.WithValue(req.Context(), httputil.UserIDKey, callerID))
}

func (e *lifecycleEnv) seedPasswordUser(t *testing.T, role, email, password string) uuid.UUID {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
		VALUES ($1, $2, $3, $4, 'Del', 'Candidate', TRUE, 'created')`, id, email, hash, role); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

// Happy path: tombstone lands, original email is freed and unfindable,
// password is gone, documents rows are removed, listings leave Discovery,
// and a bystander account is untouched (the endpoint acts on the caller).
func TestAccountDeletion_HappyPath(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	uh := e.deletionUserHandler(t)
	users := repository.NewUserRepository(e.db)

	owner := e.seedPasswordUser(t, "car_owner", "del_happy@example.com", "pw12345678")
	bystander := e.seedPasswordUser(t, "driver", "del_bystander@example.com", "pw12345678")
	car := e.seedCar(t, owner, "available", true, false)
	e.seedLicense(t, owner)

	// Listing visible before.
	before, err := e.carRepo.GetAvailableListings(ctx, "available", "")
	if err != nil {
		t.Fatalf("listings before: %v", err)
	}
	visible := func(cars []*models.Car) bool {
		for _, c := range cars {
			if c.ID == car {
				return true
			}
		}
		return false
	}
	if !visible(before) {
		t.Fatalf("seed car must be in discovery before deletion")
	}

	rr := httptest.NewRecorder()
	uh.DeleteAccount(rr, deleteAccountReq(t, owner, `{"confirm":"DELETE","password":"pw12345678"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d (%s)", rr.Code, rr.Body.String())
	}

	// Tombstone assertions.
	var email string
	var blocked bool
	var deletedAt *string
	var hash *string
	e.db.Pool.QueryRow(ctx, `SELECT email, is_blocked, deleted_at::text, password_hash FROM users WHERE id = $1`, owner).
		Scan(&email, &blocked, &deletedAt, &hash)
	if !strings.HasPrefix(email, "deleted+") || !blocked || deletedAt == nil || hash != nil {
		t.Errorf("tombstone wrong: email=%s blocked=%v deleted=%v hash=%v", email, blocked, deletedAt, hash)
	}
	// The original email cannot log in — the account under it no longer
	// exists (and the password hash is gone besides).
	if _, err := users.GetByEmail(ctx, "del_happy@example.com"); err == nil {
		t.Error("original email must be unfindable after deletion")
	}
	// Documents removed.
	var docs int
	e.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM documents WHERE user_id = $1`, owner).Scan(&docs)
	if docs != 0 {
		t.Errorf("documents not removed: %d", docs)
	}
	// Listing left Discovery (archived + owner blocked).
	after, err := e.carRepo.GetAvailableListings(ctx, "available", "")
	if err != nil {
		t.Fatalf("listings after: %v", err)
	}
	if visible(after) {
		t.Error("deleted owner's car still in discovery")
	}
	// Bystander untouched.
	var bEmail string
	e.db.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, bystander).Scan(&bEmail)
	if bEmail != "del_bystander@example.com" {
		t.Errorf("bystander mutated: %s", bEmail)
	}
}

// Wrong password / wrong word: 403/400, nothing touched.
func TestAccountDeletion_BadConfirmation(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	uh := e.deletionUserHandler(t)
	id := e.seedPasswordUser(t, "driver", "del_badpw@example.com", "pw12345678")

	rr := httptest.NewRecorder()
	uh.DeleteAccount(rr, deleteAccountReq(t, id, `{"confirm":"DELETE","password":"wrong"}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("wrong password: got %d want 403", rr.Code)
	}
	rr = httptest.NewRecorder()
	uh.DeleteAccount(rr, deleteAccountReq(t, id, `{"confirm":"delete me","password":"pw12345678"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("wrong word: got %d want 400", rr.Code)
	}
	var deletedAt *string
	e.db.Pool.QueryRow(ctx, `SELECT deleted_at::text FROM users WHERE id = $1`, id).Scan(&deletedAt)
	if deletedAt != nil {
		t.Error("account must be untouched after failed confirmation")
	}
}

// An active rental blocks with a clear, self-service reason.
func TestAccountDeletion_BlockedByActiveRental(t *testing.T) {
	e := newLifecycleEnv(t)
	uh := e.deletionUserHandler(t)

	owner := e.seedPasswordUser(t, "car_owner", "del_blk_owner@example.com", "pw12345678")
	driver := e.seedPasswordUser(t, "driver", "del_blk_driver@example.com", "pw12345678")
	e.seedLicense(t, driver)
	e.seedActiveRental(t, owner, driver)

	for _, who := range []uuid.UUID{driver, owner} {
		rr := httptest.NewRecorder()
		uh.DeleteAccount(rr, deleteAccountReq(t, who, `{"confirm":"DELETE","password":"pw12345678"}`))
		if rr.Code != http.StatusConflict {
			t.Fatalf("active rental must 409, got %d (%s)", rr.Code, rr.Body.String())
		}
		if code := errCodeOf(t, rr); code != models.ErrCodeDeletionBlocked {
			t.Errorf("code %s want DELETION_BLOCKED", code)
		}
		if !strings.Contains(rr.Body.String(), "in the app") {
			t.Errorf("blocker message must state the in-app exit: %s", rr.Body.String())
		}
	}
}

// Open (unpaid) lease requests do not block — deletion closes them and
// frees the car's reservation.
func TestAccountDeletion_AutoResolvesOpenLease(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	uh := e.deletionUserHandler(t)

	owner := e.seedPasswordUser(t, "car_owner", "del_open_owner@example.com", "pw12345678")
	driver := e.seedPasswordUser(t, "driver", "del_open_driver@example.com", "pw12345678")
	e.seedLicense(t, driver)
	car := e.seedCar(t, owner, "available", true, false)

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed request: %d", rr.Code)
	}
	var leaseID uuid.UUID
	e.db.Pool.QueryRow(ctx, `SELECT id FROM lease_requests WHERE listing_id = $1 AND driver_id = $2`, car, driver).Scan(&leaseID)
	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("accept: %v", err)
	}

	rr = httptest.NewRecorder()
	uh.DeleteAccount(rr, deleteAccountReq(t, driver, `{"confirm":"DELETE","password":"pw12345678"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete with open accepted lease: %d (%s)", rr.Code, rr.Body.String())
	}

	var status string
	var reservedBy *uuid.UUID
	e.db.Pool.QueryRow(ctx, `SELECT status FROM lease_requests WHERE id = $1`, leaseID).Scan(&status)
	e.db.Pool.QueryRow(ctx, `SELECT reserved_by_lease_request_id FROM cars WHERE id = $1`, car).Scan(&reservedBy)
	if status != "cancelled" {
		t.Errorf("open lease must be cancelled, got %s", status)
	}
	if reservedBy != nil {
		t.Error("car reservation must be released")
	}
}
