package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
)

// The product-tour progress endpoints are strictly self-scoped: the user id
// comes only from the JWT context, never from the path or body. These tests
// exercise the auth + validation branches that return before any DB access.

func TestOnboarding_GetProgress_Unauthorized(t *testing.T) {
	h := &OnboardingHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/onboarding-progress", nil)
	rr := httptest.NewRecorder()
	h.GetProgress(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestOnboarding_UpdateProgress_Unauthorized(t *testing.T) {
	h := &OnboardingHandler{}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/onboarding-progress",
		strings.NewReader(`{"entries":[{"tour_key":"welcome","status":"completed"}]}`))
	rr := httptest.NewRecorder()
	h.UpdateProgress(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestOnboarding_UpdateProgress_InvalidJSON(t *testing.T) {
	h := &OnboardingHandler{}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/onboarding-progress", strings.NewReader(`{bad json`))
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.UpdateProgress(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// TestOnboarding_UpdateProgress_EmptyEntries: a well-authed request with no
// entries is rejected at validation (400) before any repository access — so a
// nil repo never panics.
func TestOnboarding_UpdateProgress_EmptyEntries(t *testing.T) {
	h := &OnboardingHandler{}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/onboarding-progress", strings.NewReader(`{"entries":[]}`))
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.UpdateProgress(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

// TestOnboarding_UpdateProgress_InvalidStatus: an unknown status is rejected at
// validation (400) before repository access.
func TestOnboarding_UpdateProgress_InvalidStatus(t *testing.T) {
	h := &OnboardingHandler{}
	body := `{"entries":[{"tour_key":"welcome","status":"nope"}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/me/onboarding-progress", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.UpdateProgress(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}
