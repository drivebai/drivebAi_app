package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// PayoutRepository owns the owner_payouts ledger and the users-side
// connected-account mirror (migration 000049).
type PayoutRepository struct {
	db *database.DB
}

func NewPayoutRepository(db *database.DB) *PayoutRepository {
	return &PayoutRepository{db: db}
}

const ownerPayoutColumns = `
	id, lease_request_id, purchase_request_id, owner_id, stripe_account_id,
	gross_kept_cents, fee_bps, fee_cents, owner_amount_cents, currency,
	status, source, source_charge_id, stripe_transfer_id, failure_reason, note,
	reminder_count, last_reminder_at, escalated_at, paid_at,
	billing_cycle_id, period_start, period_end, consumed_at, created_at, updated_at`

func scanOwnerPayout(row scanRow) (*models.OwnerPayout, error) {
	var p models.OwnerPayout
	err := row.Scan(
		&p.ID, &p.LeaseRequestID, &p.PurchaseRequestID, &p.OwnerID, &p.StripeAccountID,
		&p.GrossKeptCents, &p.FeeBPS, &p.FeeCents, &p.OwnerAmountCents, &p.Currency,
		&p.Status, &p.Source, &p.SourceChargeID, &p.StripeTransferID, &p.FailureReason, &p.Note,
		&p.ReminderCount, &p.LastReminderAt, &p.EscalatedAt, &p.PaidAt,
		&p.BillingCycleID, &p.PeriodStart, &p.PeriodEnd, &p.ConsumedAt, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts the ledger row for a settled rental. Idempotent on the
// lease: a second settlement of the same lease returns the EXISTING row
// unchanged (ON CONFLICT DO NOTHING + re-read) — a rental's money settles
// exactly once, whichever path gets there first.
func (r *PayoutRepository) Create(ctx context.Context, p *models.OwnerPayout) (*models.OwnerPayout, bool, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO owner_payouts
			(id, lease_request_id, owner_id, stripe_account_id,
			 gross_kept_cents, fee_bps, fee_cents, owner_amount_cents, currency,
			 status, source, source_charge_id, note, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW())
		ON CONFLICT (lease_request_id) WHERE billing_cycle_id IS NULL DO NOTHING
		RETURNING `+ownerPayoutColumns,
		uuid.New(), p.LeaseRequestID, p.OwnerID, p.StripeAccountID,
		p.GrossKeptCents, p.FeeBPS, p.FeeCents, p.OwnerAmountCents, p.Currency,
		p.Status, p.Source, p.SourceChargeID, p.Note)
	created, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if p.LeaseRequestID == nil {
			return nil, false, fmt.Errorf("create owner payout: rental path requires a lease id")
		}
		existing, gerr := r.GetByLeaseRequestID(ctx, *p.LeaseRequestID)
		return existing, false, gerr
	}
	if err != nil {
		return nil, false, fmt.Errorf("create owner payout: %w", err)
	}
	return created, true, nil
}

func (r *PayoutRepository) GetByLeaseRequestID(ctx context.Context, leaseID uuid.UUID) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `SELECT `+ownerPayoutColumns+` FROM owner_payouts WHERE lease_request_id = $1`, leaseID)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// MarkPaid stamps the successful transfer. Guarded so a concurrent sweep
// can't double-stamp.
func (r *PayoutRepository) MarkPaid(ctx context.Context, id uuid.UUID, transferID, accountID, sourceChargeID string) (*models.OwnerPayout, error) {
	// sourceChargeID makes the row dispute-addressable: a lost chargeback
	// reverses exactly the transfer funded by that charge (review CRITICAL —
	// the column was previously never written in production, making the
	// clawback dead code). COALESCE keeps an earlier stamp authoritative.
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE owner_payouts
		SET status = 'paid', stripe_transfer_id = $2, stripe_account_id = $3,
		    source_charge_id = COALESCE(source_charge_id, NULLIF($4, '')),
		    failure_reason = NULL, paid_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'failed', 'awaiting_onboarding')
		RETURNING `+ownerPayoutColumns, id, transferID, accountID, sourceChargeID)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.NewAPIError("PAYOUT_STATE", "payout already settled")
	}
	return p, err
}

