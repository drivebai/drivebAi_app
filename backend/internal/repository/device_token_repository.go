package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

type DeviceTokenRepository struct {
	db *database.DB
}

func NewDeviceTokenRepository(db *database.DB) *DeviceTokenRepository {
	return &DeviceTokenRepository{db: db}
}

// Upsert inserts or updates a device token (unique on token).
// If the token already exists for a different user (shared device), it is re-assigned.
func (r *DeviceTokenRepository) Upsert(ctx context.Context, userID uuid.UUID, token, platform string, sandbox bool) error {
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO device_tokens (id, user_id, token, platform, sandbox, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (token) DO UPDATE
		    SET user_id = EXCLUDED.user_id,
		        platform = EXCLUDED.platform,
		        sandbox  = EXCLUDED.sandbox
	`, uuid.New(), userID, token, platform, sandbox, time.Now().UTC())
	return err
}

// Delete removes a device token for a user.
func (r *DeviceTokenRepository) Delete(ctx context.Context, userID uuid.UUID, token string) error {
	_, err := r.db.Pool.Exec(ctx, `
		DELETE FROM device_tokens WHERE user_id = $1 AND token = $2
	`, userID, token)
	return err
}

// DeleteByToken removes any device token row matching the given APNs token,
// regardless of user. Called by the push service when APNs reports the token
// as Unregistered (410) or BadDeviceToken (400) — those rows are dead and
// must be pruned so we don't keep sending to a destination that will never
// deliver. Uses a fresh background context (the call originates from the
// push fan-out goroutine, which has no inherited request context).
func (r *DeviceTokenRepository) DeleteByToken(token string) error {
	if token == "" {
		return nil
	}
	_, err := r.db.Pool.Exec(context.Background(), `
		DELETE FROM device_tokens WHERE token = $1
	`, token)
	return err
}

// ListByUser returns all device tokens for a given user.
func (r *DeviceTokenRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]models.DeviceToken, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, user_id, token, platform, sandbox, created_at
		FROM device_tokens WHERE user_id = $1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.DeviceToken
	for rows.Next() {
		var dt models.DeviceToken
		if err := rows.Scan(&dt.ID, &dt.UserID, &dt.Token, &dt.Platform, &dt.Sandbox, &dt.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, dt)
	}
	return out, rows.Err()
}
