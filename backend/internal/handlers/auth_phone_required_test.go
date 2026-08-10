package handlers

import (
	"strings"
	"testing"

	"github.com/drivebai/backend/internal/models"
)

// Phone is required at signup (7/31 batch item 3) on BOTH registration entry
// points — password register and OTP complete-registration. Existing
// NULL-phone accounts are deliberately untouched: login never reads phone and
// the 000041 unique index is partial, so these tests pin only the signup gate.

func TestValidateRegisterRequest_PhoneRequired(t *testing.T) {
	h := &AuthHandler{}
	base := RegisterRequest{
		Email: "new@example.com", Password: "longenough",
		FirstName: "New", LastName: "User", Role: models.RoleDriver,
	}

	missing := base
	if err := h.validateRegisterRequest(&missing); err == nil || !strings.Contains(err.Message, "Phone") {
		t.Fatalf("empty phone must be rejected, got %v", err)
	}

	bad := base
	bad.Phone = "12345"
	if err := h.validateRegisterRequest(&bad); err == nil {
		t.Fatal("non-E.164 phone must be rejected")
	}

	ok := base
	ok.Phone = "+13475551234"
	if err := h.validateRegisterRequest(&ok); err != nil {
		t.Fatalf("valid phone must pass, got %v", err)
	}
}

func TestValidateCompleteRegistration_PhoneRequired(t *testing.T) {
	base := CompleteRegistrationRequest{
		FirstName: "New", LastName: "User",
		Password: "longenough", Role: models.RoleDriver,
	}

	missing := base
	if err := validateCompleteRegistration(&missing); err == nil || !strings.Contains(err.Message, "Phone") {
		t.Fatalf("empty phone must be rejected, got %v", err)
	}

	ok := base
	ok.Phone = "+13475551234"
	if err := validateCompleteRegistration(&ok); err != nil {
		t.Fatalf("valid phone must pass, got %v", err)
	}
}
