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

// BillingRepository owns the rolling-billing tables (batch 2): consents and
// cycles, plus the ONE combined transaction the whole design leans on —
// cycle-paid + paid-through advance + flag clears, atomically.
type BillingRepository struct {
	db *database.DB
}

func NewBillingRepository(db *database.DB) *BillingRepository {
	return &BillingRepository{db: db}
}

// --- Consents ---

const billingConsentColumns = `
	id, lease_request_id, driver_id, amount_cents, billing_interval,
	terms_version, disclosure_text, stripe_payment_method_id,
	card_brand, card_last4, card_fingerprint,
	activated_at, revoked_at, revoked_reason, created_at`

func scanBillingConsent(row pgx.Row) (*models.BillingConsent, error) {
	var c models.BillingConsent
	err := row.Scan(
		&c.ID, &c.LeaseRequestID, &c.DriverID, &c.AmountCents, &c.BillingInterval,
		&c.TermsVersion, &c.DisclosureText, &c.StripePaymentMethodID,
		&c.CardBrand, &c.CardLast4, &c.CardFingerprint,
		&c.ActivatedAt, &c.RevokedAt, &c.RevokedReason, &c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateConsent records the driver's agreement BEFORE the first PI exists.
// One active consent per lease (partial unique); a duplicate create returns
// the existing active row (idempotent retry of the checkout call).
func (r *BillingRepository) CreateConsent(ctx context.Context, c *models.BillingConsent) (*models.BillingConsent, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO lease_billing_consents
			(id, lease_request_id, driver_id, amount_cents, billing_interval,
			 terms_version, disclosure_text, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'weekly', $4, $5, NOW())
		ON CONFLICT (lease_request_id) WHERE revoked_at IS NULL DO NOTHING
		RETURNING `+billingConsentColumns,
		c.LeaseRequestID, c.DriverID, c.AmountCents, c.TermsVersion, c.DisclosureText)
	created, err := scanBillingConsent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.GetActiveConsent(ctx, c.LeaseRequestID)
	}
	if err != nil {
		return nil, fmt.Errorf("create billing consent: %w", err)
	}
	return created, nil
}

// ActivateConsent stamps the saved payment method once the first charge
// succeeds (claimed-once via activated_at IS NULL).
func (r *BillingRepository) ActivateConsent(ctx context.Context, leaseID uuid.UUID, pmID, brand, last4, fingerprint string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_billing_consents
		SET stripe_payment_method_id = $2, card_brand = NULLIF($3, ''),
		    card_last4 = NULLIF($4, ''), card_fingerprint = NULLIF($5, ''),
		    activated_at = NOW()
		WHERE lease_request_id = $1 AND revoked_at IS NULL AND activated_at IS NULL
	`, leaseID, pmID, brand, last4, fingerprint)
	if err != nil {
		return false, fmt.Errorf("activate consent: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetActiveConsent returns the lease's live consent row (nil when none).
func (r *BillingRepository) GetActiveConsent(ctx context.Context, leaseID uuid.UUID) (*models.BillingConsent, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+billingConsentColumns+`
		FROM lease_billing_consents
		WHERE lease_request_id = $1 AND revoked_at IS NULL`, leaseID)
	c, err := scanBillingConsent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// RevokeConsent parks the consent (card-brand change, etc.). Claimed-once.
func (r *BillingRepository) RevokeConsent(ctx context.Context, leaseID uuid.UUID, reason string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_billing_consents
		SET revoked_at = NOW(), revoked_reason = $2
		WHERE lease_request_id = $1 AND revoked_at IS NULL
	`, leaseID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// --- Cycles ---

const billingCycleColumns = `
	id, lease_request_id, cycle_number, period_start, period_end, amount_cents,
	status, stripe_payment_intent_id, attempt_count, next_attempt_at,
	last_decline_code, needs_action_since, refunded_cents, refund_id,
	failure_notified_at, delinquent_notified_at, admin_note, created_at, updated_at`

func scanBillingCycle(row pgx.Row) (*models.BillingCycle, error) {
	var c models.BillingCycle
	err := row.Scan(
		&c.ID, &c.LeaseRequestID, &c.CycleNumber, &c.PeriodStart, &c.PeriodEnd, &c.AmountCents,
		&c.Status, &c.StripePaymentIntentID, &c.AttemptCount, &c.NextAttemptAt,
		&c.LastDeclineCode, &c.NeedsActionSince, &c.RefundedCents, &c.RefundID,
		&c.FailureNotifiedAt, &c.DelinquentNotifiedAt, &c.AdminNote, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// MintCycle creates the next cycle claimed-once per (lease, cycle_number):
// a concurrent mint returns the existing row, never a sibling.
func (r *BillingRepository) MintCycle(ctx context.Context, leaseID uuid.UUID, cycleNumber int, periodStart, periodEnd time.Time, amountCents int64, firstAttemptAt time.Time) (*models.BillingCycle, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO billing_cycles
			(id, lease_request_id, cycle_number, period_start, period_end,
			 amount_cents, status, next_attempt_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, 'scheduled', $6, NOW(), NOW())
		ON CONFLICT (lease_request_id, cycle_number) DO NOTHING
		RETURNING `+billingCycleColumns,
		leaseID, cycleNumber, periodStart, periodEnd, amountCents, firstAttemptAt)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return r.GetCycleByNumber(ctx, leaseID, cycleNumber)
	}
	if err != nil {
		return nil, fmt.Errorf("mint billing cycle: %w", err)
	}
	return c, nil
}

// NextCycleNumber derives the next number (cycle 1 is the booking charge in
// the payments table; billing_cycles start at 2).
func (r *BillingRepository) NextCycleNumber(ctx context.Context, leaseID uuid.UUID) (int, error) {
	var n int
	err := r.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(cycle_number), 1) + 1 FROM billing_cycles WHERE lease_request_id = $1`, leaseID).Scan(&n)
	return n, err
}

func (r *BillingRepository) GetCycle(ctx context.Context, id uuid.UUID) (*models.BillingCycle, error) {
	row := r.db.Pool.QueryRow(ctx, `SELECT `+billingCycleColumns+` FROM billing_cycles WHERE id = $1`, id)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (r *BillingRepository) GetCycleByNumber(ctx context.Context, leaseID uuid.UUID, n int) (*models.BillingCycle, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE lease_request_id = $1 AND cycle_number = $2`, leaseID, n)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// AttachIntent stamps the cycle's ONE PaymentIntent and moves it to
// charging (claimed from scheduled/retrying — the attempt gate).
func (r *BillingRepository) AttachIntent(ctx context.Context, id uuid.UUID, intentID string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET stripe_payment_intent_id = COALESCE(stripe_payment_intent_id, $2),
		    status = 'charging', attempt_count = attempt_count + 1, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'retrying')
	`, id, intentID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RecordFailure moves a charging/needs_action cycle down the ladder.
func (r *BillingRepository) RecordFailure(ctx context.Context, id uuid.UUID, declineCode string, nextAttemptAt *time.Time, terminal bool) (*models.BillingCycle, error) {
	status := "retrying"
	if terminal {
		status = "failed_final"
	}
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE billing_cycles
		SET status = $2, last_decline_code = NULLIF($3, ''),
		    next_attempt_at = $4, updated_at = NOW()
		WHERE id = $1 AND status IN ('charging', 'needs_action')
		RETURNING `+billingCycleColumns, id, status, declineCode, nextAttemptAt)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // moved on (paid via webhook race) — correct
	}
	return c, err
}

