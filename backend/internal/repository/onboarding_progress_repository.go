package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// OnboardingProgressRepository persists per-user product-tour progress
// (user_onboarding_progress, migration 000034).
//
// Every method is scoped by userID and NEVER accepts a target user from the
// caller other than the authenticated one — the handler passes the JWT user
// id, so a user can only read/write their own rows.
type OnboardingProgressRepository struct {
	db *database.DB
}

func NewOnboardingProgressRepository(db *database.DB) *OnboardingProgressRepository {
	return &OnboardingProgressRepository{db: db}
}

// DeleteForUser removes every tour row belonging to one user. Scoped by
// user_id, so it can never touch another account's progress.
func (r *OnboardingProgressRepository) DeleteForUser(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.db.Pool.Exec(ctx, `
		DELETE FROM user_onboarding_progress WHERE user_id = $1
	`, userID); err != nil {
		return fmt.Errorf("delete onboarding progress: %w", err)
	}
	return nil
}

// ListForUser returns all tour rows for a single user, newest-updated first.
func (r *OnboardingProgressRepository) ListForUser(ctx context.Context, userID uuid.UUID) ([]models.TourProgress, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT user_id, tour_key, status, step, created_at, updated_at
		FROM user_onboarding_progress
		WHERE user_id = $1
		ORDER BY updated_at DESC, tour_key ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list onboarding progress: %w", err)
	}
	defer rows.Close()

	out := []models.TourProgress{}
	for rows.Next() {
		var t models.TourProgress
		if err := rows.Scan(&t.UserID, &t.TourKey, &t.Status, &t.Step, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan onboarding progress: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate onboarding progress: %w", err)
	}
	return out, nil
}

// UpsertMany merge-upserts each entry for the given user in a single
// transaction, then returns the user's full, freshly-read row set. The
// user_id is bound server-side from the authenticated caller — the entries
// carry no user_id — so cross-user writes are structurally impossible.
func (r *OnboardingProgressRepository) UpsertMany(ctx context.Context, userID uuid.UUID, entries []models.UpsertTourProgressEntry) ([]models.TourProgress, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	for _, e := range entries {
		step := 0
		if e.Step != nil {
			step = *e.Step
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_onboarding_progress (user_id, tour_key, status, step, created_at, updated_at)
			VALUES ($1, $2, $3, $4, NOW(), NOW())
			ON CONFLICT (user_id, tour_key) DO UPDATE
			SET status = EXCLUDED.status, step = EXCLUDED.step, updated_at = NOW()
		`, userID, e.TourKey, string(e.Status), step); err != nil {
			return nil, fmt.Errorf("upsert onboarding progress: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return r.ListForUser(ctx, userID)
}
