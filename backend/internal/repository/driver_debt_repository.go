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

// BlockingBalanceFor is the number the BOOKING GATE reads — deliberately not
// the same number the app displays.
//
// BalanceFor answers "what does this driver owe". This answers the narrower,
// harder question: "what does this driver owe THAT THEY CAN ACTUALLY PAY
// RIGHT NOW". Only the second may refuse a booking.
//
// The reason the two must differ is that the refusal and the remedy read
// different tables. The gate reads driver_debts.status; both exits — the
// driver's POST /lease-requests/{id}/billing/pay-now and the admin's cycle
// waive — read billing_cycles.status. Nothing forces those to agree, and two
// reachable states drive them apart:
//
//  1. cycle 'paid', debt still open. handleArrearsPaid flips the cycle to
//     'paid' before it credits the debt; if the credit fails it returns
//     false so Stripe redelivers, and redelivery self-heals — but Stripe's
//     redelivery window is finite. After it closes the driver has PAID IN
//     FULL and is blocked forever.
//  2. cycle 'waived', debt still open. The waive's debt close used to be
//     best-effort. Worse, a driver who then tries to pay is routed to
//     refundLateChargeOnSettledCycle, which hands the money back and never
//     touches the debt — we take a payment, return it, and leave them
//     blocked.
//
// In both shapes PayNow answers NOTHING_DUE and the admin waive answers
// CYCLE_NOT_WAIVABLE, so the only remaining action is a hand-written UPDATE.
// "Support runs SQL" is not an exit. And because the gate sits BEFORE the
// billing-mode branch, a driver stranded this way loses the ENTIRE
// marketplace, fixed-term included — over a weekly rental they already paid.
//
// So the predicate below IS PayNow's precondition list, clause for clause.
// A debt can only block when the pay button would mint a real intent for it.
// "Every state needs an exit" stops being something we audit for and becomes
// something the query cannot violate.
//
// Sweep doctrine (docs/DESIGN_RECONCILIATION_SWEEPS.md): this is the v98
// addendum's rule — the executor is the last gate and it fails closed. The
// debt ledger's CREATION side already carries a feature-live floor
// (ListArrearsCyclesWithoutDebt below); its CONSUMPTION side — this gate —
// had no bound in either direction. Classification after this change:
// safe_by_design. What this deliberately stops blocking is picked up by
// ListOpenDebtsWithoutLiveExit, so an orphan becomes a ledger problem with a
// human on the end of it rather than a stranded user.
func (r *DriverDebtRepository) BlockingBalanceFor(ctx context.Context, driverID uuid.UUID) (*models.DriverBalance, error) {
	b := &models.DriverBalance{DriverID: driverID, Currency: "USD"}
	err := r.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(dd.outstanding_cents), 0), COUNT(*)
		FROM driver_debts dd
		LEFT JOIN billing_cycles bc ON bc.id = dd.billing_cycle_id
		LEFT JOIN lease_requests lr ON lr.id = dd.lease_request_id
		WHERE dd.driver_id = $1
		  AND dd.status = 'open'
		  AND (
		    -- ARM 1: a cycle-backed debt, payable through pay-now today.
		    --
		    -- Mirrors RollingDriverHandler.PayNow exactly:
		    --   lr.vehicle_returned_at IS NOT NULL  → the arrears branch
		    --   bc.status = 'arrears_due'           → not NOTHING_DUE
		    --   NOT EXISTS (higher money-bearing cycle)
		    --                                       → bc IS the anchor that
		    --      GetOpenOrLatestPaidCycle would return. Encoded as a
		    --      predicate rather than assumed from the one-open-cycle mint
		    --      guard, so this stays correct if that guard is relaxed.
		    --      'waived' is absent from the list for the same reason it is
		    --      absent there: no money ever moved.
		    (
		      dd.billing_cycle_id IS NOT NULL
		      AND lr.vehicle_returned_at IS NOT NULL
		      AND bc.status = 'arrears_due'
		      AND NOT EXISTS (
		        SELECT 1 FROM billing_cycles hb
		        WHERE hb.lease_request_id = bc.lease_request_id
		          AND hb.cycle_number > bc.cycle_number
		          AND hb.status IN ('paid', 'scheduled', 'charging', 'retrying',
		                            'needs_action', 'failed_final', 'arrears_due',
		                            'refunded', 'partially_refunded')
		      )
		    )
		    OR
		    -- ARM 2: a debt with NO cycle behind it, inside a bounded window.
		    --
		    -- Explicit, not a silent join effect. Migration 000058 made
		    -- billing_cycle_id nullable on purpose ("so a future debt source
		    -- (a sale, an admin adjustment) can live in the same ledger"). No
		    -- exit exists for that shape yet, so an inner join would let the
		    -- first one block forever, and dropping it entirely would let it
		    -- block nothing at all. It blocks for DebtBlocksNewRentalsFor and
		    -- then stops, with a ticket opened at the boundary.
		    --
		    -- Nothing can create such a debt today: this arm ships inert, as
		    -- the floor under a state the schema allows and the code does not
		    -- yet produce. Do NOT extend it to cycle-backed debts — there the
		    -- remedy is the coupling above, not a timer.
		    (
		      dd.billing_cycle_id IS NULL
		      AND dd.opened_at > NOW() - $2::interval
		    )
		  )
	`, driverID, models.DebtBlocksNewRentalsFor).Scan(&b.OutstandingCents, &b.OpenDebtCount)
	if err != nil {
		return nil, fmt.Errorf("blocking debt balance: %w", err)
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

// ArrearsCycleWithoutDebt is an uncollected week that never got a debt row.
type ArrearsCycleWithoutDebt struct {
	CycleID     uuid.UUID
	LeaseID     uuid.UUID
	DriverID    uuid.UUID
	AmountCents int64
	Currency    string
}

// ListArrearsCyclesWithoutDebt finds uncollected weeks that have no debt in
// the ledger.
//
// The window in which a debt can be opened is one-shot: SettleArrearsProRata
// succeeds exactly once, 'arrears_due' is excluded from every other sweep's
// lister, and the return flow explicitly treats an already-arrears cycle as
// "the debt is already recorded". So a single failed insert — a dropped
// connection, a cancelled request context — used to lose the debt forever:
// the driver owed money, was not blocked, and their balance read zero. This
// is the reconciliation that makes the ledger self-healing.
// OrphanDebt is an open debt whose billing cycle has already settled — the
// exact shape BlockingBalanceFor deliberately stops blocking on.
type OrphanDebt struct {
	DebtID      uuid.UUID
	DriverID    uuid.UUID
	LeaseID     uuid.UUID
	CycleID     uuid.UUID
	CycleStatus string
	IntentID    *string
	AmountCents int64
	Currency    string
}

// ListOpenDebtsWithoutLiveExit is the MIRROR of ListArrearsCyclesWithoutDebt
// above, and it exists because that one only ever looked in one direction.
//
// The forward sweep finds an arrears week with no debt row. This finds a debt
// row whose week is no longer in arrears — settled, waived or refunded —
// which means the driver has no way left to pay it and we have no way left to
// collect it. Before BlockingBalanceFor those rows silently stranded a driver;
// now they are permitted to go uncollected, so they MUST become visible.
// Doctrine: floors need a voice.
//
// Both bounds the doctrine demands are present and neither is optional:
//
//   - DebtLedgerLiveFrom, the same feature-live floor the forward sweep
//     carries. The doctrine's question is "if this looked back across all of
//     production history right now, what would it find?" — and the honest
//     answer for an unbounded version of this query is the same shape as the
//     sale reconcile that woke up on five July rows and began settling
//     $26,811. It finds nothing before the ledger existed because a debt row
//     could not exist then either.
//   - An age window, so a debt caught mid-webhook is never touched.
//     handleArrearsPaid flips the cycle to 'paid' and credits the debt about
//     fifty lines later; for that instant every row is legitimately an
//     "orphan". Two hours is far longer than that window and far shorter than
//     Stripe's redelivery window, so self-healing gets to happen first.
func (r *DriverDebtRepository) ListOpenDebtsWithoutLiveExit(ctx context.Context, limit int) ([]OrphanDebt, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT dd.id, dd.driver_id, dd.lease_request_id, bc.id, bc.status,
		       bc.stripe_payment_intent_id, dd.outstanding_cents,
		       COALESCE(NULLIF(dd.currency, ''), 'USD')
		FROM driver_debts dd
		JOIN billing_cycles bc ON bc.id = dd.billing_cycle_id
		WHERE dd.status = 'open'
		  AND dd.escalated_at IS NULL
		  AND bc.status IN ('paid', 'waived', 'refunded', 'partially_refunded')
		  AND dd.opened_at >= $2::timestamptz
		  AND dd.updated_at <= NOW() - interval '2 hours'
		ORDER BY dd.opened_at ASC
		LIMIT $1`, limit, models.DebtLedgerLiveFrom)
	if err != nil {
		return nil, fmt.Errorf("list orphan debts: %w", err)
	}
	defer rows.Close()
	out := make([]OrphanDebt, 0)
	for rows.Next() {
		var o OrphanDebt
		if serr := rows.Scan(&o.DebtID, &o.DriverID, &o.LeaseID, &o.CycleID,
			&o.CycleStatus, &o.IntentID, &o.AmountCents, &o.Currency); serr != nil {
			return nil, serr
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ClaimForEscalation stamps a debt so it is escalated exactly once. Returns
// false if another pass already took it.
func (r *DriverDebtRepository) ClaimForEscalation(ctx context.Context, debtID uuid.UUID) (bool, error) {
	var id uuid.UUID
	err := r.db.Pool.QueryRow(ctx, `
		UPDATE driver_debts SET escalated_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND escalated_at IS NULL AND status = 'open'
		RETURNING id`, debtID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim debt for escalation: %w", err)
	}
	return true, nil
}

func (r *DriverDebtRepository) ListArrearsCyclesWithoutDebt(ctx context.Context, limit int) ([]ArrearsCycleWithoutDebt, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT bc.id, bc.lease_request_id, lr.driver_id, bc.amount_cents,
		       COALESCE(NULLIF(lr.currency, ''), 'USD')
		FROM billing_cycles bc
		JOIN lease_requests lr ON lr.id = bc.lease_request_id
		LEFT JOIN driver_debts dd ON dd.billing_cycle_id = bc.id
		WHERE bc.status = 'arrears_due'
		  AND dd.id IS NULL
		  AND bc.amount_cents > 0
		  -- Never reach back before the ledger existed. An arrears week from
		  -- before that was never eligible for a debt row, so "missing" is
		  -- not a state it can be in.
		  AND bc.created_at >= $2::timestamptz
		ORDER BY bc.updated_at ASC
		LIMIT $1`, limit, models.DebtLedgerLiveFrom)
	if err != nil {
		return nil, fmt.Errorf("list arrears without debt: %w", err)
	}
	defer rows.Close()
	out := make([]ArrearsCycleWithoutDebt, 0)
	for rows.Next() {
		var a ArrearsCycleWithoutDebt
		if serr := rows.Scan(&a.CycleID, &a.LeaseID, &a.DriverID, &a.AmountCents, &a.Currency); serr != nil {
			return nil, serr
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
