package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// DB-gated endpoint tests for the rental-lifecycle batch: the availability
// guard's per-status error codes, dispute → ticket, admin resolve on both
// branches, and the revive-after-cancel exit. Run with:
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run TestLifecycle -v
//
// The database must be migrated through 000048. Stripe is nil throughout —
// every asserted path is either pre-Stripe (guards) or the zero-refund
// fast path (FinalizeNoRefund), which never touches the client.

type lifecycleEnv struct {
	db          *database.DB
	leaseH      *LeaseRequestHandler
	returnH     *VehicleReturnHandler
	leaseRepo   *repository.LeaseRequestRepository
	returnRepo  *repository.VehicleReturnRepository
	ticketRepo  *repository.TicketRepository
	carRepo     *repository.CarRepository
}

func newLifecycleEnv(t *testing.T) *lifecycleEnv {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run lifecycle endpoint tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)

	logger := discardLogger()
	hub := ws.NewHub(logger) // buffered channel; fine without Run for test volumes

	leaseRepo := repository.NewLeaseRequestRepository(db)
	carRepo := repository.NewCarRepository(db)
	returnRepo := repository.NewVehicleReturnRepository(db)
	ticketRepo := repository.NewTicketRepository(db)
	notifH := NewNotificationHandler(
		repository.NewNotificationRepository(db),
		repository.NewDeviceTokenRepository(db),
		hub, nil, logger)

	leaseH := NewLeaseRequestHandler(
		leaseRepo, carRepo,
		repository.NewCarDocumentRepository(db),
		repository.NewUserRepository(db),
		repository.NewChatRepository(db),
		repository.NewDocumentRepository(db),
		repository.NewSharedDocumentRepository(db),
		repository.NewKeyHandoverRepository(db),
		nil, hub, notifH, nil, time.Hour, logger)
	leaseH.SetTicketRepository(ticketRepo)

	returnH := NewVehicleReturnHandler(
		returnRepo, leaseRepo, carRepo,
		repository.NewUserRepository(db),
		repository.NewChatRepository(db),
		nil, hub, notifH, logger)
	returnH.SetTicketRepository(ticketRepo)

	return &lifecycleEnv{db: db, leaseH: leaseH, returnH: returnH,
		leaseRepo: leaseRepo, returnRepo: returnRepo, ticketRepo: ticketRepo, carRepo: carRepo}
}