// MarkFailed records a rejected transfer for the retry sweep. Never
// downgrades a paid row.
func (r *PayoutRepository) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'failed', failure_reason = $2, updated_at = NOW()
		WHERE id = $1 AND status <> 'paid'`, id, reason)
	return err
}

// MarkAwaitingOnboarding parks the payout until the owner is ready.
func (r *PayoutRepository) MarkAwaitingOnboarding(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'awaiting_onboarding', updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'failed')`, id)
	return err
}

// Withhold is the deliberate-non-payment leg — admin only, note required
// at the handler. Refuses to withhold money that already moved.
func (r *PayoutRepository) Withhold(ctx context.Context, id uuid.UUID, note string) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE owner_payouts
		SET status = 'withheld', note = $2, updated_at = NOW()
		WHERE id = $1 AND status NOT IN ('paid', 'reversed')
		RETURNING `+ownerPayoutColumns, id, note)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.NewAPIError("PAYOUT_STATE", "payout already paid — cannot withhold")
	}
	return p, err
}

// ReviveWithheld reverses a withhold decision — withheld is deliberate,
// not dead: the exit is another deliberate admin call (payout_only), which
// re-opens the row as pending so the engine pays it.
func (r *PayoutRepository) ReviveWithheld(ctx context.Context, id uuid.UUID, note string) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE owner_payouts
		SET status = 'pending', note = $2, updated_at = NOW()
		WHERE id = $1 AND status = 'withheld'
		RETURNING `+ownerPayoutColumns, id, note)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.NewAPIError("PAYOUT_STATE", "payout is not withheld")
	}
	return p, err
}

// ListExecutable returns rows the sweep should attempt now: pending, plus
// failed rows older than the retry cutoff, plus awaiting_onboarding rows
// whose owner has since become ready (join on the live user status).
func (r *PayoutRepository) ListExecutable(ctx context.Context, failedRetryBefore time.Time, limit int) ([]models.OwnerPayout, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+prefixCols(ownerPayoutColumns, "op")+`
		FROM owner_payouts op
		JOIN users u ON u.id = op.owner_id
		WHERE op.status = 'pending'
		   OR (op.status = 'failed' AND op.updated_at <= $1)
		   OR (op.status = 'awaiting_onboarding' AND u.payout_status = 'ready')
		ORDER BY op.created_at ASC
		LIMIT $2`, failedRetryBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list executable payouts: %w", err)
	}
	defer rows.Close()
	var out []models.OwnerPayout
	for rows.Next() {
		p, err := scanOwnerPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ClaimEscrowReminders claims awaiting_onboarding rows due a reminder —
// claimed-once per cadence window via last_reminder_at.
func (r *PayoutRepository) ClaimEscrowReminders(ctx context.Context, olderThan, remindedBefore time.Time, limit int) ([]models.OwnerPayout, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		UPDATE owner_payouts op
		SET reminder_count = op.reminder_count + 1, last_reminder_at = NOW(), updated_at = NOW()
		FROM (
			SELECT id FROM owner_payouts
			WHERE status = 'awaiting_onboarding'
			  AND created_at <= $1
			  AND (last_reminder_at IS NULL OR last_reminder_at <= $2)
			ORDER BY created_at ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		) picked
		WHERE op.id = picked.id
		RETURNING `+prefixCols(ownerPayoutColumns, "op"), olderThan, remindedBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("claim escrow reminders: %w", err)
	}
	defer rows.Close()
	var out []models.OwnerPayout
	for rows.Next() {
		p, err := scanOwnerPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListEscrowEscalationCandidates SELECTs (claimless — ticket-first,
// flag-after, same crash-safe shape as the term scanner) stale
// awaiting_onboarding rows that have no escalation ticket yet.
func (r *PayoutRepository) ListEscrowEscalationCandidates(ctx context.Context, olderThan time.Time, limit int) ([]models.OwnerPayout, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+ownerPayoutColumns+`
		FROM owner_payouts
		WHERE status = 'awaiting_onboarding'
		  AND created_at <= $1
		  AND escalated_at IS NULL
		ORDER BY created_at ASC
		LIMIT $2`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("list escrow escalation candidates: %w", err)
	}
	defer rows.Close()
	var out []models.OwnerPayout
	for rows.Next() {
		p, err := scanOwnerPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// MarkEscrowEscalated stamps the escalation flag after the ticket exists.
func (r *PayoutRepository) MarkEscrowEscalated(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts SET escalated_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND escalated_at IS NULL`, id)
	return err
}

