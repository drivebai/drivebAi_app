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
		ON CONFLICT (lease_request_id) WHERE revoked_at IS NULL
		DO UPDATE SET amount_cents = EXCLUDED.amount_cents,
		    terms_version = EXCLUDED.terms_version,
		    disclosure_text = EXCLUDED.disclosure_text
		WHERE lease_billing_consents.activated_at IS NULL
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

// ClaimAttempt is the attempt gate: scheduled/retrying → charging,
// attempt_count+1. It does NOT touch the intent id (review C2: the old
// combined form stamped '' and poisoned the confirm ladder forever).
func (r *BillingRepository) ClaimAttempt(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'charging', attempt_count = attempt_count + 1, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'retrying')
	`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// StampIntent persists the cycle's ONE PaymentIntent id. First writer wins
// (NULLIF guards against empty-string poisoning); no status restriction —
// the stamp must land even after the claim moved the row to charging.
func (r *BillingRepository) StampIntent(ctx context.Context, id uuid.UUID, intentID string) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET stripe_payment_intent_id = COALESCE(NULLIF(stripe_payment_intent_id, ''), NULLIF($2, '')),
		    updated_at = NOW()
		WHERE id = $1
	`, id, intentID)
	return err
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
func (r *BillingRepository) AdvanceOnCyclePaid(ctx context.Context, cycleID uuid.UUID) (*models.BillingCycle, bool, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		UPDATE billing_cycles
		SET status = 'paid', next_attempt_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'charging', 'retrying', 'needs_action', 'failed_final')
		RETURNING `+billingCycleColumns, cycleID)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil // already paid (redelivery) — benign
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim cycle paid: %w", err)
	}

	// The advance requires a LIVE occupancy (review H4): a charge landing
	// after the return completed must not extend a finished rental — the
	// caller refunds it instead.
	// Advance to the PAID CYCLE's own period_end, not a hardcoded +7d —
	// interval-agnostic (amendment batch: a monthly cycle advances 28d),
	// and for weekly cycles exactly equivalent by the mint's anchor
	// arithmetic (period_end = the rental_ends_at the cycle was minted
	// from + 7d). GREATEST defends against replays ever shrinking it.
	// Recovery from delinquency (v98): a late Pay-now on a cycle whose
	// period ended weeks ago must resume billing FORWARD with the promised
	// notice runway — advancing only to that period's end would land the
	// lease below the catch-up floor and stall it. The days in between are
	// not back-billed, exactly as on a halt clear (ClearRenewalHaltReporting).
	// Everything on time is untouched: the CASE fires only when the lease is
	// delinquent AND the paid period is already over.
	tag, err := tx.Exec(ctx, `
		UPDATE lease_requests
		SET rental_ends_at = CASE
		        WHEN (delinquent_since IS NOT NULL OR renewal_halted_reason = 'delinquent') AND $2::timestamptz < NOW()
		          THEN GREATEST(rental_ends_at, NOW() + $3::interval)
		        ELSE GREATEST(rental_ends_at, $2)
		      END,
		    term_ending_notified_at = NULL,
		    overdue_notified_at = NULL,
		    overdue_escalated_at = NULL,
		    delinquent_since = NULL,
		    renewal_halted_reason = CASE WHEN renewal_halted_reason = 'delinquent' THEN NULL ELSE renewal_halted_reason END,
		    updated_at = NOW()
		WHERE id = $1 AND billing_mode = 'rolling'
		  AND status = 'paid' AND vehicle_returned_at IS NULL
		  AND renewal_stopped_at IS NULL
	`, c.LeaseRequestID, c.PeriodEnd, fmt.Sprintf("%d seconds", int(models.BillingNoticeLead.Seconds())))
	if err != nil {
		return nil, false, fmt.Errorf("advance paid-through: %w", err)
	}
	advanced := tag.RowsAffected() == 1

	// Durable discriminator (verify-pass CRITICAL): {paid, payout absent}
	// is otherwise ambiguous between crashed-before-accrual and
	// crashed-before-refund — and resolving it wrong pays an owner for a
	// week that must be refunded. The marker is written IN THIS TX.
	if !advanced {
		if _, err := tx.Exec(ctx, `
			UPDATE billing_cycles SET admin_note = 'refund_pending: charge landed after occupancy ended', updated_at = NOW()
			WHERE id = $1`, cycleID); err != nil {
			return nil, false, fmt.Errorf("stamp refund-pending: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return c, advanced, nil
}

// SettleArrearsProRata flips an unpaid cycle to arrears_due at return
// completion with the amount cut to what the driver actually owes — the
// used days of the final week (design §7: return is never blocked on
// debt; collection is ON-SESSION only, never a silent MIT; admin waive is
// the write-off). Deliberately excludes 'charging': a mid-confirm attempt
// is hands-off per the proven-neutralize rule — if it lands post-return
// the reconciler refunds it, and arrears never applies. 'retrying' and
// 'needs_action' are included because their only arrears callers (the
// returned-lease closer and the return-aware TTL) neutralize any live
// intent before flipping.
func (r *BillingRepository) SettleArrearsProRata(ctx context.Context, id uuid.UUID, owedCents int64) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'arrears_due', amount_cents = $2, next_attempt_at = NULL,
		    needs_action_since = NULL, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'retrying', 'needs_action', 'failed_final')
	`, id, owedCents)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ListStuckCharging returns cycles parked in 'charging' beyond the grace —
