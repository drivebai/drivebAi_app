package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// ChargeDisputeRepository persists the Stripe dispute mirror (batch 1 of
// the recurring-billing plan; closes audit M2). stripe_dispute_id UNIQUE
// makes every write path idempotent under webhook redelivery.
type ChargeDisputeRepository struct {
	db *database.DB
}

func NewChargeDisputeRepository(db *database.DB) *ChargeDisputeRepository {
	return &ChargeDisputeRepository{db: db}
}

const chargeDisputeColumns = `
	id, stripe_dispute_id, stripe_charge_id, payment_intent_id, lease_request_id,
	amount_cents, currency, reason, status, outcome, ticket_id,
	payouts_withheld, reversal_done, closed_at, created_at, updated_at`

func scanChargeDispute(row pgx.Row) (*models.ChargeDispute, error) {
	var d models.ChargeDispute
	err := row.Scan(
		&d.ID, &d.StripeDisputeID, &d.StripeChargeID, &d.PaymentIntentID, &d.LeaseRequestID,
		&d.AmountCents, &d.Currency, &d.Reason, &d.Status, &d.Outcome, &d.TicketID,
		&d.PayoutsWithheld, &d.ReversalDone, &d.ClosedAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Upsert records a dispute event. Insert-or-update on stripe_dispute_id:
// the first event creates the row; later events (updated/closed, or
// redelivery) refresh status only. Returns (row, createdNow).
func (r *ChargeDisputeRepository) Upsert(ctx context.Context, d *models.ChargeDispute) (*models.ChargeDispute, bool, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO charge_disputes
			(id, stripe_dispute_id, stripe_charge_id, payment_intent_id, lease_request_id,
			 amount_cents, currency, reason, status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		ON CONFLICT (stripe_dispute_id)
		DO UPDATE SET status = EXCLUDED.status, updated_at = NOW()
		RETURNING `+chargeDisputeColumns+`, (xmax = 0) AS created_now`,
		d.StripeDisputeID, d.StripeChargeID, d.PaymentIntentID, d.LeaseRequestID,
		d.AmountCents, d.Currency, d.Reason, d.Status)

	var out models.ChargeDispute
	var createdNow bool
	err := row.Scan(
		&out.ID, &out.StripeDisputeID, &out.StripeChargeID, &out.PaymentIntentID, &out.LeaseRequestID,
		&out.AmountCents, &out.Currency, &out.Reason, &out.Status, &out.Outcome, &out.TicketID,
		&out.PayoutsWithheld, &out.ReversalDone, &out.ClosedAt, &out.CreatedAt, &out.UpdatedAt,
		&createdNow,
	)
	if err != nil {
		return nil, false, fmt.Errorf("upsert charge dispute: %w", err)
	}
	return &out, createdNow, nil
}

// ClaimPayoutsWithheld flips the claimed-once flag that gates the
// withhold-on-open side effect (one withholding sweep per dispute, no
// matter how many times the created event is delivered).
func (r *ChargeDisputeRepository) ClaimPayoutsWithheld(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE charge_disputes SET payouts_withheld = TRUE, updated_at = NOW()
		WHERE id = $1 AND payouts_withheld = FALSE`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetTicket records the support ticket opened for this dispute (best-effort
// linkage; the ticket itself dedupes via the live-scoped unique index).
func (r *ChargeDisputeRepository) SetTicket(ctx context.Context, id, ticketID uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE charge_disputes SET ticket_id = $2, updated_at = NOW()
		WHERE id = $1 AND ticket_id IS NULL`, id, ticketID)
	return err
}

// Close stamps the outcome, claimed-once (closed_at IS NULL scope): exactly
// one caller runs the won/lost side effects even under redelivery.
func (r *ChargeDisputeRepository) Close(ctx context.Context, id uuid.UUID, outcome string) (*models.ChargeDispute, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE charge_disputes
		SET outcome = $2, status = $3, closed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND closed_at IS NULL
		RETURNING `+chargeDisputeColumns, id, outcome, outcome)
	d, err := scanChargeDispute(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // already closed — redelivery, benign
	}
	if err != nil {
		return nil, fmt.Errorf("close charge dispute: %w", err)
	}
	return d, nil
}

// ClaimReversalDone gates the transfer-reversal side effect claimed-once.
func (r *ChargeDisputeRepository) ClaimReversalDone(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE charge_disputes SET reversal_done = TRUE, updated_at = NOW()
		WHERE id = $1 AND reversal_done = FALSE`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// GetByStripeID loads a dispute row (nil when unknown).
func (r *ChargeDisputeRepository) GetByStripeID(ctx context.Context, stripeDisputeID string) (*models.ChargeDispute, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+chargeDisputeColumns+` FROM charge_disputes WHERE stripe_dispute_id = $1`, stripeDisputeID)
	d, err := scanChargeDispute(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}
