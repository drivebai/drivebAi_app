package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// Handler-level tests mirror the vehicle-return and key-handover test style:
// exercise the auth + input-validation paths that return before any
// repository access, so the tests don't need a running DB.

func TestPurchase_Create_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cars/"+uuid.New().String()+"/purchase-requests", nil)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestPurchase_Cancel_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/cancel", nil)
	rr := httptest.NewRecorder()
	h.Cancel(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestPurchase_Accept_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/accept", nil)
	rr := httptest.NewRecorder()
	h.Accept(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestPurchase_InspectAccept_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/inspect/accept", nil)
	rr := httptest.NewRecorder()
	h.InspectAccept(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestPurchase_InspectReject_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/inspect/reject", nil)
	rr := httptest.NewRecorder()
	h.InspectReject(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestPurchase_ScheduleHandover_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/schedule-handover", nil)
	rr := httptest.NewRecorder()
	h.ScheduleHandover(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestPurchase_KeysHandedOver_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/keys-handed-over", nil)
	rr := httptest.NewRecorder()
	h.KeysHandedOver(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// TestPurchase_Create_InvalidCarID exercises the URL-param parse branch —
// we should reject with 400 before touching the DB even for a
// well-authed user.
func TestPurchase_Create_InvalidCarID(t *testing.T) {
	h := &PurchaseRequestHandler{}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("carId", "not-a-uuid")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cars/not-a-uuid/purchase-requests", strings.NewReader(`{"offer_amount_cents":150000}`))
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// TestPurchase_Create_BelowMinimum guards the $1,000 floor.
func TestPurchase_Create_BelowMinimum(t *testing.T) {
	h := &PurchaseRequestHandler{}
	carID := uuid.New()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("carId", carID.String())
	body := `{"offer_amount_cents":50000}` // $500 — below floor
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cars/"+carID.String()+"/purchase-requests", bytes.NewBufferString(body))
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for offer below floor, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), string(models.ErrCodePurchaseOfferTooLow)) {
		t.Errorf("body should mention OFFER_TOO_LOW; got %s", rr.Body.String())
	}
}

// TestPurchase_Cancel_InvalidID
func TestPurchase_Cancel_InvalidID(t *testing.T) {
	h := &PurchaseRequestHandler{}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/not-a-uuid/cancel", nil)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Cancel(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// TestPurchase_Get_Unauthorized guards the read endpoint. Even a
// well-known purchase id is invisible without auth.
func TestPurchase_Get_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/purchase-requests/"+uuid.New().String(), nil)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// TestPurchase_UploadEvidence_Unauthorized
func TestPurchase_UploadEvidence_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/rejection-evidence", nil)
	rr := httptest.NewRecorder()
	h.UploadEvidence(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// TestPurchase_SignBOS_Unauthorized
func TestPurchase_SignBOS_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/purchase-requests/"+uuid.New().String()+"/bos/sign", nil)
	rr := httptest.NewRecorder()
	h.SignBOS(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// TestPurchase_AdminResolveRejection_Unauthorized: admin endpoint still
// checks that a user context exists — the middleware would 401 first in
// production, but the handler defends its own edge.
func TestPurchase_AdminResolveRejection_Unauthorized(t *testing.T) {
	h := &PurchaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/purchase-rejections/"+uuid.New().String()+"/resolve", nil)
	rr := httptest.NewRecorder()
	h.AdminResolveRejection(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

// TestPurchase_StatusForErr maps every ErrCode to a plausible HTTP status.
// The compile-time coverage prevents "silent 500" regressions when a new
// error code is added.
func TestPurchase_StatusForErr(t *testing.T) {
	cases := map[*models.APIError]int{
		models.ErrPurchaseRequestNotFound: http.StatusNotFound,
		models.ErrCarSold:                 http.StatusConflict,
		models.ErrBOSNotSigned:            http.StatusConflict,
		models.ErrBOSLocked:               http.StatusConflict,
		models.ErrAlreadySigned:           http.StatusConflict,
		models.ErrNotAwaitingInspection:   http.StatusConflict,
		models.ErrNotHandoverScheduled:    http.StatusConflict,
		models.ErrPurchaseOfferTooLow:     http.StatusBadRequest,
		models.ErrPurchaseEvidenceRequired: http.StatusBadRequest,
		models.ErrInvalidRoleField:        http.StatusForbidden,
		models.ErrCannotBuyOwnCar:         http.StatusForbidden,
	}
	for e, want := range cases {
		got := statusForPurchaseErr(e)
		if got != want {
			t.Errorf("%s: got status %d, want %d", e.Code, got, want)
		}
	}
}
