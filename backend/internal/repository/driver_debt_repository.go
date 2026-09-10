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

// DriverDebtRepository owns the driver-level debt ledger.
type DriverDebtRepository struct {
	db *database.DB
}

func NewDriverDebtRepository(db *database.DB) *DriverDebtRepository {
	return &DriverDebtRepository{db: db}
}

const driverDebtColumns = `
	id, driver_id, lease_request_id, billing_cycle_id,
	original_amount_cents, outstanding_cents, currency, status, reason,
	snapshot_email, snapshot_phone, snapshot_name, card_fingerprint, card_last4,
	opened_at, closed_at, created_at, updated_at`

func scanDriverDebt(row pgx.Row) (*models.DriverDebt, error) {
	var d models.DriverDebt
	err := row.Scan(
		&d.ID, &d.DriverID, &d.LeaseRequestID, &d.BillingCycleID,
		&d.OriginalAmountCents, &d.OutstandingCents, &d.Currency, &d.Status, &d.Reason,
		&d.SnapshotEmail, &d.SnapshotPhone, &d.SnapshotName, &d.CardFingerprint, &d.CardLast4,
		&d.OpenedAt, &d.ClosedAt, &d.CreatedAt, &d.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// OpenForCycle records the debt for one uncollected week. Claimed-once by the
// unique index on billing_cycle_id, so a sweep that re-runs — or two sweeps
// racing — produce exactly one debt. Returns the existing row on a repeat so
// the caller can carry on without special-casing.
func (r *DriverDebtRepository) OpenForCycle(
	ctx context.Context,
	driverID, leaseID, cycleID uuid.UUID,
	owedCents int64,
	currency string,
	snap models.DriverDebtSnapshot,
) (*models.DriverDebt, bool, error) {
	if owedCents <= 0 {
		return nil, false, fmt.Errorf("open debt: owed must be positive, got %d", owedCents)
	}
	if currency == "" {
		currency = "USD"
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO driver_debts
			(driver_id, lease_request_id, billing_cycle_id, original_amount_cents,
			 outstanding_cents, currency, status, reason,
			 snapshot_email, snapshot_phone, snapshot_name, card_fingerprint, card_last4)
		VALUES ($1, $2, $3, $4, $4, $5, 'open', $6,
			 NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), NULLIF($10,''), NULLIF($11,''))
		ON CONFLICT (billing_cycle_id) WHERE billing_cycle_id IS NOT NULL DO NOTHING
		RETURNING `+driverDebtColumns,
		driverID, leaseID, cycleID, owedCents, currency, models.DebtReasonUncollectedRental,
		snap.Email, snap.Phone, snap.Name, snap.CardFingerprint, snap.CardLast4)

	created, serr := scanDriverDebt(row)
	if errors.Is(serr, pgx.ErrNoRows) {
		// Already open for this cycle — return what stands.
		existing, gerr := scanDriverDebt(tx.QueryRow(ctx,
			`SELECT `+driverDebtColumns+` FROM driver_debts WHERE billing_cycle_id = $1`, cycleID))
		if gerr != nil {
			return nil, false, fmt.Errorf("open debt: reread: %w", gerr)
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, false, cerr
		}
		return existing, false, nil
	}
	if serr != nil {
		return nil, false, fmt.Errorf("open debt: %w", serr)
	}
	if _, eerr := tx.Exec(ctx, `
		INSERT INTO driver_debt_entries (debt_id, kind, amount_cents, actor, note)
		VALUES ($1, 'opened', $2, 'system', 'uncollected rental days')`,
		created.ID, owedCents); eerr != nil {
		return nil, false, fmt.Errorf("open debt: entry: %w", eerr)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return nil, false, cerr
	}
	return created, true, nil
}

// BalanceFor is the one number the app, the owner's ticket and admin all read.
func (r *DriverDebtRepository) BalanceFor(ctx context.Context, driverID uuid.UUID) (*models.DriverBalance, error) {
	b := &models.DriverBalance{DriverID: driverID, Currency: "USD"}
	err := r.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(outstanding_cents), 0), COUNT(*)
		FROM driver_debts WHERE driver_id = $1 AND status = 'open'
	`, driverID).Scan(&b.OutstandingCents, &b.OpenDebtCount)
	if err != nil {
		return nil, fmt.Errorf("debt balance: %w", err)
	}
	return b, nil
}

// ListOpenFor returns the debts behind the balance, oldest first — the order
// a driver expects to pay them off in.
func (r *DriverDebtRepository) ListOpenFor(ctx context.Context, driverID uuid.UUID) ([]models.DriverDebt, error) {
	rows, err := r.db.Pool.Query(ctx,
		`SELECT `+driverDebtColumns+` FROM driver_debts
		 WHERE driver_id = $1 AND status = 'open' ORDER BY opened_at ASC`, driverID)
	if err != nil {
		return nil, fmt.Errorf("list debts: %w", err)
	}
	defer rows.Close()
	out := make([]models.DriverDebt, 0)
	for rows.Next() {
		d, serr := scanDriverDebt(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// GetByCycle finds the debt raised for one uncollected week.
func (r *DriverDebtRepository) GetByCycle(ctx context.Context, cycleID uuid.UUID) (*models.DriverDebt, error) {
	d, err := scanDriverDebt(r.db.Pool.QueryRow(ctx,
		`SELECT `+driverDebtColumns+` FROM driver_debts WHERE billing_cycle_id = $1`, cycleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// ApplyPayment credits money against a debt and closes it when it reaches
// zero. Partial payments are first-class: the balance simply falls.
//
// Idempotent on the Stripe intent id (unique index on the entry): a
// redelivered webhook inserts nothing and moves no money. Returns the debt as
// it stands after the call, and whether THIS call applied the money.
func (r *DriverDebtRepository) ApplyPayment(
	ctx context.Context, debtID uuid.UUID, amountCents int64, intentID, actor string,
) (*models.DriverDebt, bool, error) {
	if amountCents <= 0 {
		return nil, false, fmt.Errorf("apply payment: amount must be positive, got %d", amountCents)
	}
	if actor == "" {
		actor = "driver"
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The entry is the claim. A duplicate intent id conflicts and applies
	// nothing, which is what makes webhook redelivery safe.
	var entryID uuid.UUID
	claimErr := tx.QueryRow(ctx, `
		INSERT INTO driver_debt_entries (debt_id, kind, amount_cents, stripe_payment_intent_id, actor)
		VALUES ($1, 'payment', $2, NULLIF($3,''), $4)
		ON CONFLICT (stripe_payment_intent_id) WHERE stripe_payment_intent_id IS NOT NULL DO NOTHING
		RETURNING id`, debtID, amountCents, intentID, actor).Scan(&entryID)
	if errors.Is(claimErr, pgx.ErrNoRows) {
		current, gerr := scanDriverDebt(tx.QueryRow(ctx,
			`SELECT `+driverDebtColumns+` FROM driver_debts WHERE id = $1`, debtID))
		if gerr != nil {
			return nil, false, gerr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, false, cerr
		}
		return current, false, nil
	}
	if claimErr != nil {
		return nil, false, fmt.Errorf("apply payment: claim: %w", claimErr)
	}

	// Never drive the balance negative: an overpayment closes the debt at
	// zero, and the surplus is visible in the entry history.
	updated, uerr := scanDriverDebt(tx.QueryRow(ctx, `
		UPDATE driver_debts
		SET outstanding_cents = GREATEST(outstanding_cents - $2, 0),
		    status   = CASE WHEN outstanding_cents - $2 <= 0 THEN 'paid' ELSE status END,
		    closed_at = CASE WHEN outstanding_cents - $2 <= 0 THEN NOW() ELSE closed_at END,
		    updated_at = NOW()
		WHERE id = $1 AND status = 'open'
		RETURNING `+driverDebtColumns, debtID, amountCents))
	if errors.Is(uerr, pgx.ErrNoRows) {
		// Debt already closed by a waive or an earlier payment. The entry
		// stands as the record that this money arrived.
		current, gerr := scanDriverDebt(tx.QueryRow(ctx,
			`SELECT `+driverDebtColumns+` FROM driver_debts WHERE id = $1`, debtID))
		if gerr != nil {
			return nil, false, gerr
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			return nil, false, cerr
		}
		return current, false, nil
	}
	if uerr != nil {
		return nil, false, fmt.Errorf("apply payment: %w", uerr)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return nil, false, cerr
	}
	return updated, true, nil
}

// Close writes a debt off without money: an admin waive, or a write-off.
// Status-scoped so it cannot reopen or double-close.
func (r *DriverDebtRepository) Close(
	ctx context.Context, debtID uuid.UUID, status, actor, note string,
) (bool, error) {
	if status != models.DebtWaived && status != models.DebtWrittenOff {
		return false, fmt.Errorf("close debt: bad status %q", status)
	}
	kind := models.DebtEntryWaive
	if status == models.DebtWrittenOff {
		kind = models.DebtEntryWriteOff
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The entry records what was actually FORGIVEN — the outstanding amount
	// at this moment, not the original. A debt half paid off and then waived
	// would otherwise report a write-off twice its true size. Read it inside
	// the transaction, before the update, rather than relying on RETURNING
	// semantics for a value the same statement is overwriting.
	var remaining int64
	uerr := tx.QueryRow(ctx, `
		SELECT outstanding_cents FROM driver_debts
		WHERE id = $1 AND status = 'open' FOR UPDATE`, debtID).Scan(&remaining)
	if errors.Is(uerr, pgx.ErrNoRows) {
		return false, nil
	}
	if uerr != nil {
		return false, fmt.Errorf("close debt: read outstanding: %w", uerr)
	}
	var closedID uuid.UUID
	uerr = tx.QueryRow(ctx, `
		UPDATE driver_debts
		SET status = $2, outstanding_cents = 0, closed_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'open'
		RETURNING id`, debtID, status).Scan(&closedID)
	if errors.Is(uerr, pgx.ErrNoRows) {
		return false, nil
	}
	if uerr != nil {
		return false, fmt.Errorf("close debt: %w", uerr)
	}
	if _, eerr := tx.Exec(ctx, `
		INSERT INTO driver_debt_entries (debt_id, kind, amount_cents, actor, note)
		VALUES ($1, $2, $3, $4, NULLIF($5,''))`,
		debtID, kind, remaining, actor, note); eerr != nil {
		return false, fmt.Errorf("close debt: entry: %w", eerr)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return false, cerr
	}
	return true, nil
}

// MatchOpenDebtsByFingerprint finds open debts whose card fingerprint matches
// one presented by a (possibly different) account. It is a REVIEW SIGNAL: the
// caller must never auto-block on it. Excludes the account presenting it, so
// a driver's own debt is not reported as a match against themselves.
func (r *DriverDebtRepository) MatchOpenDebtsByFingerprint(
	ctx context.Context, fingerprint string, excludeDriverID uuid.UUID,
) ([]models.DriverDebt, error) {
	if fingerprint == "" {
		return nil, nil
	}
	rows, err := r.db.Pool.Query(ctx,
		`SELECT `+driverDebtColumns+` FROM driver_debts
		 WHERE card_fingerprint = $1 AND status = 'open' AND driver_id <> $2
		 ORDER BY opened_at ASC`, fingerprint, excludeDriverID)
	if err != nil {
		return nil, fmt.Errorf("match debts: %w", err)
	}
	defer rows.Close()
	out := make([]models.DriverDebt, 0)
	for rows.Next() {
		d, serr := scanDriverDebt(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}
