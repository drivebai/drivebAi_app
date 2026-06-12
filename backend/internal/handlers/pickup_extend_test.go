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
)

// Exercises the auth + body-validation paths in ExtendPickupDeadline that
// return before any repository access. Matches the DB-free handler style
// used by key_handover_test.go and pickup_confirm_test.go.

func TestExtendPickupDeadline_Unauthorized(t *testing.T) {
	h := &LeaseRequestHandler{}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/"+uuid.New().String()+"/pickup-deadline/extend",
		strings.NewReader(`{"minutes":15}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.ExtendPickupDeadline(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestExtendPickupDeadline_InvalidID(t *testing.T) {
	h := &LeaseRequestHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/not-a-uuid/pickup-deadline/extend",
		strings.NewReader(`{"minutes":15}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ExtendPickupDeadline(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestExtendPickupDeadline_InvalidMinutes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"zero", `{"minutes":0}`},
		{"negative", `{"minutes":-15}`},
		{"too-large", `{"minutes":121}`},
		{"not-preset", `{"minutes":45}`},
		{"missing", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &LeaseRequestHandler{}

			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", uuid.New().String())

			req := httptest.NewRequest(http.MethodPost,
				"/api/v1/lease-requests/"+rctx.URLParams.Values[0]+"/pickup-deadline/extend",
				bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
			ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			h.ExtendPickupDeadline(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected status 400, got %d body=%q",
					tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestExtendPickupDeadline_InvalidJSON(t *testing.T) {
	h := &LeaseRequestHandler{}

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/lease-requests/x/pickup-deadline/extend",
		strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ExtendPickupDeadline(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed body, got %d", rr.Code)
	}
}
