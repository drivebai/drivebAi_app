package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
)

// ContactChangeOTP is one pending email/phone change awaiting its code
// (batch items 7+8). Mirrors the login_otps shape, but user-bound and
// single-purpose — see migration 000045 for why login_otps is not reused.
type ContactChangeOTP struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Field      string // "email" | "phone"
	NewValue   string
	CodeHash   string
	ExpiresAt  time.Time
	Attempts   int
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

type ContactChangeRepository struct {
	db *database.DB
}

func NewContactChangeRepository(db *database.DB) *ContactChangeRepository {
	return &ContactChangeRepository{db: db}
}

func (r *ContactChangeRepository) Create(ctx context.Context, o *ContactChangeOTP) error {
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO contact_change_otps (id, user_id, field, new_value, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, o.ID, o.UserID, o.Field, o.NewValue, o.CodeHash, o.ExpiresAt)
	return err
}

// GetLatestUnconsumed returns the user's newest pending change — issuing a
// new code supersedes older rows exactly like the login-OTP flow.
func (r *ContactChangeRepository) GetLatestUnconsumed(ctx context.Context, userID uuid.UUID) (*ContactChangeOTP, error) {
	var o ContactChangeOTP
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, user_id, field, new_value, code_hash, expires_at, attempts, consumed_at, created_at
		FROM contact_change_otps
		WHERE user_id = $1 AND consumed_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1
	`, userID).Scan(&o.ID, &o.UserID, &o.Field, &o.NewValue, &o.CodeHash,
		&o.ExpiresAt, &o.Attempts, &o.ConsumedAt, &o.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *ContactChangeRepository) IncrementAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx,
		`UPDATE contact_change_otps SET attempts = attempts + 1 WHERE id = $1`, id)
	return err
}

func (r *ContactChangeRepository) MarkConsumed(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx,
		`UPDATE contact_change_otps SET consumed_at = NOW() WHERE id = $1`, id)
	return err
}
