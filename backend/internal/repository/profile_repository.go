package repository

import (
	"context"
	"errors"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ProfileRepository struct {
	db *database.DB
}

func NewProfileRepository(db *database.DB) *ProfileRepository {
	return &ProfileRepository{db: db}
}

// Create inserts a new profile. If a profile already exists for the
// (user_id, role) pair, returns the existing row instead (idempotent).
func (r *ProfileRepository) Create(ctx context.Context, userID uuid.UUID, role models.Role, onboardingStatus models.OnboardingStatus) (*models.Profile, error) {
	if onboardingStatus == "" {
		onboardingStatus = models.OnboardingRoleSelected
	}

	query := `
		INSERT INTO user_profiles (user_id, role, onboarding_status)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, role) DO UPDATE
			SET updated_at = user_profiles.updated_at
		RETURNING id, user_id, role, onboarding_status, created_at, updated_at
	`

	p := &models.Profile{}
	err := r.db.Pool.QueryRow(ctx, query, userID, role, onboardingStatus).Scan(
		&p.ID, &p.UserID, &p.Role, &p.OnboardingStatus, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *ProfileRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.Profile, error) {
	query := `
		SELECT id, user_id, role, onboarding_status, created_at, updated_at
		FROM user_profiles
		WHERE id = $1
	`
	p := &models.Profile{}
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&p.ID, &p.UserID, &p.Role, &p.OnboardingStatus, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

func (r *ProfileRepository) GetByUserIDAndRole(ctx context.Context, userID uuid.UUID, role models.Role) (*models.Profile, error) {
	query := `
		SELECT id, user_id, role, onboarding_status, created_at, updated_at
		FROM user_profiles
		WHERE user_id = $1 AND role = $2
	`
	p := &models.Profile{}
	err := r.db.Pool.QueryRow(ctx, query, userID, role).Scan(
		&p.ID, &p.UserID, &p.Role, &p.OnboardingStatus, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return p, nil
}

func (r *ProfileRepository) ListByUserID(ctx context.Context, userID uuid.UUID) ([]*models.Profile, error) {
	query := `
		SELECT id, user_id, role, onboarding_status, created_at, updated_at
		FROM user_profiles
		WHERE user_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.db.Pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []*models.Profile
	for rows.Next() {
		p := &models.Profile{}
		if err := rows.Scan(&p.ID, &p.UserID, &p.Role, &p.OnboardingStatus, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func (r *ProfileRepository) UpdateOnboardingStatus(ctx context.Context, id uuid.UUID, status models.OnboardingStatus) error {
	query := `UPDATE user_profiles SET onboarding_status = $2, updated_at = NOW() WHERE id = $1`
	result, err := r.db.Pool.Exec(ctx, query, id, status)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
