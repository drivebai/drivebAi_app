package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
)

// These tests exercise the auth + input-validation paths in ConfirmPickup
// that return before any repository access, matching the DB-free handler
// test style used elsewhere in this package (see key_handover_test.go).

func TestConfirmPickup_Unauthorized(t *testing.T) {
	h := &LeaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/lease-requests/"+uuid.New().String()+"/pickup-confirm", nil)
	rr := httptest.NewRecorder()

	h.ConfirmPickup(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestConfirmPickup_InvalidID(t *testing.T) {
	h := &LeaseRequestHandler{}

	// chi.URLParam pulls the value from chi.RouteContext. Set "id" to an
	// obvious non-UUID so the handler hits the "Invalid lease request ID"
	// branch and returns 400 before any DB access.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/lease-requests/not-a-uuid/pickup-confirm", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ConfirmPickup(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}
