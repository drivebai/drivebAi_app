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

// Auth + input validation tests for the rescind endpoint, matching the
// DB-free style used by pickup_confirm_test / pickup_extend_test / etc.

func TestRescindAccept_Unauthorized(t *testing.T) {
	h := &LeaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/"+uuid.New().String()+"/rescind", nil)
	rr := httptest.NewRecorder()

	h.RescindAcceptedLeaseRequest(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestRescindAccept_InvalidID(t *testing.T) {
	h := &LeaseRequestHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/not-a-uuid/rescind", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.RescindAcceptedLeaseRequest(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}
