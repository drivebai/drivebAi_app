package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// VehicleReturnRepository handles the vehicle-return state machine and the
// partial-refund accounting tied to lease_requests / payments / cars.
type VehicleReturnRepository struct {
	db *database.DB
}

func NewVehicleReturnRepository(db *database.DB) *VehicleReturnRepository {
	return &VehicleReturnRepository{db: db}
}

// Column list kept in one place so each scanner stays in sync. Mirrors the
// shape used by KeyHandoverRepository.
const vehicleReturnColumns = `
	id, lease_request_id, car_id, owner_id, driver_id, status,
	driver_initiated_at, owner_confirmed_at, disputed_at, completed_at, cancelled_at,
	pickup_confirmed_at, returned_at, rental_weeks, paid_amount_cents,
	used_days, refund_amount_cents, refund_id, refund_status, refunded_at, refund_failure_reason,
	dispute_reason, dispute_resolved_by,
	created_at, updated_at`

func scanVehicleReturn(row scanRow) (*models.VehicleReturn, error) {
	var v models.VehicleReturn
	err := row.Scan(
		&v.ID, &v.LeaseRequestID, &v.CarID, &v.OwnerID, &v.DriverID, &v.Status,
		&v.DriverInitiatedAt, &v.OwnerConfirmedAt, &v.DisputedAt, &v.CompletedAt, &v.CancelledAt,
		&v.PickupConfirmedAt, &v.ReturnedAt, &v.RentalWeeks, &v.PaidAmountCents,
		&v.UsedDays, &v.RefundAmountCents, &v.RefundID, &v.RefundStatus, &v.RefundedAt, &v.RefundFailureReason,
		&v.DisputeReason, &v.DisputeResolvedBy,
		&v.CreatedAt, &v.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// CreateForLeaseParams bundles the precomputed inputs the handler hands to
// the repo. Keeping them as a struct lets callers compute the refund once
// and reuse the same RefundComputation for the row + for the response
// payload.
type CreateForLeaseParams struct {
	LeaseRequestID    uuid.UUID
	CarID             uuid.UUID
	OwnerID           uuid.UUID
	DriverID          uuid.UUID
	PickupConfirmedAt time.Time
	ReturnedAt        time.Time
	RentalWeeks       int
	PaidAmountCents   int64
	UsedDays          int
	RefundAmountCents int64
}

// CreateForLease inserts a fresh driver_initiated return row. Idempotent on
// lease_request_id (UNIQUE in migration 000030) — a second call returns
// the existing row so a double-tap on "I returned the car" never creates
// two parallel returns.
func (r *VehicleReturnRepository) CreateForLease(ctx context.Context, p CreateForLeaseParams) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO vehicle_returns
			(id, lease_request_id, car_id, owner_id, driver_id, status,
			 driver_initiated_at, pickup_confirmed_at, returned_at, rental_weeks, paid_amount_cents,
			 used_days, refund_amount_cents, created_at, updated_at)
		VALUES
			(gen_random_uuid(), $1, $2, $3, $4, 'driver_initiated',
			 NOW(), $5, $6, $7, $8,
			 $9, $10, NOW(), NOW())
		ON CONFLICT (lease_request_id) DO NOTHING
		RETURNING `+vehicleReturnColumns,
		p.LeaseRequestID, p.CarID, p.OwnerID, p.DriverID,
		p.PickupConfirmedAt, p.ReturnedAt, p.RentalWeeks, p.PaidAmountCents,
		p.UsedDays, p.RefundAmountCents,
	)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Idempotent path — a row already exists for this lease.
		return r.GetByLeaseRequestID(ctx, p.LeaseRequestID)
	}
	if err != nil {
		return nil, fmt.Errorf("create vehicle return: %w", err)
	}
	return v, nil
}

// GetByID returns a single return row.
func (r *VehicleReturnRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx, `SELECT `+vehicleReturnColumns+` FROM vehicle_returns WHERE id = $1`, id)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrVehicleReturnNotFound
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// GetByIDForUser returns a return row only if the user is a participant
// (owner or driver). Same shape as KeyHandoverRepository.GetByIDForUser —
// non-participants get NotFound to avoid leaking row existence.
func (r *VehicleReturnRepository) GetByIDForUser(ctx context.Context, id, userID uuid.UUID) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+vehicleReturnColumns+` FROM vehicle_returns WHERE id = $1 AND (owner_id = $2 OR driver_id = $2)`,
		id, userID)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrVehicleReturnNotFound
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// GetByLeaseRequestID returns the return row linked to a lease, if any.
func (r *VehicleReturnRepository) GetByLeaseRequestID(ctx context.Context, leaseRequestID uuid.UUID) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx,
		`SELECT `+vehicleReturnColumns+` FROM vehicle_returns WHERE lease_request_id = $1`, leaseRequestID)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrVehicleReturnNotFound
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ListActiveForUser returns every non-terminal return where the user is a
// participant. Used for GET /vehicle-returns/today.
//
// Also surfaces recently terminal rows (completed/cancelled within the last
// 15 minutes) so a user who is on Today when the WS `vehicle_return_completed`
// event fires sees the "Return complete — $X refunded" confirmation card
// before it ages out. iOS's per-user `dismissedReturnIds` set hides any
// terminal row the user has explicitly dismissed.
func (r *VehicleReturnRepository) ListActiveForUser(ctx context.Context, userID uuid.UUID) ([]models.VehicleReturn, error) {
	rows, err := r.db.Pool.Query(ctx,
		`SELECT `+vehicleReturnColumns+`
		 FROM vehicle_returns
		 WHERE (owner_id = $1 OR driver_id = $1)
		   AND (
		         status IN ('driver_initiated','owner_confirmed','disputed')
		      OR (status = 'completed' AND completed_at >= NOW() - INTERVAL '15 minutes')
		      OR (status = 'cancelled' AND cancelled_at >= NOW() - INTERVAL '15 minutes')
		   )
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list active vehicle returns: %w", err)
	}
	defer rows.Close()

	out := []models.VehicleReturn{}
	for rows.Next() {
		v, err := scanVehicleReturn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// Cancel transitions driver_initiated → cancelled. Guarded so only the
// driver, only within the cancel window, can perform it. Returns the
// updated row on success; ErrInvalidReturnState / ErrReturnCancelExpired
// otherwise.
func (r *VehicleReturnRepository) Cancel(ctx context.Context, id, driverID uuid.UUID) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE vehicle_returns
		SET status = 'cancelled', cancelled_at = NOW(), updated_at = NOW()
		WHERE id = $1
		  AND driver_id = $2
		  AND status = 'driver_initiated'
		  AND driver_initiated_at >= NOW() - $3::interval
		RETURNING `+vehicleReturnColumns,
		id, driverID, fmt.Sprintf("%d seconds", int(models.VehicleReturnDriverCancelWindow.Seconds())))
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Classify the failure for the handler.
		existing, gErr := r.GetByID(ctx, id)
		if gErr != nil {
			return nil, gErr
		}
		if existing.DriverID != driverID {
			return nil, models.ErrInvalidReturnState
		}
		if existing.Status != models.VehicleReturnDriverInitiated {
			return nil, models.ErrInvalidReturnState
		}
		return nil, models.ErrReturnCancelExpired
	}
	if err != nil {
		return nil, fmt.Errorf("cancel vehicle return: %w", err)
	}
	return v, nil
}

// OwnerConfirm transitions driver_initiated → owner_confirmed and stamps
// refund_status='pending' (so the row qualifies for the stuck-refund
// scanner if the Stripe call subsequently fails). The handler immediately
// follows up with a Stripe call and FinalizeRefund / FinalizeNoRefund.
//
// When the computed refund is not applicable (zero-paid lease, sub-cent),
// the handler should call MarkRefundNotApplicable in the same flow.
func (r *VehicleReturnRepository) OwnerConfirm(ctx context.Context, id, ownerID uuid.UUID) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE vehicle_returns
		SET status = 'owner_confirmed',
		    owner_confirmed_at = NOW(),
		    refund_status = CASE
		        WHEN refund_amount_cents > 0 THEN 'pending'::varchar
		        ELSE 'not_applicable'::varchar
		    END,
		    updated_at = NOW()
		WHERE id = $1 AND owner_id = $2 AND status = 'driver_initiated'
		RETURNING `+vehicleReturnColumns,
		id, ownerID)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidReturnState
	}
	if err != nil {
		return nil, fmt.Errorf("owner confirm vehicle return: %w", err)
	}
	return v, nil
}

