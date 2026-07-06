package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Login email normalization (QA pt-2 / D7): the login path must TrimSpace
// the email (parity with the OTP path) so " user@x.com " authenticates.
// Full-credential round-trips need a DB; what we can pin without one is
// that the trim actually runs before validation — a whitespace-only email
// must be rejected as empty (400) instead of proceeding to a repo lookup
// (which, pre-fix, is exactly what happened and produced a 401 for a
// "wrong" email of "   ").
func TestLogin_TrimsEmailBeforeValidation(t *testing.T) {
	h := &AuthHandler{}

	body, _ := json.Marshal(LoginRequest{Email: "   ", Password: "correct-horse"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("whitespace-only email must 400 after trim, got %d", rr.Code)
	}
}

// NOTE: "password is NOT trimmed" is asserted by inspection of Login (only
// req.Email is normalized) — a behavioral test would need a live user repo
// to reach the credential check, which this suite deliberately avoids.