// the process died between the claim and the outcome; the engine re-reads
// the intent's true state (review M2: no state without an exit).
func (r *BillingRepository) ListStuckCharging(ctx context.Context, before time.Time, limit int) ([]models.BillingCycle, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE status = 'charging' AND updated_at <= $1
		ORDER BY updated_at ASC LIMIT $2`, before, limit)
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

// RefundCycleClaim records a full cycle refund claimed-once (H4: a charge
// landing on a returned/terminal lease is refunded, never kept).
func (r *BillingRepository) RefundCycleClaim(ctx context.Context, id uuid.UUID, refundID string, amountCents int64) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'refunded', refund_id = $2, refunded_cents = $3, updated_at = NOW()
		WHERE id = $1 AND status = 'paid' AND refund_id IS NULL
	`, id, refundID, amountCents)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// WaiveUnpaidCycle voids an unpaid cycle when renewals stop before its
// period ever started (stop/terminate with the charge not yet through).
func (r *BillingRepository) WaiveUnpaidCycle(ctx context.Context, id uuid.UUID, note string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'waived', admin_note = $2, next_attempt_at = NULL, updated_at = NOW()
		WHERE id = $1 AND status IN ('scheduled', 'charging', 'retrying', 'needs_action', 'failed_final', 'arrears_due')
	`, id, note)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// GetOpenCycleForLease returns the lease's single unresolved cycle, if any.
func (r *BillingRepository) GetOpenCycleForLease(ctx context.Context, leaseID uuid.UUID) (*models.BillingCycle, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE lease_request_id = $1
		  AND status IN ('scheduled', 'charging', 'retrying', 'needs_action', 'failed_final')
		ORDER BY cycle_number DESC LIMIT 1`, leaseID)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// GetCycleByIntent resolves a Stripe intent to its cycle (dispute routing).
