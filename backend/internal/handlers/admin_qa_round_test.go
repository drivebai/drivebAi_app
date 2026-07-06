package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/middleware"
	"github.com/drivebai/backend/internal/models"
)

// Admin password reset (QA pt-2 / D7). Handler-level tests exercise the
// paths that return before any repository access (repo-style: no DB), plus
// a router-level check that the RequireRole(admin) guard actually protects
// the new route.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAdminResetPassword_InvalidID(t *testing.T) {
	h := &AdminHandler{logger: discardLogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/not-a-uuid/reset-password", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.ResetUserPassword(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}

func TestAdminResetPassword_DependenciesNotWired(t *testing.T) {
	// Constructed without SetPasswordResetDependencies — must fail closed
	// (500), never panic or silently 202.
	h := &AdminHandler{logger: discardLogger()}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+uuid.NewString()+"/reset-password", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.NewString())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.ResetUserPassword(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 when deps unwired, got %d", rr.Code)
	}
}

// TestAdminResetPassword_RoleGuard mounts the route exactly as main.go does
// (inside RequireRole(admin)) and proves a non-admin caller is rejected
// before the handler runs, while an admin reaches the handler.
func TestAdminResetPassword_RoleGuard(t *testing.T) {
	h := &AdminHandler{logger: discardLogger()}

	r := chi.NewRouter()
	r.Route("/api/v1/admin", func(r chi.Router) {
		r.Use(middleware.RequireRole(models.RoleAdmin))
		r.Post("/users/{id}/reset-password", h.ResetUserPassword)
	})

	cases := []struct {
		name       string
		role       models.Role
		wantStatus int
	}{
		// Driver / owner JWTs must be rejected by the middleware.
		{"driver forbidden", models.RoleDriver, http.StatusForbidden},
		{"owner forbidden", models.RoleCarOwner, http.StatusForbidden},
		// Admin passes the guard and reaches the handler, which (with no
		// deps wired in this test) fails with 500 — anything but 403
		// proves the handler actually executed.
		{"admin passes guard", models.RoleAdmin, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+uuid.NewString()+"/reset-password", nil)
			ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
			ctx = context.WithValue(ctx, httputil.RoleKey, tc.role)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("role %s: want %d, got %d", tc.role, tc.wantStatus, rr.Code)
			}
		})
	}
}