// ListForOwner is the owner-facing earnings list.
func (r *PayoutRepository) ListForOwner(ctx context.Context, ownerID uuid.UUID) ([]models.OwnerPayout, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+ownerPayoutColumns+` FROM owner_payouts
		WHERE owner_id = $1 ORDER BY created_at DESC LIMIT 100`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list payouts for owner: %w", err)
	}
	defer rows.Close()
	var out []models.OwnerPayout
	for rows.Next() {
		p, err := scanOwnerPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// AdminPayoutRow is the console shape: the ledger row + who and what it is
// about + its age.
type AdminPayoutRow struct {
	models.OwnerPayout
	OwnerName  string  `json:"owner_name"`
	OwnerEmail string  `json:"owner_email"`
	CarTitle   string  `json:"car_title"`
	AgeDays    float64 `json:"age_days"`
}

// AdminList answers "where is every owner's money" — optionally filtered by
// status, oldest first so aged balances surface.
func (r *PayoutRepository) AdminList(ctx context.Context, status string, limit int) ([]AdminPayoutRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	args := []interface{}{limit}
	where := ""
	if status != "" {
		args = append(args, status)
		where = "WHERE op.status = $2"
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+prefixCols(ownerPayoutColumns, "op")+`,
		       COALESCE(u.first_name || ' ' || u.last_name, ''), u.email,
		       (SELECT title FROM cars WHERE id = lr.listing_id),
		       EXTRACT(EPOCH FROM (NOW() - op.created_at)) / 86400.0
		FROM owner_payouts op
		JOIN users u ON u.id = op.owner_id
		JOIN lease_requests lr ON lr.id = op.lease_request_id
		`+where+`
		ORDER BY op.created_at ASC
		LIMIT $1`, args...)
	if err != nil {
		return nil, fmt.Errorf("admin list payouts: %w", err)
	}
	defer rows.Close()
	var out []AdminPayoutRow
	for rows.Next() {
		var a AdminPayoutRow
		if err := rows.Scan(
			&a.ID, &a.LeaseRequestID, &a.OwnerID, &a.StripeAccountID,
			&a.GrossKeptCents, &a.FeeBPS, &a.FeeCents, &a.OwnerAmountCents, &a.Currency,
			&a.Status, &a.Source, &a.SourceChargeID, &a.StripeTransferID, &a.FailureReason, &a.Note,
			&a.ReminderCount, &a.LastReminderAt, &a.EscalatedAt, &a.PaidAt, &a.CreatedAt, &a.UpdatedAt,
			&a.OwnerName, &a.OwnerEmail, &a.CarTitle, &a.AgeDays); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ─── users-side connected-account mirror ────────────────────────────────────

// SetStripeAccount stores the freshly created connected account id.
func (r *PayoutRepository) SetStripeAccount(ctx context.Context, userID uuid.UUID, accountID string) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE users SET stripe_account_id = $2, payout_status = 'onboarding',
		       payout_status_updated_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND stripe_account_id IS NULL`, userID, accountID)
	return err
}

// ClearStripeAccount forgets a connected account that no longer exists at
// Stripe (deleted/rejected-and-removed) so the owner can start over.
// Guarded on the exact stale id — never clears a fresher account written
// by a concurrent request.
func (r *PayoutRepository) ClearStripeAccount(ctx context.Context, userID uuid.UUID, staleAccountID string) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE users SET stripe_account_id = NULL, payout_status = 'none',
		       payout_requirements = NULL, payout_status_updated_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND stripe_account_id = $2`, userID, staleAccountID)
	return err
}

// UpdatePayoutStatus mirrors the account state locally. requirementsJSON may
// be nil.
func (r *PayoutRepository) UpdatePayoutStatus(ctx context.Context, accountID string, status models.UserPayoutStatus, requirementsJSON []byte) (uuid.UUID, models.UserPayoutStatus, error) {
	var userID uuid.UUID
	var previous models.UserPayoutStatus
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE users u SET payout_status = $2, payout_requirements = $3,
		       payout_status_updated_at = NOW(), updated_at = NOW()
		FROM (SELECT id, payout_status AS prev FROM users WHERE stripe_account_id = $1) old
		WHERE u.id = old.id
		RETURNING u.id, old.prev`, accountID, status, requirementsJSON).Scan(&userID, &previous)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", models.ErrUserNotFound
		}
		return uuid.Nil, "", fmt.Errorf("update payout status: %w", err)
	}
	return userID, previous, nil
}

// GetPayoutAccount returns the user's account id + local status.
func (r *PayoutRepository) GetPayoutAccount(ctx context.Context, userID uuid.UUID) (accountID *string, status models.UserPayoutStatus, err error) {
	err = r.db.Pool.QueryRow(ctx, `
		SELECT stripe_account_id, payout_status FROM users WHERE id = $1 AND deleted_at IS NULL`,
		userID).Scan(&accountID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", models.ErrUserNotFound
	}
	return accountID, status, err
}

// prefixCols qualifies a comma-separated column list with a table alias —
// same trick as prefixedVehicleReturnColumns.
func prefixCols(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// --- Batch 1 (dispute policy, design §5) ---

// WithholdAllUnpaidForLease parks every not-yet-paid payout row for the
// lease when a dispute opens. Status-scoped: paid rows are untouched (their
// clawback happens only on dispute LOST, via MarkReversed). The note tags
// the dispute so a WON outcome can release exactly these rows.
func (r *PayoutRepository) WithholdAllUnpaidForLease(ctx context.Context, leaseID uuid.UUID, note string) (int, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'withheld', note = $2, updated_at = NOW()
		WHERE lease_request_id = $1
		  AND status IN ('pending', 'awaiting_onboarding', 'failed', 'accruing')
	`, leaseID, note)
	if err != nil {
		return 0, fmt.Errorf("withhold unpaid for lease: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ReleaseDisputeWithheld undoes dispute withholding after the LAST open
// dispute on the lease closes won: every row still tagged "pending outcome"
// returns to 'pending' (rows a money-gone closure re-tagged as unreleasable
// stay parked; external-refund tags never match). The caller guarantees no
// sibling dispute remains open. The payout sweep re-parks non-ready owners
// to awaiting_onboarding on its own, so 'pending' is the safe reentry.
func (r *PayoutRepository) ReleaseDisputeWithheld(ctx context.Context, leaseID uuid.UUID) (int, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'pending', updated_at = NOW()
		WHERE lease_request_id = $1
		  AND status = 'withheld'
		  AND note LIKE 'dispute %: withheld pending outcome'
	`, leaseID)
	if err != nil {
		return 0, fmt.Errorf("release dispute-withheld: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// GetPaidByChargeID finds the PAID payout row funded by a specific charge —
// the dispute-lost reversal target. Legacy rows store the charge on
// source_charge_id at settlement time.
func (r *PayoutRepository) GetPaidByChargeID(ctx context.Context, chargeID string) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+ownerPayoutColumns+`
		FROM owner_payouts
		WHERE source_charge_id = $1 AND status = 'paid'
	`, chargeID)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// MarkReversed records a completed clawback, claimed-once via the status
// scope (paid → reversed).
func (r *PayoutRepository) MarkReversed(ctx context.Context, id uuid.UUID, reversalID string, amountCents int64, reason string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'reversed', stripe_reversal_id = $2,
		    reversed_amount_cents = $3, reversed_at = NOW(),
		    reversal_reason = $4, updated_at = NOW()
		WHERE id = $1 AND status = 'paid'
	`, id, reversalID, amountCents, reason)
	if err != nil {
		return false, fmt.Errorf("mark payout reversed: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetPaidByLeaseID is the reversal-target FALLBACK for legacy rows created
// before MarkPaid stamped source_charge_id (incl. the one live paid row):
// fixed-term leases settle exactly once, so lease-scoped is exact for them.
func (r *PayoutRepository) GetPaidByLeaseID(ctx context.Context, leaseID uuid.UUID) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+ownerPayoutColumns+`
		FROM owner_payouts
		WHERE lease_request_id = $1 AND status = 'paid'
	`, leaseID)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// RetagDisputeWithheldUnreleasable permanently parks dispute-withheld rows
// whose money went back to the cardholder (lost/refunded closure): the note
// stops matching the release pattern, so no later WON sibling can free them.
func (r *PayoutRepository) RetagDisputeWithheldUnreleasable(ctx context.Context, leaseID uuid.UUID, disputeTag string) (int, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET note = $2 || ': money returned to cardholder — unreleasable', updated_at = NOW()
		WHERE lease_request_id = $1
		  AND status = 'withheld'
		  AND note LIKE 'dispute %: withheld pending outcome'
	`, leaseID, disputeTag)
	if err != nil {
		return 0, fmt.Errorf("retag dispute-withheld: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// --- Rolling billing (batch 2): per-cycle arrears ledger ---

// CreateCycleAccruing inserts the cycle's payout row at charge time in the
// 'accruing' state (arrears model). Idempotent per billing_cycle_id via the
// partial unique index — a webhook redelivery returns the existing row.
func (r *PayoutRepository) CreateCycleAccruing(ctx context.Context, p *models.OwnerPayout) (*models.OwnerPayout, bool, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO owner_payouts
			(id, lease_request_id, owner_id, gross_kept_cents, fee_bps, fee_cents,
			 owner_amount_cents, currency, status, source, source_charge_id,
			 billing_cycle_id, period_start, period_end, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, 'accruing', 'cycle_consumed', $8,
		        $9, $10, $11, NOW(), NOW())
		ON CONFLICT (billing_cycle_id) WHERE billing_cycle_id IS NOT NULL DO NOTHING
		RETURNING `+ownerPayoutColumns,
		p.LeaseRequestID, p.OwnerID, p.GrossKeptCents, p.FeeBPS, p.FeeCents,
		p.OwnerAmountCents, p.Currency, p.SourceChargeID,
		p.BillingCycleID, p.PeriodStart, p.PeriodEnd)
	created, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := r.GetByBillingCycleID(ctx, *p.BillingCycleID)
		return existing, false, gerr
	}
	if err != nil {
		return nil, false, fmt.Errorf("create cycle accruing: %w", err)
	}
	return created, true, nil
}

func (r *PayoutRepository) GetByBillingCycleID(ctx context.Context, cycleID uuid.UUID) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+ownerPayoutColumns+` FROM owner_payouts WHERE billing_cycle_id = $1`, cycleID)
	p, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// PromoteConsumedCycles is the arrears promotion (design §5): accruing rows
// whose week is over become pending — GUARDED: pickup confirmed, no live
// return, no refund on the cycle, no open dispute on the cycle's intent.
// Returns how many promoted; the existing payout sweep transfers them.
func (r *PayoutRepository) PromoteConsumedCycles(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts op
		SET status = 'pending', consumed_at = NOW(), updated_at = NOW()
		FROM (
			SELECT op2.id AS pid
			FROM owner_payouts op2
			JOIN billing_cycles bc ON bc.id = op2.billing_cycle_id
			JOIN lease_requests lr ON lr.id = op2.lease_request_id
			WHERE op2.status = 'accruing'
			  AND op2.period_end <= $1
			  AND lr.pickup_confirmed_at IS NOT NULL
			  AND bc.refunded_cents = 0
			  AND bc.status = 'paid'
			  AND (bc.admin_note IS NULL OR bc.admin_note NOT LIKE 'refund_pending%')
			  AND (lr.vehicle_returned_at IS NULL OR lr.vehicle_returned_at >= op2.period_end)
			  AND NOT EXISTS (
			      SELECT 1 FROM vehicle_returns vr
			      WHERE vr.lease_request_id = op2.lease_request_id
			        AND vr.status IN ('driver_initiated', 'owner_confirmed', 'disputed'))
			  AND NOT EXISTS (
			      SELECT 1 FROM charge_disputes cd
			      WHERE cd.payment_intent_id = bc.stripe_payment_intent_id
			        AND cd.outcome_settled = FALSE)
			ORDER BY op2.period_end ASC
			LIMIT $2
			FOR UPDATE OF op2 SKIP LOCKED
		) picked
		WHERE op.id = picked.pid
	`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("promote consumed cycles: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RewriteAccruingForFinalCycle adjusts a cycle's provisional split when the
// rental ends mid-cycle (rolling return settlement): kept shrinks to the
// consumed share and the row becomes immediately payable (the lease is
// over — nothing left to guard). Status-scoped to accruing: money that
// already moved is never rewritten.
func (r *PayoutRepository) RewriteAccruingForFinalCycle(ctx context.Context, cycleID uuid.UUID, keptCents, feeCents, ownerCents int64) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET gross_kept_cents = $2, fee_cents = $3, owner_amount_cents = $4,
		    status = 'pending', consumed_at = NOW(), source = 'return_completed',
		    updated_at = NOW()
		WHERE billing_cycle_id = $1 AND status = 'accruing'
	`, cycleID, keptCents, feeCents, ownerCents)
	if err != nil {
		return false, fmt.Errorf("rewrite final cycle payout: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// VoidAccruingCycle terminally voids an unconsumed cycle's payout row
// (overshoot fully refunded / pickup no-show).
func (r *PayoutRepository) VoidAccruingCycle(ctx context.Context, cycleID uuid.UUID, note string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'voided', gross_kept_cents = 0, fee_cents = 0,
		    owner_amount_cents = 0, note = $2, updated_at = NOW()
		WHERE billing_cycle_id = $1 AND status IN ('accruing', 'withheld')
	`, cycleID, note)
	if err != nil {
		return false, fmt.Errorf("void cycle payout: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ListCycleLedgerForLease returns the lease's per-cycle payout rows
// (billing_cycle_id NOT NULL), oldest first — the admin drawer's money
// column alongside ListCyclesForLease.
func (r *PayoutRepository) ListCycleLedgerForLease(ctx context.Context, leaseID uuid.UUID) ([]*models.OwnerPayout, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+ownerPayoutColumns+` FROM owner_payouts
		WHERE lease_request_id = $1 AND billing_cycle_id IS NOT NULL
		ORDER BY period_start ASC NULLS LAST, created_at ASC`, leaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.OwnerPayout
	for rows.Next() {
		p, serr := scanOwnerPayout(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FinalizeCyclePayoutRow settles a cycle's payout slot race-proof against
// the webhook's accrual insert (batch-3 review): the settlement can run
// while handleCyclePaid sits between its advance and its
// CreateCycleAccruing — a plain accruing→X UPDATE no-ops on the missing
// row, and the late insert then zombies as a full-week 'accruing' that
// nothing can ever move. This upsert claims the (billing_cycle_id) slot
// FIRST, so the webhook's later ON CONFLICT DO NOTHING becomes the no-op.
// An existing 'accruing' row rewrites; 'withheld' (dispute machinery owns
// it) and already-final rows stay untouched.
func (r *PayoutRepository) FinalizeCyclePayoutRow(ctx context.Context, p *models.OwnerPayout, status, note string) error {
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO owner_payouts
			(id, lease_request_id, owner_id, gross_kept_cents, fee_bps, fee_cents,
			 owner_amount_cents, currency, status, source, source_charge_id,
			 billing_cycle_id, period_start, period_end, consumed_at, note, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, 'return_completed', $9,
		        $10, $11, $12, NOW(), NULLIF($13, ''), NOW(), NOW())
		ON CONFLICT (billing_cycle_id) WHERE billing_cycle_id IS NOT NULL
		DO UPDATE SET gross_kept_cents = EXCLUDED.gross_kept_cents,
		    fee_cents = EXCLUDED.fee_cents,
		    owner_amount_cents = EXCLUDED.owner_amount_cents,
		    status = EXCLUDED.status, source = 'return_completed',
		    consumed_at = NOW(), note = COALESCE(owner_payouts.note, EXCLUDED.note),
		    updated_at = NOW()
		WHERE owner_payouts.status = 'accruing'
	`, p.LeaseRequestID, p.OwnerID, p.GrossKeptCents, p.FeeBPS, p.FeeCents,
		p.OwnerAmountCents, p.Currency, status, p.SourceChargeID,
		p.BillingCycleID, p.PeriodStart, p.PeriodEnd, note)
	if err != nil {
		return fmt.Errorf("finalize cycle payout row: %w", err)
	}
	return nil
}

// CreateForSale records the seller's split of a completed car sale.
// Claimed-once by the unique index on purchase_request_id, so a capture that
// retries — or two instances racing — produce exactly one payout row.
//
// Mirrors Create() deliberately: same ledger, same statuses, same escrow
// behaviour. A separate seller_payouts table would mean a second
// implementation of split, transfer, escrow and reversal, each free to drift
// from the rental one that has already moved real money correctly.
func (r *PayoutRepository) CreateForSale(ctx context.Context, p *models.OwnerPayout) (*models.OwnerPayout, bool, error) {
	if p.PurchaseRequestID == nil {
		return nil, false, fmt.Errorf("create sale payout: purchase id required")
	}
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO owner_payouts
			(id, purchase_request_id, owner_id, stripe_account_id,
			 gross_kept_cents, fee_bps, fee_cents, owner_amount_cents, currency,
			 status, source, source_charge_id, note, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), NOW())
		ON CONFLICT (purchase_request_id) WHERE purchase_request_id IS NOT NULL DO NOTHING
		RETURNING `+ownerPayoutColumns,
		uuid.New(), p.PurchaseRequestID, p.OwnerID, p.StripeAccountID,
		p.GrossKeptCents, p.FeeBPS, p.FeeCents, p.OwnerAmountCents, p.Currency,
		p.Status, p.Source, p.SourceChargeID, p.Note)
	created, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := r.GetByPurchaseRequestID(ctx, *p.PurchaseRequestID)
		return existing, false, gerr
	}
	if err != nil {
		return nil, false, fmt.Errorf("create sale payout: %w", err)
	}
	return created, true, nil
}

// GetByPurchaseRequestID returns the payout row for one sale, or nil.
func (r *PayoutRepository) GetByPurchaseRequestID(ctx context.Context, purchaseID uuid.UUID) (*models.OwnerPayout, error) {
	p, err := scanOwnerPayout(r.db.Pool.QueryRow(ctx,
		`SELECT `+ownerPayoutColumns+` FROM owner_payouts WHERE purchase_request_id = $1`, purchaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sale payout: %w", err)
	}
	return p, nil
}

// WithholdUnpaidByChargeID holds any unpaid payout funded by one charge,
// whatever its source. A dispute names a CHARGE, not a rental — so this is
// how a disputed car sale stops paying its seller while the outcome is
// unknown. Status-scoped, so a paid row is untouched (that money is clawed
// back by transfer reversal instead).
func (r *PayoutRepository) WithholdUnpaidByChargeID(ctx context.Context, chargeID, note string) (int, error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE owner_payouts
		SET status = 'withheld',
		    note = COALESCE(note || ' | ', '') || $2,
		    updated_at = NOW()
		WHERE source_charge_id = $1
		  AND status IN ('pending', 'awaiting_onboarding', 'failed', 'accruing')
	`, chargeID, note)
	if err != nil {
		return 0, fmt.Errorf("withhold unpaid by charge: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
