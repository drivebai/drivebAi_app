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
	id, lease_request_id, owner_id, stripe_account_id,
	gross_kept_cents, fee_bps, fee_cents, owner_amount_cents, currency,
	status, source, source_charge_id, stripe_transfer_id, failure_reason, note,
	reminder_count, last_reminder_at, escalated_at, paid_at, created_at, updated_at`

func scanOwnerPayout(row scanRow) (*models.OwnerPayout, error) {
	var p models.OwnerPayout
	err := row.Scan(
		&p.ID, &p.LeaseRequestID, &p.OwnerID, &p.StripeAccountID,
		&p.GrossKeptCents, &p.FeeBPS, &p.FeeCents, &p.OwnerAmountCents, &p.Currency,
		&p.Status, &p.Source, &p.SourceChargeID, &p.StripeTransferID, &p.FailureReason, &p.Note,
		&p.ReminderCount, &p.LastReminderAt, &p.EscalatedAt, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
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
		ON CONFLICT (lease_request_id) DO NOTHING
		RETURNING `+ownerPayoutColumns,
		uuid.New(), p.LeaseRequestID, p.OwnerID, p.StripeAccountID,
		p.GrossKeptCents, p.FeeBPS, p.FeeCents, p.OwnerAmountCents, p.Currency,
		p.Status, p.Source, p.SourceChargeID, p.Note)
	created, err := scanOwnerPayout(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := r.GetByLeaseRequestID(ctx, p.LeaseRequestID)
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
func (r *PayoutRepository) MarkPaid(ctx context.Context, id uuid.UUID, transferID, accountID string) (*models.OwnerPayout, error) {
	row := r.db.Pool.QueryRow(ctx, `
		UPDATE owner_payouts
		SET status = 'paid', stripe_transfer_id = $2, stripe_account_id = $3,
		    failure_reason = NULL, paid_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'failed', 'awaiting_onboarding')
		RETURNING `+ownerPayoutColumns, id, transferID, accountID)
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
		WHERE id = $1 AND status <> 'paid'
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