// MarkNeedsAction parks a cycle awaiting the driver's 3DS rescue.
func (r *BillingRepository) MarkNeedsAction(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'needs_action', needs_action_since = COALESCE(needs_action_since, NOW()), updated_at = NOW()
		WHERE id = $1 AND status = 'charging'`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ListDueCycles returns cycles whose next attempt is due.
func (r *BillingRepository) ListDueCycles(ctx context.Context, now time.Time, limit int) ([]models.BillingCycle, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE status IN ('scheduled', 'retrying') AND next_attempt_at <= $1
		ORDER BY next_attempt_at ASC LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BillingCycle
	for rows.Next() {
		c, serr := scanBillingCycle(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListNeedsActionExpired returns 3DS rescues past the TTL.
func (r *BillingRepository) ListNeedsActionExpired(ctx context.Context, before time.Time, limit int) ([]models.BillingCycle, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE status = 'needs_action' AND needs_action_since <= $1
		ORDER BY needs_action_since ASC LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BillingCycle
	for rows.Next() {
		c, serr := scanBillingCycle(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ClaimFailureNotice / ClaimDelinquentNotice: claimed-once notification
// stamps on the CYCLE row — per-episode by construction.
func (r *BillingRepository) ClaimFailureNotice(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET failure_notified_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND failure_notified_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (r *BillingRepository) ClaimDelinquentNotice(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET delinquent_notified_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND delinquent_notified_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// AdvanceOnCyclePaid is THE transaction (design §4a): cycle → paid AND
// paid-through += 7d AND term/delinquency flags cleared — one COMMIT.
// Claimed-once by the cycle-status scope; the paid-through advance is
// anchor arithmetic (from the stored value, never NOW()). Callers treat
// (nil, nil) as "another delivery won" and do nothing.
func (r *BillingRepository) AdvanceOnCyclePaid(ctx context.Context, cycleID uuid.UUID) (*models.BillingCycle, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE billing_cycles
		SET status = 'paid', next_attempt_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'charging', 'retrying', 'needs_action', 'failed_final')
		RETURNING `+billingCycleColumns, cycleID)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // already paid (redelivery) — benign
	}
	if err != nil {
		return nil, fmt.Errorf("claim cycle paid: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE lease_requests
		SET rental_ends_at = rental_ends_at + INTERVAL '7 days',
		    term_ending_notified_at = NULL,
		    overdue_notified_at = NULL,
		    overdue_escalated_at = NULL,
		    delinquent_since = NULL,
		    renewal_halted_reason = CASE WHEN renewal_halted_reason = 'delinquent' THEN NULL ELSE renewal_halted_reason END,
		    updated_at = NOW()
		WHERE id = $1 AND billing_mode = 'rolling'
	`, c.LeaseRequestID); err != nil {
		return nil, fmt.Errorf("advance paid-through: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// SettleArrears flips an unpaid cycle to arrears_due at return completion
// (on-session collection only — never a silent MIT).
func (r *BillingRepository) SettleArrears(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles SET status = 'arrears_due', next_attempt_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'charging', 'retrying', 'needs_action', 'failed_final')
	`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
