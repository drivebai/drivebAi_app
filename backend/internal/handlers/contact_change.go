package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/email"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// OTP-confirmed email/phone changes (batch items 7+8). NOTHING is committed
// until the code verifies. Delivery is email-only:
//   - email change → code goes to the NEW address (proves ownership of it);
//   - phone change → code goes to the account's CURRENT email (proves
//     account control — possession-proof of the phone itself would need an
//     SMS provider, which the stack doesn't have).
//
// Deliberately a separate table/flow from login OTPs: login_otps is keyed by
// bare email with no purpose column, and its verify path mints registration
// tokens for unknown addresses — blind reuse would cross-contaminate flows.

// Narrow store interfaces so tests substitute fakes (reviewStore pattern).
type contactChangeStore interface {
	Create(ctx context.Context, o *repository.ContactChangeOTP) error
	GetLatestUnconsumed(ctx context.Context, userID uuid.UUID) (*repository.ContactChangeOTP, error)
	IncrementAttempts(ctx context.Context, id uuid.UUID) error
	MarkConsumed(ctx context.Context, id uuid.UUID) error
}

type contactChangeUserStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*models.User, error)
	Update(ctx context.Context, u *models.User) error
	EmailExists(ctx context.Context, email string) (bool, error)
	PhoneExistsExcludingUser(ctx context.Context, phone string, excludeID uuid.UUID) (bool, error)
	GetOTPSendCount(ctx context.Context, email string, since time.Time) (int, error)
	RecordOTPSend(ctx context.Context, email, ipAddress string) error
}

type ContactChangeHandler struct {
	users     contactChangeUserStore
	changes   contactChangeStore
	otpSender email.OTPSender
	logger    *slog.Logger
}

func NewContactChangeHandler(users *repository.UserRepository, changes *repository.ContactChangeRepository, otpSender email.OTPSender, logger *slog.Logger) *ContactChangeHandler {
	return &ContactChangeHandler{users: users, changes: changes, otpSender: otpSender, logger: logger}
}

// Same budget as the login-OTP flow.
const (
	contactChangeExpiry      = 10 * time.Minute
	contactChangeMaxSends    = 5
	contactChangeSendWindow  = 15 * time.Minute
	contactChangeMaxAttempts = 5
)

type contactChangeRequestBody struct {
	Field    string `json:"field"` // "email" | "phone"
	NewValue string `json:"new_value"`
}