// Dispute transitions driver_initiated → disputed.
func (r *VehicleReturnRepository) Dispute(ctx context.Context, id, ownerID uuid.UUID, reason string) (*models.VehicleReturn, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE vehicle_returns
		SET status = 'disputed',
		    disputed_at = NOW(),
		    dispute_reason = $3,
		    updated_at = NOW()
		WHERE id = $1 AND owner_id = $2 AND status = 'driver_initiated'
		RETURNING `+vehicleReturnColumns,
		id, ownerID, reason)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidReturnState
	}
	if err != nil {
		return nil, fmt.Errorf("dispute vehicle return: %w", err)
	}
	return v, nil
}

// FinalizeRefund records a successful Stripe refund and flips the return
// row to 'completed'. Same transaction:
//   - sets lease_requests.vehicle_returned_at so other surfaces stop
//     treating the rental as active.
//   - clears cars.reserved_by_lease_request_id (if it still pointed at
//     this lease) so the listing returns to discovery.
//
// Idempotent: invoking twice with the same refundID is a no-op (the row
// is already 'completed' and the second UPDATE matches zero rows).
func (r *VehicleReturnRepository) FinalizeRefund(ctx context.Context, id uuid.UUID, refundID string) (*models.VehicleReturn, error) {
	return r.finalize(ctx, id, &refundID, models.VehicleReturnRefundSucceeded)
}

// FinalizeNoRefund is the zero-refund fast-path: paid_amount_cents was 0
// or the computed refund rounded below 1¢. Flips the row to 'completed'
// with refund_status='not_applicable' and no Stripe call.
func (r *VehicleReturnRepository) FinalizeNoRefund(ctx context.Context, id uuid.UUID) (*models.VehicleReturn, error) {
	return r.finalize(ctx, id, nil, models.VehicleReturnRefundNotApplicable)
}

func (r *VehicleReturnRepository) finalize(ctx context.Context, id uuid.UUID, refundID *string, status models.VehicleReturnRefundStatus) (*models.VehicleReturn, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Flip the return row.
	now := time.Now().UTC()
	var refundedAt *time.Time
	if status == models.VehicleReturnRefundSucceeded {
		refundedAt = &now
	}

	row := tx.QueryRow(ctx, `
		UPDATE vehicle_returns
		SET status = 'completed',
		    completed_at = $2,
		    refund_id = $3,
		    refund_status = $4,
		    refunded_at = $5,
		    refund_failure_reason = NULL,
		    updated_at = $2
		WHERE id = $1
		  AND status IN ('driver_initiated','owner_confirmed','completed')
		RETURNING `+vehicleReturnColumns,
		id, now, refundID, string(status), refundedAt)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Row not in a finalizable state — could be cancelled / disputed.
		return nil, models.ErrInvalidReturnState
	}
	if err != nil {
		return nil, fmt.Errorf("finalize vehicle return: %w", err)
	}

	// Stamp the lease so other Today / Discovery surfaces know the rental
	// is complete. Best-effort guard: don't overwrite an existing value.
	if _, err := tx.Exec(ctx, `
		UPDATE lease_requests
		SET vehicle_returned_at = COALESCE(vehicle_returned_at, $2),
		    updated_at = NOW()
		WHERE id = $1
	`, v.LeaseRequestID, now); err != nil {
		return nil, fmt.Errorf("stamp lease vehicle_returned_at: %w", err)
	}

	// Release the car if it's still reserved by this lease. unreserve is
	// safe regardless of whether another reservation is in play because
	// we only clear the slot when it still matches this lease's ID.
	if _, err := tx.Exec(ctx, `
		UPDATE cars
		SET reserved_by_lease_request_id = NULL
		WHERE reserved_by_lease_request_id = $1
	`, v.LeaseRequestID); err != nil {
		return nil, fmt.Errorf("unreserve car: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return v, nil
}

// MarkRefundFailed records that the Stripe call failed; the row stays at
// 'owner_confirmed' so the stuck-refund scanner picks it up on its next
// tick. Persists the error message for operator visibility.
func (r *VehicleReturnRepository) MarkRefundFailed(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE vehicle_returns
		SET refund_status = 'failed', refund_failure_reason = $2, updated_at = NOW()
		WHERE id = $1
	`, id, reason)
	if err != nil {
		return fmt.Errorf("mark refund failed: %w", err)
	}
	return nil
}

