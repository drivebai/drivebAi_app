package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// Behavioral tests for OptionalAuth: real JWTService, real HTTP round trips.
// The contract under test — anonymous passes through user-less, a valid
// token populates the same context keys AuthMiddleware sets, and an invalid
// token is REJECTED (401) rather than downgraded to anonymous.

func optionalAuthHarness(t *testing.T) (*auth.JWTService, http.Handler, *struct {
	called bool
	userID uuid.UUID
	hasUser bool
}) {
	t.Helper()
	jwtSvc := auth.NewJWTService("test-secret", time.Hour, time.Hour)
	seen := &struct {
		called bool
		userID uuid.UUID
		hasUser bool
	}{}
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.called = true
		seen.userID, seen.hasUser = httputil.GetUserID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	return jwtSvc, OptionalAuth(jwtSvc, nil)(final), seen
}

func TestOptionalAuth_NoHeaderIsAnonymous(t *testing.T) {
	_, h, seen := optionalAuthHarness(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/listings", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !seen.called || seen.hasUser {
		t.Errorf("anonymous request must reach the handler with NO user in context (called=%v hasUser=%v)", seen.called, seen.hasUser)
	}
}

func TestOptionalAuth_ValidTokenPopulatesUser(t *testing.T) {
	jwtSvc, h, seen := optionalAuthHarness(t)
	user := &models.User{ID: uuid.New(), Email: "driver@example.com", Role: models.RoleDriver}
	token, _, err := jwtSvc.GenerateAccessToken(user)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/listings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !seen.hasUser || seen.userID != user.ID {
		t.Errorf("valid token must populate the user context key (hasUser=%v id=%v)", seen.hasUser, seen.userID)
	}
}

func TestOptionalAuth_InvalidTokenIs401NotAnonymous(t *testing.T) {
	_, h, seen := optionalAuthHarness(t)
	for _, header := range []string{
		"Bearer not-a-jwt",
		"Bearer ",
		"NotBearer abc",
	} {
		seen.called = false
		req := httptest.NewRequest(http.MethodGet, "/listings", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q: status = %d, want 401 — a bad token must never silently downgrade to the anonymous view", header, rec.Code)
		}
		if seen.called {
			t.Errorf("header %q: handler must not run on a rejected token", header)
		}
	}
}

func TestOptionalAuth_ExpiredTokenIs401(t *testing.T) {
	// Token minted by a service whose access TTL is negative → already expired.
	expiredSvc := auth.NewJWTService("test-secret", -time.Minute, time.Hour)
	user := &models.User{ID: uuid.New(), Email: "driver@example.com", Role: models.RoleDriver}
	token, _, err := expiredSvc.GenerateAccessToken(user)
	if err != nil {
		t.Fatal(err)
	}

	_, h, seen := optionalAuthHarness(t)
	req := httptest.NewRequest(http.MethodGet, "/listings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token: status = %d, want 401 (client refresh machinery must engage)", rec.Code)
	}
	if seen.called {
		t.Error("handler must not run on an expired token")
	}
}

// TestProtectedRoutesRejectAnonymous pins the guest boundary: a router
// assembled with the REAL AuthMiddleware (as the /api/v1 protected group is)
// returns 401 for tokenless requests on representative protected patterns.
// This tests the middleware gate itself; the route-to-group wiring in
// cmd/api/main.go is asserted by review, since building the full router
// needs live repositories.
func TestProtectedRoutesRejectAnonymous(t *testing.T) {
	jwtSvc := auth.NewJWTService("test-secret", time.Hour, time.Hour)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(jwtSvc, nil))
		ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
		// Representative sample of the protected surface.
		r.Get("/api/v1/cars", ok)
		r.Post("/api/v1/cars", ok)
		r.Get("/api/v1/me/likes", ok)
		r.Post("/api/v1/listings/{listingId}/like", ok)
		r.Post("/api/v1/listings/{listingId}/lease-requests", ok)
		r.Post("/api/v1/cars/{carId}/purchase-requests", ok)
		r.Get("/api/v1/chats", ok)
		r.Get("/api/v1/me/documents", ok)
	})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/cars"},
		{http.MethodPost, "/api/v1/cars"},
		{http.MethodGet, "/api/v1/me/likes"},
		{http.MethodPost, "/api/v1/listings/" + uuid.NewString() + "/like"},
		{http.MethodPost, "/api/v1/listings/" + uuid.NewString() + "/lease-requests"},
		{http.MethodPost, "/api/v1/cars/" + uuid.NewString() + "/purchase-requests"},
		{http.MethodGet, "/api/v1/chats"},
		{http.MethodGet, "/api/v1/me/documents"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401 for anonymous", tc.method, tc.path, rec.Code)
		}
	}
}
