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

// DB-free coverage for the new price-review endpoints, mirroring the same
// auth + input-validation slice that rescind_test / pickup_*_test pin.
// Anything that needs the repo (state machine transitions, transaction
// behaviour, system-message insertion) is verified by hand against a real
// DB in the manual QA checklist — those repos aren't unit-testable
// without a Postgres harness in the same way the rest of the repo is.

func TestAcceptPriceChange_Unauthorized(t *testing.T) {
	h := &LeaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/"+uuid.New().String()+"/accept-price", nil)
	rr := httptest.NewRecorder()

	h.AcceptPriceChange(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestAcceptPriceChange_InvalidID(t *testing.T) {
	h := &LeaseRequestHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/not-a-uuid/accept-price", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.AcceptPriceChange(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestDeclinePriceChange_Unauthorized(t *testing.T) {
	h := &LeaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/"+uuid.New().String()+"/decline-price", nil)
	rr := httptest.NewRecorder()

	h.DeclinePriceChange(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestDeclinePriceChange_InvalidID(t *testing.T) {
	h := &LeaseRequestHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/not-a-uuid/decline-price", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.DeclinePriceChange(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// IsPayable is the actual gate that keeps the driver from paying through a
// stale offer. These pins prove the predicate matches the product spec
// (Pay Now hidden while price-review pending, regardless of status).
func TestLeaseRequest_IsPayable_BlocksOnPriceReview(t *testing.T) {
	for _, status := range []models.LeaseRequestStatus{
		models.LeaseStatusAccepted, models.LeaseStatusPaymentPending,
	} {
		lr := &models.LeaseRequest{Status: status, PriceChangePending: true}
		if lr.IsPayable() {
			t.Fatalf("status=%s with PriceChangePending=true must NOT be payable", status)
		}
	}
}

func TestLeaseRequest_IsPayable_AllowsAcceptedAndPaymentPending(t *testing.T) {
	for _, status := range []models.LeaseRequestStatus{
		models.LeaseStatusAccepted, models.LeaseStatusPaymentPending,
	} {
		lr := &models.LeaseRequest{Status: status, PriceChangePending: false}
		if !lr.IsPayable() {
			t.Fatalf("status=%s with PriceChangePending=false must be payable", status)
		}
	}
}

func TestLeaseRequest_IsPayable_RejectsTerminalAndPreAcceptStates(t *testing.T) {
	for _, status := range []models.LeaseRequestStatus{
		models.LeaseStatusRequested,
		models.LeaseStatusDeclined,
		models.LeaseStatusCancelled,
		models.LeaseStatusPaid,
		models.LeaseStatusExpired,
		models.LeaseStatusExpiredRefunded,
	} {
		lr := &models.LeaseRequest{Status: status, PriceChangePending: false}
		if lr.IsPayable() {
			t.Fatalf("status=%s must NOT be payable", status)
		}
	}
}
