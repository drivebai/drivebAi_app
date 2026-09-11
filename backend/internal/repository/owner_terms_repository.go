package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
)

// OwnerTermsRepository records an owner's acceptance of the rolling-rental
// owner package, verbatim, once per version.
type OwnerTermsRepository struct {
	db *database.DB
}

func NewOwnerTermsRepository(db *database.DB) *OwnerTermsRepository {
	return &OwnerTermsRepository{db: db}
}

// Accepted reports whether the owner has accepted this exact version, and
// when.
func (r *OwnerTermsRepository) Accepted(ctx context.Context, ownerID uuid.UUID, version string) (bool, *time.Time, error) {
	var at time.Time
	err := r.db.Pool.QueryRow(ctx, `
		SELECT accepted_at FROM owner_terms_acceptances
		WHERE owner_id = $1 AND terms_version = $2`, ownerID, version).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("owner terms accepted: %w", err)
	}
	return true, &at, nil
}

// Record stores the acceptance. Idempotent per (owner, version): a repeat
// returns created=false and changes nothing — the first record is the
// evidence and stays.
func (r *OwnerTermsRepository) Record(ctx context.Context, ownerID uuid.UUID, version, text, channel string, recordedBy *uuid.UUID, note *string) (created bool, err error) {
	tag, err := r.db.Pool.Exec(ctx, `
		INSERT INTO owner_terms_acceptances (owner_id, terms_version, terms_text, channel, recorded_by, note)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (owner_id, terms_version) DO NOTHING`,
		ownerID, version, text, channel, recordedBy, note)
	if err != nil {
		return false, fmt.Errorf("record owner terms: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
