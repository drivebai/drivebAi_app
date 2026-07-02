package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// Exercises the auth + input-validation branches of Dismiss that return
// before any DB access. Matches the DB-free handler test style used by
// pickup_confirm_test / pickup_extend_test.

func TestDismissHandover_Unauthorized(t *testing.T) {
	h := &KeyHandoverHandler{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/key-handovers/"+uuid.New().String()+"/dismiss", nil)
	rr := httptest.NewRecorder()

	h.Dismiss(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestDismissHandover_InvalidID(t *testing.T) {
	h := &KeyHandoverHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/key-handovers/not-a-uuid/dismiss", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.Dismiss(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

// Pins the terminal-state contract — the dismiss endpoint must only allow
// dismissal of cards whose underlying lease has reached a terminal pickup
// state. A drift here would let a user hide an active card from themselves.

func TestIsPickupTerminal(t *testing.T) {
	terminal := []models.LeaseRequestStatus{
		models.LeaseStatusExpiredRefunded,
		models.LeaseStatusCancelled,
		models.LeaseStatusDeclined,
		models.LeaseStatusExpired,
	}
	for _, s := range terminal {
		if !isPickupTerminal(s) {
			t.Errorf("status %q should be terminal", s)
		}
	}

	active := []models.LeaseRequestStatus{
		models.LeaseStatusRequested,
		models.LeaseStatusAccepted,
		models.LeaseStatusPaymentPending,
		models.LeaseStatusPaid,
	}
	for _, s := range active {
		if isPickupTerminal(s) {
			t.Errorf("status %q should NOT be terminal (dismissing an active card would hide it from the user)", s)
		}
	}
}

func TestErrHandoverNotDismissable_Shape(t *testing.T) {
	if got, want := models.ErrHandoverNotDismissable.Code, "HANDOVER_NOT_DISMISSABLE"; got != want {
		t.Errorf("error code drift: got %q want %q", got, want)
	}
	if models.ErrHandoverNotDismissable.Message == "" {
		t.Error("error message should not be empty (client renders this verbatim)")
	}
}

// Pins the role-specific "waiting on owner" error shape. The iOS client
// keys off the code (HANDOVER_OWNER_NOT_CONFIRMED) to render a distinct
// message from the generic INVALID_HANDOVER_ACTION guard, so a drift here
// would silently downgrade the UX.
func TestErrHandoverOwnerNotConfirmed_Shape(t *testing.T) {
	if got, want := models.ErrHandoverOwnerNotConfirmed.Code, "HANDOVER_OWNER_NOT_CONFIRMED"; got != want {
		t.Errorf("error code drift: got %q want %q", got, want)
	}
	if got, want := models.ErrHandoverOwnerNotConfirmed.Code, models.ErrCodeHandoverOwnerNotConfirmed; got != want {
		t.Errorf("error code constant drift: got %q want %q", got, want)
	}
	if models.ErrHandoverOwnerNotConfirmed.Message == "" {
		t.Error("error message should not be empty (client renders this verbatim)")
	}
	// Must be distinct from the generic guard so the client can branch.
	if models.ErrHandoverOwnerNotConfirmed.Code == models.ErrInvalidHandoverAction.Code {
		t.Error("HANDOVER_OWNER_NOT_CONFIRMED must be distinct from INVALID_HANDOVER_ACTION")
	}
}
