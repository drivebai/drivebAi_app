package handlers

import (
	"context"
	"time"

	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// Narrow store interfaces for the two auth handlers. The concrete
// repositories satisfy them implicitly (see main.go wiring); tests substitute
// fakes so security-critical branches — blocked-account rejection on refresh
// and on OTP re-login — are exercised through the real handlers over
// httptest instead of being asserted on source text.

type authUserStore interface {
	Create(ctx context.Context, user *models.User) error
	GetByID(ctx context.Context, id uuid.UUID) (*models.User, error)
	GetByEmail(ctx context.Context, email string) (*models.User, error)
	EmailExists(ctx context.Context, email string) (bool, error)
	PhoneExists(ctx context.Context, phone string) (bool, error)
	UpdatePassword(ctx context.Context, userID uuid.UUID, passwordHash string) error
	SetActiveProfile(ctx context.Context, userID uuid.UUID, profileID uuid.UUID, role models.Role) error
	GetOTPSendCount(ctx context.Context, email string, since time.Time) (int, error)
	RecordOTPSend(ctx context.Context, email, ipAddress string) error
}

type authTokenStore interface {
	CreateRefreshToken(ctx context.Context, token *models.RefreshToken) error
	GetByHash(ctx context.Context, tokenHash string) (*models.RefreshToken, error)
	RevokeToken(ctx context.Context, tokenID uuid.UUID) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
	CreatePasswordResetToken(ctx context.Context, token *models.PasswordResetToken) error
	GetPasswordResetTokenByHash(ctx context.Context, tokenHash string) (*models.PasswordResetToken, error)
	MarkPasswordResetTokenUsed(ctx context.Context, tokenID uuid.UUID) error
	InvalidatePasswordResetTokensForUser(ctx context.Context, userID uuid.UUID) error
}

type authProfileStore interface {
	Create(ctx context.Context, userID uuid.UUID, role models.Role, onboardingStatus models.OnboardingStatus) (*models.Profile, error)
}

type loginOTPStore interface {
	Create(ctx context.Context, email, codeHash string, expiresAt time.Time, ipAddress, userAgent string) (*models.LoginOTP, error)
	GetLatestUnconsumed(ctx context.Context, email string) (*models.LoginOTP, error)
	IncrementAttempts(ctx context.Context, id uuid.UUID) (int, error)
	MarkConsumed(ctx context.Context, id uuid.UUID) error
	SetMessageID(ctx context.Context, id uuid.UUID, messageID string) error
}
