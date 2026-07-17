package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/email"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// BEHAVIORAL tests for the two token-issuing paths a blocked user could
// abuse after the middleware cut them off: refresh-token rotation and OTP
// re-login. The real handlers run over httptest; only the stores are faked
// via the narrow interfaces in auth_stores.go. Each rejection test has a
// paired happy-path test so a 403-for-the-wrong-reason cannot pass silently.

// ─── fakes ───────────────────────────────────────────────────────────────────

type fakeUserStore struct {
	authUserStore // panic on anything not overridden
	user          *models.User
}

func (f *fakeUserStore) GetByEmail(_ context.Context, _ string) (*models.User, error) {
	if f.user == nil {
		return nil, models.ErrUserNotFound
	}
	return f.user, nil
}

func (f *fakeUserStore) GetByID(_ context.Context, _ uuid.UUID) (*models.User, error) {
	if f.user == nil {
		return nil, models.ErrUserNotFound
	}
	return f.user, nil
}

func (f *fakeUserStore) GetOTPSendCount(_ context.Context, _ string, _ time.Time) (int, error) {
	return 0, nil
}
func (f *fakeUserStore) RecordOTPSend(_ context.Context, _, _ string) error { return nil }

type fakeTokenStore struct {
	authTokenStore
	stored             *models.RefreshToken
	refreshTokensMade  int
	revokedTokenIDs    []uuid.UUID
	revokedAllForUsers []uuid.UUID
}

func (f *fakeTokenStore) GetByHash(_ context.Context, _ string) (*models.RefreshToken, error) {
	return f.stored, nil
}

func (f *fakeTokenStore) RevokeToken(_ context.Context, id uuid.UUID) error {
	f.revokedTokenIDs = append(f.revokedTokenIDs, id)
	return nil
}

func (f *fakeTokenStore) RevokeAllForUser(_ context.Context, id uuid.UUID) error {
	f.revokedAllForUsers = append(f.revokedAllForUsers, id)
	return nil
}

func (f *fakeTokenStore) CreateRefreshToken(_ context.Context, _ *models.RefreshToken) error {
	f.refreshTokensMade++
	return nil
}

type fakeOTPStore struct {
	loginOTPStore
	otp      *models.LoginOTP
	consumed []uuid.UUID
}

func (f *fakeOTPStore) GetLatestUnconsumed(_ context.Context, _ string) (*models.LoginOTP, error) {
	return f.otp, nil
}

func (f *fakeOTPStore) MarkConsumed(_ context.Context, id uuid.UUID) error {
	f.consumed = append(f.consumed, id)
	return nil
}

func (f *fakeOTPStore) IncrementAttempts(_ context.Context, _ uuid.UUID) (int, error) {
	return 1, nil
}

type fakeProfileStore struct{}

func (fakeProfileStore) Create(_ context.Context, userID uuid.UUID, role models.Role, st models.OnboardingStatus) (*models.Profile, error) {
	return &models.Profile{ID: uuid.New(), UserID: userID, Role: role, OnboardingStatus: st}, nil
}

type fakeOTPSender struct{}

func (fakeOTPSender) SendLoginOTP(_, _ string) (*email.OTPSendResult, error) {
	return &email.OTPSendResult{}, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────
// (discardLogger comes from admin_qa_round_test.go — same package)

func testUser(blocked bool) *models.User {
	return &models.User{
		ID:               uuid.New(),
		Email:            "blocked@example.com",
		Role:             models.RoleDriver,
		FirstName:        "B",
		LastName:         "U",
		OnboardingStatus: models.OnboardingComplete,
		IsBlocked:        blocked,
	}
}

func decodeErrCode(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("body is not the API error envelope: %v (%s)", err, body)
	}
	return resp.Error.Code
}

// ─── OTP re-login ─────────────────────────────────────────────────────────────

const testOTPCode = "123456"

func newOTPHandlerForUser(user *models.User) (*OTPAuthHandler, *fakeTokenStore) {
	tokens := &fakeTokenStore{}
	otps := &fakeOTPStore{otp: &models.LoginOTP{
		ID:        uuid.New(),
		Email:     "blocked@example.com",
		CodeHash:  auth.HashOTP(testOTPCode),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}}
	h := &OTPAuthHandler{
		userRepo:    &fakeUserStore{user: user},
		tokenRepo:   tokens,
		otpRepo:     otps,
		profileRepo: fakeProfileStore{},
		jwtSvc:      auth.NewJWTService("test-secret", 15*time.Minute, time.Hour),
		otpSender:   fakeOTPSender{},
		logger:      discardLogger(),
	}
	return h, tokens
}