func (r *BillingRepository) GetCycleByIntent(ctx context.Context, intentID string) (*models.BillingCycle, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE stripe_payment_intent_id = $1`, intentID)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// PartialRefundCycleClaim records the final-cycle pro-rata refund
// (claimed-once via refund_id IS NULL on a paid cycle).
func (r *BillingRepository) PartialRefundCycleClaim(ctx context.Context, id uuid.UUID, refundID string, refundedCents int64) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'partially_refunded', refund_id = $2, refunded_cents = $3, updated_at = NOW()
		WHERE id = $1 AND status = 'paid' AND refund_id IS NULL
	`, id, refundID, refundedCents)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MintCycleOnePaid bootstraps week 1 as a real cycle row at pickup (batch
// 3): rolling settlement and payout cadence then operate uniformly on
// cycles. Claimed-once by the (lease, 1) unique; the intent comes from the
// week-1 payments row.
func (r *BillingRepository) MintCycleOnePaid(ctx context.Context, leaseID uuid.UUID, periodStart, periodEnd time.Time, amountCents int64, intentID string) (*models.BillingCycle, bool, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO billing_cycles
			(id, lease_request_id, cycle_number, period_start, period_end,
			 amount_cents, status, stripe_payment_intent_id, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 1, $2, $3, $4, 'paid', NULLIF($5, ''), NOW(), NOW())
		ON CONFLICT (lease_request_id, cycle_number) DO NOTHING
		RETURNING `+billingCycleColumns,
		leaseID, periodStart, periodEnd, amountCents, intentID)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := r.GetCycleByNumber(ctx, leaseID, 1)
		return existing, false, gerr
	}
	if err != nil {
		return nil, false, fmt.Errorf("mint cycle one: %w", err)
	}
	return c, true, nil
}

// ListRollingNeedingCycleOne finds paid+picked-up rolling leases whose week
// 1 hasn't been bootstrapped (the sweep phase's lister).
func (r *BillingRepository) ListRollingNeedingCycleOne(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT lr.id FROM lease_requests lr
		WHERE lr.billing_mode = 'rolling' AND lr.status = 'paid'
		  AND lr.pickup_confirmed_at IS NOT NULL
		  AND (
		    NOT EXISTS (SELECT 1 FROM billing_cycles bc
		                WHERE bc.lease_request_id = lr.id AND bc.cycle_number = 1)
		    -- Crash window (batch-3 review): cycle 1 minted but its accrual
		    -- never landed — keep listing until the payout row exists, so
		    -- the phase's idempotent fall-through can finish the job.
		    OR EXISTS (SELECT 1 FROM billing_cycles bc
		               WHERE bc.lease_request_id = lr.id AND bc.cycle_number = 1
		                 AND bc.status = 'paid' AND bc.refund_id IS NULL
		                 AND NOT EXISTS (SELECT 1 FROM owner_payouts op
		                                 WHERE op.billing_cycle_id = bc.id))
		  )
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GetOpenOrLatestPaidCycle returns the highest-numbered cycle in a money-
// bearing or money-settled state — the rolling settlement anchor. Refunded
// states are included so a crashed settlement replays against the SAME
// anchor cycle instead of sliding back to its predecessor; only 'waived'
// (no money ever moved) is excluded.
func (r *BillingRepository) GetOpenOrLatestPaidCycle(ctx context.Context, leaseID uuid.UUID) (*models.BillingCycle, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE lease_request_id = $1
		  AND status IN ('paid', 'scheduled', 'charging', 'retrying', 'needs_action',
		                 'failed_final', 'arrears_due', 'refunded', 'partially_refunded')
		ORDER BY cycle_number DESC LIMIT 1`, leaseID)
	c, err := scanBillingCycle(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// ListPaidCyclesAfterReturn finds paid, un-refunded cycles whose period
// starts strictly AFTER the lease's stamped return — money collected for a
// week the driver never entered because the charge was in flight while the
// return settled. The sweep refunds these in full (batch 3 reconciler).
// Strict '>' so the boundary case (returned exactly at period start)
// stays with the settlement path's current-cycle pro-rata instead.
func (r *BillingRepository) ListPaidCyclesAfterReturn(ctx context.Context, limit int) ([]*models.BillingCycle, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT bc.id, bc.lease_request_id, bc.cycle_number, bc.period_start, bc.period_end,
		       bc.amount_cents, bc.status, bc.stripe_payment_intent_id, bc.attempt_count,
		       bc.next_attempt_at, bc.last_decline_code, bc.needs_action_since, bc.refunded_cents,
		       bc.refund_id, bc.failure_notified_at, bc.delinquent_notified_at, bc.admin_note,
		       bc.created_at, bc.updated_at
		FROM billing_cycles bc
		JOIN lease_requests lr ON lr.id = bc.lease_request_id
		WHERE bc.status = 'paid' AND bc.refund_id IS NULL
		  AND lr.billing_mode = 'rolling'
		  AND lr.vehicle_returned_at IS NOT NULL
		  AND (
		    bc.period_start > lr.vehicle_returned_at
		    -- refund_pending float (batch-3 review HIGH): the webhook's
		    -- occupancy-refused branch stamped the marker but its refund
		    -- kept failing until Stripe redelivery exhausted — without this
		    -- arm the cycle floats paid-with-refund-owed forever.
		    OR bc.admin_note LIKE 'refund_pending%'
		  )
		ORDER BY bc.updated_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.BillingCycle
	for rows.Next() {
		c, serr := scanBillingCycle(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCyclesForLease returns every cycle for a lease, oldest first — the
// admin billing drawer's source.
func (r *BillingRepository) ListCyclesForLease(ctx context.Context, leaseID uuid.UUID) ([]*models.BillingCycle, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+billingCycleColumns+` FROM billing_cycles
		WHERE lease_request_id = $1 ORDER BY cycle_number ASC`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.BillingCycle
	for rows.Next() {
		c, serr := scanBillingCycle(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordLateChargeRefund stamps a full refund onto a cycle whose status
// was already written off (arrears_due / waived) when its charge landed —
// the batch-3 review CRITICAL: the claim scope refuses those statuses on
// purpose, but the money signal must still be processed, not ACKed away.
// The status deliberately stays put: arrears keeps its (pro-rata) debt for
// on-session collection; waived stays waived. Claimed-once via refund_id.
func (r *BillingRepository) RecordLateChargeRefund(ctx context.Context, id uuid.UUID, refundID string, refundedCents int64) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET refund_id = $2, refunded_cents = $3, updated_at = NOW()
		WHERE id = $1 AND refund_id IS NULL AND status IN ('arrears_due', 'waived')
	`, id, refundID, refundedCents)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ListOpenCyclesOnReturnedLeases finds cycles left open after the rental
// factually ended — the states no other closer reaches (batch-3 review):
// 'retrying' is skipped forever by the retry phase once the lease is
// returned, 'scheduled' with a stamped intent is past the settlement's
// provably-safe waive, and a 'failed_final' the settlement missed (crash,
// overshoot) would otherwise park. The closer phase neutralizes any live
// intent and settles pro-rata arrears / waives.
func (r *BillingRepository) ListOpenCyclesOnReturnedLeases(ctx context.Context, limit int) ([]*models.BillingCycle, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT bc.id, bc.lease_request_id, bc.cycle_number, bc.period_start, bc.period_end,
		       bc.amount_cents, bc.status, bc.stripe_payment_intent_id, bc.attempt_count,
		       bc.next_attempt_at, bc.last_decline_code, bc.needs_action_since, bc.refunded_cents,
		       bc.refund_id, bc.failure_notified_at, bc.delinquent_notified_at, bc.admin_note,
		       bc.created_at, bc.updated_at
		FROM billing_cycles bc
		JOIN lease_requests lr ON lr.id = bc.lease_request_id
		WHERE bc.status IN ('scheduled', 'retrying', 'failed_final')
		  AND lr.billing_mode = 'rolling'
		  AND lr.vehicle_returned_at IS NOT NULL
		ORDER BY bc.updated_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.BillingCycle
	for rows.Next() {
		c, serr := scanBillingCycle(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ArrearsPaidClaim settles a post-return debt: arrears_due → paid,
// claimed-once, deliberately OVERWRITING stripe_payment_intent_id with the
// on-session arrears intent — the original (neutralized/failed) intent has
// no further meaning, and dispute resolution must find the cycle by the
// charge that actually holds money (batch 4).
func (r *BillingRepository) ArrearsPaidClaim(ctx context.Context, id uuid.UUID, intentID string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_cycles
		SET status = 'paid', stripe_payment_intent_id = NULLIF($2, ''), updated_at = NOW()
		WHERE id = $1 AND status = 'arrears_due'
	`, id, intentID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// UpdateConsentPaymentMethod swaps the mandate's card after a VERIFIED
// SetupIntent (batch 4 card update). Only a live, activated consent may be
// re-carded — an unactivated consent has no mandate to update, and a
// revoked one must go through a fresh booking.
func (r *BillingRepository) UpdateConsentPaymentMethod(ctx context.Context, leaseID uuid.UUID, pmID, brand, last4, fingerprint string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE lease_billing_consents
		SET stripe_payment_method_id = $2, card_brand = NULLIF($3, ''),
		    card_last4 = NULLIF($4, ''), card_fingerprint = NULLIF($5, '')
		WHERE lease_request_id = $1 AND revoked_at IS NULL AND activated_at IS NOT NULL
		  AND $2 <> ''
	`, leaseID, pmID, brand, last4, fingerprint)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// --- Amendment offers (batch: rolling amendments) ---

const amendmentColumns = `
	id, lease_request_id, proposed_by, kind, new_amount_cents, new_interval,
	status, expires_at, acted_at, created_at`

func scanAmendment(row pgx.Row) (*models.BillingAmendmentOffer, error) {
	var a models.BillingAmendmentOffer
	err := row.Scan(&a.ID, &a.LeaseRequestID, &a.ProposedBy, &a.Kind, &a.NewAmountCents,
		&a.NewInterval, &a.Status, &a.ExpiresAt, &a.ActedAt, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateAmendmentOffer opens the single live offer per lease; a second
// open proposal returns (nil, nil) so the handler can 409 with the
// existing offer.
func (r *BillingRepository) CreateAmendmentOffer(ctx context.Context, leaseID, proposedBy uuid.UUID, kind string, newAmountCents int64, newInterval string, ttl time.Duration) (*models.BillingAmendmentOffer, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO billing_amendment_offers
			(id, lease_request_id, proposed_by, kind, new_amount_cents, new_interval,
			 status, expires_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, 'open', NOW() + $6, NOW(), NOW())
		ON CONFLICT (lease_request_id) WHERE status = 'open' DO NOTHING
		RETURNING `+amendmentColumns,
		leaseID, proposedBy, kind, newAmountCents, newInterval, ttl)
	a, err := scanAmendment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create amendment offer: %w", err)
	}
	return a, nil
}

func (r *BillingRepository) GetAmendment(ctx context.Context, id uuid.UUID) (*models.BillingAmendmentOffer, error) {
	row := r.db.Pool.QueryRow(ctx, `SELECT `+amendmentColumns+` FROM billing_amendment_offers WHERE id = $1`, id)
	a, err := scanAmendment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// GetOpenAmendmentForLease returns the lease's live offer, if any.
func (r *BillingRepository) GetOpenAmendmentForLease(ctx context.Context, leaseID uuid.UUID) (*models.BillingAmendmentOffer, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+amendmentColumns+` FROM billing_amendment_offers
		WHERE lease_request_id = $1 AND status = 'open'`, leaseID)
	a, err := scanAmendment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// CloseAmendment moves an open, unexpired offer to declined/withdrawn
// (claimed-once). Acceptance goes through AcceptAmendment instead — it
// must swap the consent in the same transaction.
func (r *BillingRepository) CloseAmendment(ctx context.Context, id uuid.UUID, to string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE billing_amendment_offers
		SET status = $2, acted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'open' AND expires_at > NOW()
	`, id, to)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ExpireAmendments sweeps lapsed offers, returning them for notification.
func (r *BillingRepository) ExpireAmendments(ctx context.Context, limit int) ([]*models.BillingAmendmentOffer, error) {
	rows, err := r.db.Pool.Query(ctx, `
		UPDATE billing_amendment_offers
		SET status = 'expired', acted_at = NOW(), updated_at = NOW()
		WHERE id IN (SELECT id FROM billing_amendment_offers
		             WHERE status = 'open' AND expires_at <= NOW() LIMIT $1)
		  AND status = 'open'
		RETURNING `+amendmentColumns, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.BillingAmendmentOffer
	for rows.Next() {
		a, serr := scanAmendment(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AcceptAmendment is the ONE transaction the amendment model leans on:
// claim the offer, re-verify no open cycle (an in-dunning week must
// finish at ITS agreed amount — the charge assert would strand it
// otherwise), supersede the active consent, and mint the successor with
// fresh acceptance evidence — same PM, new amount/interval, the new
// disclosure recorded verbatim. No window exists where the lease has no
// active consent (the mint phase would halt consent_revoked in it).
// Returns the new consent; sentinel errors: ErrAmendmentGone (lost the
// claim / expired), ErrAmendmentCycleOpen, ErrAmendmentNoMandate.
func (r *BillingRepository) AcceptAmendment(ctx context.Context, offerID uuid.UUID, termsVersion, disclosureText string) (*models.BillingConsent, error) {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	offer := &models.BillingAmendmentOffer{}
	err = tx.QueryRow(ctx, `
		UPDATE billing_amendment_offers
		SET status = 'accepted', acted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'open' AND expires_at > NOW()
		RETURNING `+amendmentColumns, offerID).Scan(
		&offer.ID, &offer.LeaseRequestID, &offer.ProposedBy, &offer.Kind, &offer.NewAmountCents,
		&offer.NewInterval, &offer.Status, &offer.ExpiresAt, &offer.ActedAt, &offer.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAmendmentGone
	}
	if err != nil {
		return nil, fmt.Errorf("claim amendment: %w", err)
	}

	var openCycles int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM billing_cycles
		WHERE lease_request_id = $1
		  AND status IN ('scheduled', 'charging', 'retrying', 'needs_action', 'failed_final')`,
		offer.LeaseRequestID).Scan(&openCycles); err != nil {
		return nil, fmt.Errorf("amendment open-cycle guard: %w", err)
	}
	if openCycles > 0 {
		return nil, ErrAmendmentCycleOpen
	}

	var pm, brand, last4, fingerprint *string
	var activatedAt *time.Time
	var driverID uuid.UUID
	var oldInterval string
	err = tx.QueryRow(ctx, `
		UPDATE lease_billing_consents
		SET revoked_at = NOW(), revoked_reason = 'superseded: amendment ' || $2
		WHERE lease_request_id = $1 AND revoked_at IS NULL
		RETURNING driver_id, billing_interval, stripe_payment_method_id, card_brand, card_last4, card_fingerprint, activated_at`,
		offer.LeaseRequestID, offerID).Scan(&driverID, &oldInterval, &pm, &brand, &last4, &fingerprint, &activatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAmendmentNoMandate
	}
	if err != nil {
		return nil, fmt.Errorf("supersede consent: %w", err)
	}
	if pm == nil || *pm == "" || activatedAt == nil {
		return nil, ErrAmendmentNoMandate // never amend an unactivated mandate
	}
	// Belt inside the TX (review HIGH): a price offer can never change the
	// interval, whatever its row says — the successor inherits the old one.
	successorInterval := offer.NewInterval
	if offer.Kind == "price" {
		successorInterval = oldInterval
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO lease_billing_consents
			(id, lease_request_id, driver_id, amount_cents, billing_interval,
			 terms_version, disclosure_text, stripe_payment_method_id,
			 card_brand, card_last4, card_fingerprint, activated_at, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		RETURNING `+billingConsentColumns,
		offer.LeaseRequestID, driverID, offer.NewAmountCents, successorInterval,
		termsVersion, disclosureText, pm, brand, last4, fingerprint)
	consent, err := scanBillingConsent(row)
	if err != nil {
		return nil, fmt.Errorf("mint successor consent: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return consent, nil
}