func (e *lifecycleEnv) seedUser(t *testing.T, role, email string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
		VALUES ($1, $2, 'x', $3, 'Life', 'Cycle', TRUE, 'created')`, id, email, role); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })
	return id
}

func (e *lifecycleEnv) seedLicense(t *testing.T, userID uuid.UUID) {
	t.Helper()
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO documents (id, user_id, type, file_name, file_path, file_size, mime_type, status)
		VALUES ($1, $2, 'drivers_license', 'l.jpg', '/tmp/l.jpg', 1, 'image/jpeg', 'verified')`, id, userID); err != nil {
		t.Fatalf("seed license: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, id) })
}

func (e *lifecycleEnv) seedCar(t *testing.T, ownerID uuid.UUID, status string, approved, paused bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.db.Pool.Exec(context.Background(), `
		INSERT INTO cars (
			id, owner_id, title, description, make, model, year, body_type, fuel_type, mileage,
			is_for_rent, weekly_rent_price, is_for_sale, currency,
			status, is_paused, is_approved, rented_weeks, total_earned, created_at, updated_at
		) VALUES ($1, $2, 'Lifecycle Endpoint Car', '', 'Test', $6, 2024, 'sedan', 'gas', 1000,
		          TRUE, 300, FALSE, 'USD', $3, $4, $5, 0, 0, NOW(), NOW())`,
		id, ownerID, status, paused, approved, "C-"+id.String()[:8]); err != nil {
		t.Fatalf("seed car: %v", err)
	}
	t.Cleanup(func() { e.db.Pool.Exec(context.Background(), `DELETE FROM cars WHERE id = $1`, id) })
	return id
}

func createLeaseReq(t *testing.T, driverID uuid.UUID, listingID uuid.UUID) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("listingId", listingID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/listings/"+listingID.String()+"/lease-requests",
		bytes.NewBufferString(`{"weeks":1}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, driverID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func errCodeOf(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("parse error body %q: %v", rr.Body.String(), err)
	}
	return body.Error.Code
}

// The availability guard, per status: rented/sold/reserved → 409
// CAR_NOT_AVAILABLE; paused/unapproved → 400 CAR_NOT_FOR_RENT; available →
// 201.
func TestLifecycle_CreateAvailabilityGuard(t *testing.T) {
	e := newLifecycleEnv(t)
	owner := e.seedUser(t, "car_owner", "lcep_owner@example.com")
	driver := e.seedUser(t, "driver", "lcep_driver@example.com")
	e.seedLicense(t, driver)

	cases := []struct {
		name       string
		status     string
		approved   bool
		paused     bool
		wantStatus int
		wantCode   string
	}{
		{"rented", "rented", true, false, http.StatusConflict, models.ErrCodeCarNotAvailable},
		{"sold", "sold", true, false, http.StatusConflict, models.ErrCodeCarNotAvailable},
		{"paused", "paused", true, true, http.StatusBadRequest, models.ErrCodeCarNotForRent},
		{"pending (unapproved)", "pending", false, false, http.StatusBadRequest, models.ErrCodeCarNotForRent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			car := e.seedCar(t, owner, c.status, c.approved, c.paused)
			rr := httptest.NewRecorder()
			e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
			if rr.Code != c.wantStatus {
				t.Fatalf("status %s: got %d want %d (%s)", c.name, rr.Code, c.wantStatus, rr.Body.String())
			}
			if code := errCodeOf(t, rr); code != c.wantCode {
				t.Errorf("status %s: code %s want %s", c.name, code, c.wantCode)
			}
		})
	}

	// The paid-but-not-picked-up window: status still 'available' but the
	// reservation is held — must 409, not create a doomed request.
	t.Run("reserved but status available", func(t *testing.T) {
		car := e.seedCar(t, owner, "available", true, false)
		holder := e.seedUser(t, "driver", "lcep_holder@example.com")
		e.seedLicense(t, holder)
		rrH := httptest.NewRecorder()
		e.leaseH.CreateLeaseRequest(rrH, createLeaseReq(t, holder, car))
		if rrH.Code != http.StatusCreated {
			t.Fatalf("holder create: %d (%s)", rrH.Code, rrH.Body.String())
		}
		var holderLease struct {
			LeaseRequest struct {
				ID uuid.UUID `json:"id"`
			} `json:"lease_request"`
		}
		json.Unmarshal(rrH.Body.Bytes(), &holderLease)
		if _, err := e.leaseRepo.AcceptLeaseRequest(context.Background(), holderLease.LeaseRequest.ID, owner); err != nil {
			t.Fatalf("accept holder lease: %v", err)
		}

		rr := httptest.NewRecorder()
		e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
		if rr.Code != http.StatusConflict {
			t.Fatalf("reserved car: got %d want 409 (%s)", rr.Code, rr.Body.String())
		}
		if code := errCodeOf(t, rr); code != models.ErrCodeCarNotAvailable {
			t.Errorf("reserved car: code %s", code)
		}
	})

	t.Run("available succeeds", func(t *testing.T) {
		car := e.seedCar(t, owner, "available", true, false)
		rr := httptest.NewRecorder()
		e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
		if rr.Code != http.StatusCreated {
			t.Fatalf("available car: got %d want 201 (%s)", rr.Code, rr.Body.String())
		}
	})
}

// seedActiveRental builds the full paid+picked-up state through the real
// transitions so the guards stay honest, and returns (leaseID, carID).
func (e *lifecycleEnv) seedActiveRental(t *testing.T, owner, driver uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	car := e.seedCar(t, owner, "available", true, false)
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, createLeaseReq(t, driver, car))
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed rental create: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID uuid.UUID `json:"id"`
		} `json:"lease_request"`
	}
	json.Unmarshal(rr.Body.Bytes(), &created)
	leaseID := created.LeaseRequest.ID
	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("seed rental accept: %v", err)
	}
	if _, err := e.leaseRepo.SetPaid(ctx, leaseID); err != nil {
		t.Fatalf("seed rental paid: %v", err)
	}
	if _, err := e.leaseRepo.ConfirmPickup(ctx, leaseID, driver); err != nil {
		t.Fatalf("seed rental pickup: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE vehicle_return_id IN (SELECT id FROM vehicle_returns WHERE lease_request_id = $1)`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id = $1`, leaseID)
	})
	return leaseID, car
}

func returnReq(t *testing.T, userID, returnID uuid.UUID, body string) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", returnID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vehicle-returns/"+returnID.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, userID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// Dispute must open a linked, open, submitted ticket; admin ACCEPT (zero
// refund) completes the return, releases the car, and resolves the ticket.
func TestLifecycle_DisputeTicketAndAdminAccept(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "lcep_owner_d@example.com")
	driver := e.seedUser(t, "driver", "lcep_driver_d@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)

	// Driver initiates ($0 paid → zero-refund path keeps Stripe out).
	rr := httptest.NewRecorder()
	initReq := returnReq(t, driver, leaseID, `{}`)
	initRctx := chi.NewRouteContext()
	initRctx.URLParams.Add("id", leaseID.String())
	e.returnH.Initiate(rr, initReq)
	if rr.Code != http.StatusCreated {
		t.Fatalf("initiate: %d (%s)", rr.Code, rr.Body.String())
	}
	ret, err := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if err != nil {
		t.Fatalf("load return: %v", err)
	}

	// Owner disputes → ticket appears, linked and open.
	rr = httptest.NewRecorder()
	e.returnH.Dispute(rr, returnReq(t, owner, ret.ID, `{"reason":"Car was not at the agreed spot"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("dispute: %d (%s)", rr.Code, rr.Body.String())
	}
	var ticketStatus string
	if err := e.db.Pool.QueryRow(ctx, `
		SELECT status FROM support_tickets WHERE vehicle_return_id = $1`, ret.ID).Scan(&ticketStatus); err != nil {
		t.Fatalf("dispute created no ticket: %v", err)
	}
	if ticketStatus != "open" {
		t.Errorf("ticket status = %s, want open", ticketStatus)
	}

	// Admin accepts with the required note → completed, car released,
	// ticket resolved.
	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, owner, ret.ID, `{"resolution":"accept","note":"Photos confirm the return."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("admin accept: %d (%s)", rr.Code, rr.Body.String())
	}
	after, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if after.Status != models.VehicleReturnCompleted {
		t.Errorf("return status = %s, want completed", after.Status)
	}
	var carStatus string
	var reservedBy *uuid.UUID
	e.db.Pool.QueryRow(ctx, `SELECT status, reserved_by_lease_request_id FROM cars WHERE id = $1`, carID).Scan(&carStatus, &reservedBy)
	if carStatus != "available" || reservedBy != nil {
		t.Errorf("car after accept: status=%s reserved=%v, want available+free", carStatus, reservedBy)
	}
	e.db.Pool.QueryRow(ctx, `SELECT status FROM support_tickets WHERE vehicle_return_id = $1`, ret.ID).Scan(&ticketStatus)
	if ticketStatus != "resolved" {
		t.Errorf("ticket after accept = %s, want resolved", ticketStatus)
	}

	// A missing note must 400 before touching anything.
	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, owner, ret.ID, `{"resolution":"accept","note":""}`))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty note must 400, got %d", rr.Code)
	}
}

// Admin REJECT keeps the rental alive (car stays rented+reserved) and the
// driver can submit a NEW return afterwards — cancelled is no longer
// lease-fatal.
func TestLifecycle_AdminRejectThenRevive(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "lcep_owner_r@example.com")
	driver := e.seedUser(t, "driver", "lcep_driver_r@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)

	rr := httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("initiate: %d", rr.Code)
	}
	ret, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)

	rr = httptest.NewRecorder()
	e.returnH.Dispute(rr, returnReq(t, owner, ret.ID, `{"reason":"Still have not received the car"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("dispute: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	e.returnH.AdminResolve(rr, returnReq(t, owner, ret.ID, `{"resolution":"reject","note":"Owner evidence shows no handover."}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("admin reject: %d (%s)", rr.Code, rr.Body.String())
	}
	after, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if after.Status != models.VehicleReturnCancelled {
		t.Fatalf("return after reject = %s, want cancelled", after.Status)
	}
	var carStatus string
	var reservedBy *uuid.UUID
	e.db.Pool.QueryRow(ctx, `SELECT status, reserved_by_lease_request_id FROM cars WHERE id = $1`, carID).Scan(&carStatus, &reservedBy)
	if carStatus != "rented" || reservedBy == nil {
		t.Errorf("car after reject: status=%s reserved=%v, want rented+held (rental continues)", carStatus, reservedBy)
	}

	// The exit: the driver returns the car for real later — Initiate must
	// revive the cancelled row instead of dead-ending forever.
	rr = httptest.NewRecorder()
	e.returnH.Initiate(rr, returnReq(t, driver, leaseID, `{}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-initiate after reject: %d (%s)", rr.Code, rr.Body.String())
	}
	revived, _ := e.returnRepo.GetByLeaseRequestID(ctx, leaseID)
	if revived.Status != models.VehicleReturnDriverInitiated {
		t.Errorf("revived return = %s, want driver_initiated", revived.Status)
	}
	if revived.DisputeReason != nil {
		t.Errorf("revived return must clear the old dispute trail")
	}
}

// The term sweep end-to-end on real rows: overdue flag set once, escalation
// opens exactly one ticket even when the sweep runs twice.
func TestLifecycle_TermSweepIdempotent(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	owner := e.seedUser(t, "car_owner", "lcep_owner_t@example.com")
	driver := e.seedUser(t, "driver", "lcep_driver_t@example.com")
	e.seedLicense(t, driver)
	leaseID, _ := e.seedActiveRental(t, owner, driver)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE lease_request_id = $1`, leaseID)
	})

	// Age the rental: ended 4 days ago → overdue AND past escalation.
	if _, err := e.db.Pool.Exec(ctx, `
		UPDATE lease_requests SET rental_ends_at = NOW() - INTERVAL '4 days' WHERE id = $1`, leaseID); err != nil {
		t.Fatalf("age rental: %v", err)
	}

	e.leaseH.runTermSweep(ctx)
	e.leaseH.runTermSweep(ctx) // idempotency: second run must add nothing

	var overdueAt, escalatedAt *time.Time
	e.db.Pool.QueryRow(ctx, `
		SELECT overdue_notified_at, overdue_escalated_at FROM lease_requests WHERE id = $1`, leaseID).
		Scan(&overdueAt, &escalatedAt)
	if overdueAt == nil {
		t.Error("overdue_notified_at not set by the sweep")
	}
	if escalatedAt == nil {
		t.Error("overdue_escalated_at not set by the sweep")
	}

	var tickets int
	e.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM support_tickets WHERE lease_request_id = $1`, leaseID).Scan(&tickets)
	if tickets != 1 {
		t.Errorf("escalation tickets = %d, want exactly 1", tickets)
	}
}
