package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
)

// These tests exercise the auth + input-validation paths that return before any
// repository access, matching the DB-free handler test style used elsewhere.

func TestKeyHandover_Today_Unauthorized(t *testing.T) {
	h := &KeyHandoverHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/key-handovers/today", nil)
	rr := httptest.NewRecorder()

	h.Today(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestKeyHandover_OwnerConfirm_Unauthorized(t *testing.T) {
	h := &KeyHandoverHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/key-handovers/"+uuid.New().String()+"/owner-confirm", nil)
	rr := httptest.NewRecorder()

	h.OwnerConfirm(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestKeyHandover_DriverConfirm_Unauthorized(t *testing.T) {
	h := &KeyHandoverHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/key-handovers/"+uuid.New().String()+"/driver-confirm", nil)
	rr := httptest.NewRecorder()

	h.DriverConfirm(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", rr.Code)
	}
}

func TestKeyHandover_Get_InvalidID(t *testing.T) {
	h := &KeyHandoverHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/key-handovers/not-a-uuid", nil)
	// Inject a user so we pass the auth check and reach UUID parsing.
	req = req.WithContext(context.WithValue(req.Context(), httputil.UserIDKey, uuid.New()))
	rr := httptest.NewRecorder()

	h.Get(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}