func postOTPVerify(t *testing.T, h *OTPAuthHandler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/otp/verify",
		strings.NewReader(`{"email":"blocked@example.com","code":"`+testOTPCode+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.VerifyOTP(rr, req)
	return rr
}

// The re-entry hole the client can hit: block a user, user requests a fresh
// OTP, logs straight back in. Must now be 403 ACCOUNT_BLOCKED with no tokens
// minted — even though the OTP itself is perfectly valid.
func TestVerifyOTP_BlockedUser_RefusedWithoutTokens(t *testing.T) {
	h, tokens := newOTPHandlerForUser(testUser(true))

	rr := postOTPVerify(t, h)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	if code := decodeErrCode(t, rr.Body.String()); code != models.ErrCodeAccountBlocked {
		t.Fatalf("error code = %q, want %q", code, models.ErrCodeAccountBlocked)
	}
	if tokens.refreshTokensMade != 0 {
		t.Fatalf("refresh tokens minted for a blocked user: %d", tokens.refreshTokensMade)
	}
}

// Paired happy path: the same rig with an unblocked user must log in, which
// proves the 403 above comes from the block check and not a broken fixture.
func TestVerifyOTP_UnblockedUser_LogsIn(t *testing.T) {
	h, tokens := newOTPHandlerForUser(testUser(false))

	rr := postOTPVerify(t, h)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Kind        string `json:"kind"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Kind != "login" || resp.AccessToken == "" {
		t.Fatalf("kind=%q accessToken empty=%v, want login/non-empty", resp.Kind, resp.AccessToken == "")
	}
	if tokens.refreshTokensMade != 1 {
		t.Fatalf("refresh tokens minted = %d, want 1", tokens.refreshTokensMade)
	}
}

// ─── Refresh-token rotation ──────────────────────────────────────────────────

func newAuthHandlerForRefresh(user *models.User) (*AuthHandler, *fakeTokenStore, *models.RefreshToken) {
	stored := &models.RefreshToken{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: "irrelevant-for-fake",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	tokens := &fakeTokenStore{stored: stored}
	h := &AuthHandler{
		userRepo:  &fakeUserStore{user: user},
		tokenRepo: tokens,
		jwtSvc:    auth.NewJWTService("test-secret", 15*time.Minute, time.Hour),
		logger:    discardLogger(),
	}
	return h, tokens, stored
}

func postRefresh(t *testing.T, h *AuthHandler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token/refresh",
		strings.NewReader(`{"refresh_token":"some-valid-refresh-token"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.RefreshToken(rr, req)
	return rr
}

// A blocked user holding an unexpired refresh token must not be able to mint
// a new session. The presented token is burned by rotation before the
// rejection, so retrying is also dead.
func TestRefreshToken_BlockedUser_RefusedAndTokenBurned(t *testing.T) {
	user := testUser(true)
	h, tokens, stored := newAuthHandlerForRefresh(user)

	rr := postRefresh(t, h)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	if code := decodeErrCode(t, rr.Body.String()); code != models.ErrCodeAccountBlocked {
		t.Fatalf("error code = %q, want %q", code, models.ErrCodeAccountBlocked)
	}
	if tokens.refreshTokensMade != 0 {
		t.Fatalf("new refresh tokens minted for a blocked user: %d", tokens.refreshTokensMade)
	}
	if len(tokens.revokedTokenIDs) != 1 || tokens.revokedTokenIDs[0] != stored.ID {
		t.Fatalf("presented token not burned: revoked=%v", tokens.revokedTokenIDs)
	}
}

func TestRefreshToken_UnblockedUser_Rotates(t *testing.T) {
	h, tokens, _ := newAuthHandlerForRefresh(testUser(false))

	rr := postRefresh(t, h)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if tokens.refreshTokensMade != 1 {
		t.Fatalf("refresh tokens minted = %d, want 1", tokens.refreshTokensMade)
	}
}
