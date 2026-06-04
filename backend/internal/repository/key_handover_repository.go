package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// KeyHandoverRepository handles the key-handover state machine.
type KeyHandoverRepository struct {
	db *database.DB
}

func NewKeyHandoverRepository(db *database.DB) *KeyHandoverRepository {
	return &KeyHandoverRepository{db: db}
}

const keyHandoverColumns = `
	id, lease_request_id, car_id, owner_id, driver_id,
	pickup_latitude, pickup_longitude, pickup_area, status,
	owner_confirmed_at, driver_confirmed_at, confirmation_deadline, started_at,
	created_at, updated_at`

func scanKeyHandover(row scanRow) (*models.KeyHandover, error) {
	var k models.KeyHandover
	err := row.Scan(
		&k.ID, &k.LeaseRequestID, &k.CarID, &k.OwnerID, &k.DriverID,
		&k.PickupLatitude, &k.PickupLongitude, &k.PickupArea, &k.Status,
		&k.OwnerConfirmedAt, &k.DriverConfirmedAt, &k.ConfirmationDeadline, &k.StartedAt,
		&k.CreatedAt, &k.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// CreateForLease inserts a pending handover for a paid lease request.
// Idempotent: a second call for the same lease_request_id returns the existing row.
func (r *KeyHandoverRepository) CreateForLease(ctx context.Context, lr *models.LeaseRequest, lat, lng *float64, area *string) (*models.KeyHandover, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO key_handovers
			(id, lease_request_id, car_id, owner_id, driver_id, pickup_latitude, pickup_longitude, pickup_area, status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, 'pending', NOW(), NOW())
		ON CONFLICT (lease_request_id) DO NOTHING
		RETURNING `+keyHandoverColumns,
		lr.ID, lr.ListingID, lr.OwnerID, lr.DriverID, lat, lng, area,
	)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		// A handover already exists for this lease (idempotent path).
		return r.GetByLeaseRequestID(ctx, lr.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("create key handover: %w", err)
	}
	return kh, nil
}

// GetByID fetches a handover by its ID.
func (r *KeyHandoverRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.KeyHandover, error) {
	row := r.db.Pool.QueryRow(ctx, `SELECT `+keyHandoverColumns+` FROM key_handovers WHERE id = $1`, id)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		return nil, models.ErrKeyHandoverNotFound
	}
	if err != nil {
		return nil, err
	}
	return kh, nil
}

// GetByIDForUser fetches a handover and verifies the user is a participant.
func (r *KeyHandoverRepository) GetByIDForUser(ctx context.Context, id, userID uuid.UUID) (*models.KeyHandover, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+keyHandoverColumns+` FROM key_handovers WHERE id = $1 AND (owner_id = $2 OR driver_id = $2)`, id, userID)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		return nil, models.ErrKeyHandoverNotFound
	}
	if err != nil {
		return nil, err
	}
	return kh, nil
}

// GetByLeaseRequestID fetches the handover linked to a lease request.
func (r *KeyHandoverRepository) GetByLeaseRequestID(ctx context.Context, leaseRequestID uuid.UUID) (*models.KeyHandover, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+keyHandoverColumns+` FROM key_handovers WHERE lease_request_id = $1`, leaseRequestID)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		return nil, models.ErrKeyHandoverNotFound
	}
	if err != nil {
		return nil, err
	}
	return kh, nil
}

// ListActiveForUser returns pending + owner_confirmed handovers for a participant.
func (r *KeyHandoverRepository) ListActiveForUser(ctx context.Context, userID uuid.UUID) ([]models.KeyHandover, error) {
	rows, err := r.db.Pool.Query(ctx,
		`SELECT `+keyHandoverColumns+`
		 FROM key_handovers
		 WHERE (owner_id = $1 OR driver_id = $1) AND status IN ('pending', 'owner_confirmed')
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list active key handovers: %w", err)
	}
	defer rows.Close()

	out := []models.KeyHandover{}
	for rows.Next() {
		kh, err := scanKeyHandover(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *kh)
	}
	return out, nil
}

// OwnerConfirm transitions pending → owner_confirmed and sets the driver's deadline.
// Guarded so only the owner, only from pending, can perform it.
func (r *KeyHandoverRepository) OwnerConfirm(ctx context.Context, id, ownerID uuid.UUID, deadline time.Time) (*models.KeyHandover, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE key_handovers
		SET status = 'owner_confirmed', owner_confirmed_at = NOW(), confirmation_deadline = $3, updated_at = NOW()
		WHERE id = $1 AND owner_id = $2 AND status = 'pending'
		RETURNING `+keyHandoverColumns, id, ownerID, deadline)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		return nil, models.ErrInvalidHandoverAction
	}
	if err != nil {
		return nil, fmt.Errorf("owner confirm key handover: %w", err)
	}
	return kh, nil
}

// DriverConfirm transitions owner_confirmed → completed, but only before the
// deadline. Sets started_at = NOW() (the rental clock start).
func (r *KeyHandoverRepository) DriverConfirm(ctx context.Context, id, driverID uuid.UUID) (*models.KeyHandover, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE key_handovers
		SET status = 'completed', driver_confirmed_at = NOW(), started_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND driver_id = $2 AND status = 'owner_confirmed' AND confirmation_deadline > NOW()
		RETURNING `+keyHandoverColumns, id, driverID)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		return nil, models.ErrInvalidHandoverAction
	}
	if err != nil {
		return nil, fmt.Errorf("driver confirm key handover: %w", err)
	}
	return kh, nil
}

// Expire transitions owner_confirmed → expired when the deadline has passed.
// Guarded so concurrent reads only expire the row once. The bool reports whether
// THIS call performed the transition (true), so the caller can notify exactly once.
func (r *KeyHandoverRepository) Expire(ctx context.Context, id uuid.UUID) (*models.KeyHandover, bool, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE key_handovers
		SET status = 'expired', updated_at = NOW()
		WHERE id = $1 AND status = 'owner_confirmed' AND confirmation_deadline <= NOW()
		RETURNING `+keyHandoverColumns, id)
	kh, err := scanKeyHandover(row)
	if err == pgx.ErrNoRows {
		// Another request already transitioned it (or it's no longer overdue/owner_confirmed).
		current, gErr := r.GetByID(ctx, id)
		return current, false, gErr
	}
	if err != nil {
		return nil, false, fmt.Errorf("expire key handover: %w", err)
	}
	return kh, true, nil
}