// ListStuckRefunds returns rows whose Stripe refund should have landed but
// hasn't — owner has confirmed, refund_amount_cents > 0, refund_id is
// still NULL. Used by StartReturnRefundScanner. The `staleAfter` cutoff
// gives a freshly-attempted row a quiet window before the scanner replays
// the call.
func (r *VehicleReturnRepository) ListStuckRefunds(ctx context.Context, staleAfter time.Time, limit int) ([]models.VehicleReturn, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+vehicleReturnColumns+`
		FROM vehicle_returns
		WHERE refund_amount_cents > 0
		  AND refund_id IS NULL
		  AND (refund_status IS NULL OR refund_status IN ('pending','failed'))
		  AND status IN ('owner_confirmed','completed')
		  AND updated_at <= $1
		ORDER BY updated_at ASC
		LIMIT $2
	`, staleAfter, limit)
	if err != nil {
		return nil, fmt.Errorf("list stuck vehicle return refunds: %w", err)
	}
	defer rows.Close()

	out := []models.VehicleReturn{}
	for rows.Next() {
		v, err := scanVehicleReturn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// ResolveDispute is the admin-only path off a 'disputed' row.
//
//	resolution = "accept" → flip back to driver_initiated semantics: set
//	    status='owner_confirmed' (skipping the owner's explicit confirm
//	    since admin is acting on their behalf) AND set refund_status to
//	    'pending' / 'not_applicable' depending on the computed amount.
//	    Handler then runs the same refund pipeline.
//	resolution = "reject" → flip to 'cancelled'. No refund, no car
//	    release (the rental remains live; in practice the lease will
//	    almost always already be at its natural end).
func (r *VehicleReturnRepository) ResolveDispute(ctx context.Context, id uuid.UUID, resolution string) (*models.VehicleReturn, error) {
	var query string
	switch resolution {
	case "accept":
		query = `
			UPDATE vehicle_returns
			SET status = 'owner_confirmed',
			    owner_confirmed_at = COALESCE(owner_confirmed_at, NOW()),
			    dispute_resolved_by = 'admin',
			    refund_status = CASE
			        WHEN refund_amount_cents > 0 THEN 'pending'::varchar
			        ELSE 'not_applicable'::varchar
			    END,
			    updated_at = NOW()
			WHERE id = $1 AND status = 'disputed'
			RETURNING ` + vehicleReturnColumns
	case "reject":
		query = `
			UPDATE vehicle_returns
			SET status = 'cancelled',
			    cancelled_at = NOW(),
			    dispute_resolved_by = 'admin',
			    updated_at = NOW()
			WHERE id = $1 AND status = 'disputed'
			RETURNING ` + vehicleReturnColumns
	default:
		return nil, models.NewValidationError("resolution must be 'accept' or 'reject'")
	}

	row := r.db.Pool.QueryRow(ctx, query, id)
	v, err := scanVehicleReturn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrInvalidReturnState
	}
	if err != nil {
		return nil, fmt.Errorf("resolve vehicle return dispute: %w", err)
	}
	return v, nil
}

// ListByStatus is the admin-list helper. Empty status returns all rows.
func (r *VehicleReturnRepository) ListByStatus(ctx context.Context, status string, limit, offset int) ([]models.VehicleReturn, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows pgx.Rows
		err  error
	)
	if status == "" {
		rows, err = r.db.Pool.Query(ctx,
			`SELECT `+vehicleReturnColumns+`
			 FROM vehicle_returns
			 ORDER BY created_at DESC
			 LIMIT $1 OFFSET $2`, limit, offset)
	} else {
		rows, err = r.db.Pool.Query(ctx,
			`SELECT `+vehicleReturnColumns+`
			 FROM vehicle_returns
			 WHERE status = $1
			 ORDER BY created_at DESC
			 LIMIT $2 OFFSET $3`, status, limit, offset)
	}
	if err != nil {
		return nil, fmt.Errorf("list vehicle returns: %w", err)
	}
	defer rows.Close()

	out := []models.VehicleReturn{}
	for rows.Next() {
		v, err := scanVehicleReturn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}
