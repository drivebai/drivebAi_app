package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// These are BEHAVIORAL tests: a real JWT is minted, the real middleware runs
// over httptest, and only the DB read behind BlockChecker is faked. They
// prove the client-reported failure mode ("blocked user keeps using the app
// with a still-valid token") is closed at the middleware layer.

type fakeBlockStore struct {
	mu      chan struct{} // 1-slot semaphore so tests can mutate safely
	blocked map[uuid.UUID]bool
	err     error
	calls   int
}

func newFakeBlockStore() *fakeBlockStore {
	f := &fakeBlockStore{mu: make(chan struct{}, 1), blocked: map[uuid.UUID]bool{}}
	f.mu <- struct{}{}
	return f
}

func (f *fakeBlockStore) IsUserBlocked(_ context.Context, id uuid.UUID) (bool, error) {
	<-f.mu
	defer func() { f.mu <- struct{}{} }()
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	return f.blocked[id], nil
}

func (f *fakeBlockStore) setBlocked(id uuid.UUID, b bool) {
	<-f.mu
	f.blocked[id] = b
	f.mu <- struct{}{}
}

func discardTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testJWT(t *testing.T) *auth.JWTService {
	t.Helper()
	return auth.NewJWTService("test-secret", 15*time.Minute, time.Hour)
}

func mintToken(t *testing.T, jwtSvc *auth.JWTService, userID uuid.UUID) string {
	t.Helper()
	token, _, err := jwtSvc.GenerateAccessToken(&models.User{
		ID: userID, Email: "user@example.com", Role: models.RoleDriver,
	})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return token
}

// serveOnce runs one request with a Bearer token through the middleware and
// reports the status code, response body, and whether the inner handler ran.
func serveOnce(t *testing.T, jwtSvc *auth.JWTService, checker *auth.BlockChecker, token string) (int, string, bool) {
	t.Helper()
	nextRan := false
	handler := AuthMiddleware(jwtSvc, checker)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextRan = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String(), nextRan
}

func assertAccountBlockedBody(t *testing.T, body string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("response body is not the API error envelope: %v (%s)", err, body)
	}
	if resp.Error.Code != models.ErrCodeAccountBlocked {
		t.Fatalf("error code = %q, want %q", resp.Error.Code, models.ErrCodeAccountBlocked)
	}
}

// The client's exact complaint: a user is blocked while holding a valid,
// unexpired access token. Every authenticated request must now be 403
// ACCOUNT_BLOCKED and must never reach the handler.
func TestAuthMiddleware_BlockedUserWithStillValidToken_Rejected(t *testing.T) {
	jwtSvc := testJWT(t)
	userID := uuid.New()
	store := newFakeBlockStore()
	store.setBlocked(userID, true)
	checker := auth.NewBlockChecker(store, 30*time.Second, discardTestLogger())

	token := mintToken(t, jwtSvc, userID) // valid for 15 more minutes

	code, body, nextRan := serveOnce(t, jwtSvc, checker, token)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
	if nextRan {
		t.Fatal("inner handler ran for a blocked user")
	}
	assertAccountBlockedBody(t, body)
}

func TestAuthMiddleware_UnblockedUser_PassesThrough(t *testing.T) {
	jwtSvc := testJWT(t)
	userID := uuid.New()
	checker := auth.NewBlockChecker(newFakeBlockStore(), 30*time.Second, discardTestLogger())

	code, _, nextRan := serveOnce(t, jwtSvc, checker, mintToken(t, jwtSvc, userID))
	if code != http.StatusOK || !nextRan {
		t.Fatalf("status = %d nextRan = %v, want 200/true", code, nextRan)
	}
}

// Blocking mid-session: the allow decision is cached, so the block bites
// after Invalidate (what the admin endpoint calls in-process) — and, absent
// that, after the TTL. This pins the enforcement-latency contract.
func TestAuthMiddleware_BlockAfterCachedAllow_TakesEffectOnInvalidate(t *testing.T) {
	jwtSvc := testJWT(t)
	userID := uuid.New()
	store := newFakeBlockStore()
	checker := auth.NewBlockChecker(store, time.Hour /* effectively never expires in-test */, discardTestLogger())
	token := mintToken(t, jwtSvc, userID)

	if code, _, _ := serveOnce(t, jwtSvc, checker, token); code != http.StatusOK {
		t.Fatalf("pre-block request: status = %d, want 200", code)
	}

	store.setBlocked(userID, true)

	// Still cached as allowed — this documents WHY BlockUser must Invalidate.
	if code, _, _ := serveOnce(t, jwtSvc, checker, token); code != http.StatusOK {
		t.Fatalf("cached request: status = %d, want 200 (cache not yet invalidated)", code)
	}

	checker.Invalidate(userID)

	code, body, _ := serveOnce(t, jwtSvc, checker, token)
	if code != http.StatusForbidden {
		t.Fatalf("post-invalidate request: status = %d, want 403", code)
	}
	assertAccountBlockedBody(t, body)
}

// Documented failure policy: a store error fails OPEN (request allowed) so a
// DB blip can't lock out the whole user base. If that policy is ever flipped
// intentionally, this test should be updated with the new rationale.
func TestAuthMiddleware_StoreError_FailsOpen(t *testing.T) {
	jwtSvc := testJWT(t)
	store := newFakeBlockStore()
	store.err = errors.New("db down")
	checker := auth.NewBlockChecker(store, time.Second, discardTestLogger())

	code, _, nextRan := serveOnce(t, jwtSvc, checker, mintToken(t, jwtSvc, uuid.New()))
	if code != http.StatusOK || !nextRan {
		t.Fatalf("status = %d nextRan = %v, want 200/true (fail-open)", code, nextRan)
	}
}

// Regression: requests with no/garbage tokens keep failing 401 exactly as
// before the block check was added.
func TestAuthMiddleware_MissingOrInvalidToken_Still401(t *testing.T) {
	jwtSvc := testJWT(t)
	checker := auth.NewBlockChecker(newFakeBlockStore(), time.Second, discardTestLogger())

	if code, _, _ := serveOnce(t, jwtSvc, checker, ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", code)
	}
	if code, _, _ := serveOnce(t, jwtSvc, checker, "not-a-jwt"); code != http.StatusUnauthorized {
		t.Fatalf("garbage token: status = %d, want 401", code)
	}
}