// Request — POST /api/v1/profile/contact-change/request.
func (h *ContactChangeHandler) Request(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	var body contactChangeRequestBody
	if err := DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	user, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var (
		newValue   string // normalized value that will be committed
		deliverTo  string // where the code goes
		changeDesc string // human phrase for the email body
	)

	switch body.Field {
	case "email":
		newValue = strings.ToLower(strings.TrimSpace(body.NewValue))
		if newValue == "" || !strings.Contains(newValue, "@") {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid email format"))
			return
		}
		if newValue == strings.ToLower(user.Email) {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("That's already your email address"))
			return
		}
		if taken, terr := h.users.EmailExists(r.Context(), newValue); terr == nil && taken {
			httputil.WriteError(w, http.StatusConflict, models.ErrEmailTaken)
			return
		}
		deliverTo = newValue // prove ownership of the NEW address
		changeDesc = "change your DrivaBai email to " + newValue
	case "phone":
		normalized, pok := models.NormalizePhone(body.NewValue)
		if !pok {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Phone must include the country code, e.g. +1 347 555 1234"))
			return
		}
		if user.Phone != nil && *user.Phone == normalized {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("That's already your phone number"))
			return
		}
		if taken, terr := h.users.PhoneExistsExcludingUser(r.Context(), normalized, userID); terr == nil && taken {
			httputil.WriteError(w, http.StatusConflict, models.ErrPhoneTaken)
			return
		}
		newValue = normalized
		deliverTo = user.Email // account control; no SMS capability exists
		changeDesc = "change your DrivaBai phone number to " + normalized
	default:
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("field must be 'email' or 'phone'"))
		return
	}

	// Rate-limit on the delivery address, same budget as login OTPs.
	if count, cerr := h.users.GetOTPSendCount(r.Context(), deliverTo, time.Now().Add(-contactChangeSendWindow)); cerr == nil && count >= contactChangeMaxSends {
		httputil.WriteError(w, http.StatusTooManyRequests, models.NewAPIError("RATE_LIMITED", "Too many codes requested — try again in a few minutes"))
		return
	}

	code, codeHash, err := auth.GenerateOTP()
	if err != nil {
		h.logger.Error("contact change: generate otp", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	otp := &repository.ContactChangeOTP{
		UserID:    userID,
		Field:     body.Field,
		NewValue:  newValue,
		CodeHash:  codeHash,
		ExpiresAt: time.Now().Add(contactChangeExpiry),
	}
	if err := h.changes.Create(r.Context(), otp); err != nil {
		h.logger.Error("contact change: store otp", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	_ = h.users.RecordOTPSend(r.Context(), deliverTo, r.RemoteAddr)

	if _, err := h.otpSender.SendContactChangeOTP(deliverTo, code, changeDesc); err != nil {
		h.logger.Error("contact change: send otp email", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("EMAIL_SEND_FAILED", "Couldn't send the confirmation code — try again"))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "sent_to": deliverTo, "field": body.Field})
}

type contactChangeVerifyBody struct {
	Code string `json:"code"`
}

// Verify — POST /api/v1/profile/contact-change/verify. On a matching code
// the change commits atomically (uniqueness re-checked as the race
// backstop) and the updated profile is returned in the UpdateProfile shape.
func (h *ContactChangeHandler) Verify(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	var body contactChangeVerifyBody
	if err := DecodeJSON(r, &body); err != nil || !auth.ValidateOTPFormat(body.Code) {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Enter the 6-digit code"))
		return
	}

	otp, err := h.changes.GetLatestUnconsumed(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrOTPInvalid)
		return
	}
	if time.Now().After(otp.ExpiresAt) {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrOTPExpired)
		return
	}
	if otp.Attempts >= contactChangeMaxAttempts {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrOTPAttemptsExceeded)
		return
	}
	if auth.HashOTP(body.Code) != otp.CodeHash {
		_ = h.changes.IncrementAttempts(r.Context(), otp.ID)
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrOTPInvalid)
		return
	}

	if err := h.changes.MarkConsumed(r.Context(), otp.ID); err != nil {
		h.logger.Error("contact change: mark consumed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	user, err := h.users.GetByID(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	// Race backstop: the identifier may have been claimed since Request.
	switch otp.Field {
	case "email":
		if taken, terr := h.users.EmailExists(r.Context(), otp.NewValue); terr == nil && taken {
			httputil.WriteError(w, http.StatusConflict, models.ErrEmailTaken)
			return
		}
		user.Email = otp.NewValue
		// The code was delivered to this address — ownership is proven.
		user.IsEmailVerified = true
	case "phone":
		if taken, terr := h.users.PhoneExistsExcludingUser(r.Context(), otp.NewValue, userID); terr == nil && taken {
			httputil.WriteError(w, http.StatusConflict, models.ErrPhoneTaken)
			return
		}
		v := otp.NewValue
		user.Phone = &v
	}

	if err := h.users.Update(r.Context(), user); err != nil {
		// The DB unique constraints are the final backstop — map them to
		// the same 409s the pre-checks produce.
		msg := err.Error()
		switch {
		case strings.Contains(msg, "users_email_key"):
			httputil.WriteError(w, http.StatusConflict, models.ErrEmailTaken)
		case strings.Contains(msg, "users_phone_unique_idx"):
			httputil.WriteError(w, http.StatusConflict, models.ErrPhoneTaken)
		default:
			h.logger.Error("contact change: commit", "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	profile := UserProfile{
		ID:               user.ID,
		Email:            user.Email,
		Role:             user.Role,
		FirstName:        user.FirstName,
		LastName:         user.LastName,
		Phone:            user.Phone,
		IsEmailVerified:  user.IsEmailVerified,
		OnboardingStatus: user.OnboardingStatus,
		ProfilePhotoURL:  user.ProfilePhotoURL,
	}
	WriteSuccess(w, http.StatusOK, "Profile updated successfully", profile)
}
