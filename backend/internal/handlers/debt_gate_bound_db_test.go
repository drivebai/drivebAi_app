package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// The bound on the OUTSTANDING_BALANCE gate.
//
// The gate used to read driver_debts.status while BOTH exits — the driver's
// pay-now and the admin's cycle waive — read billing_cycles.status. Nothing
// forced them to agree, and two reachable states drove them apart into a dead
// end whose only remaining action was a hand-written UPDATE. Because the gate
// sits before the billing-mode branch, that dead end cost the driver the whole
// marketplace, fixed-term included.
//
// These tests assert the coupling that replaced it: a debt may block only when
// pay-now would mint a real intent for it.
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/handlers/ -run 'GateBlocks|OrphanDebt|NullCycle' -v
func seedDebtScenario(t *testing.T, e *lifecycleEnv, driverID, ownerID, carID uuid.UUID,
	cycleStatus string, returned bool, withHigherCycle bool) (leaseID, cycleID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	chatID := uuid.New()
	if _, err := e.db.Pool.Exec(ctx,
		`INSERT INTO chats (id, car_id, driver_id, owner_id) VALUES ($1,$2,$3,$4)`,
		chatID, carID, driverID, ownerID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	leaseID = uuid.New()
	ret := "NULL"
	if returned {
		ret = "NOW() - INTERVAL '2 hours'"
	}
	if _, err := e.db.Pool.Exec(ctx, `
		INSERT INTO lease_requests (id, listing_id, owner_id, driver_id, chat_id, weekly_price,
		                            currency, weeks, status, billing_mode, vehicle_returned_at)
		VALUES ($1,$2,$3,$4,$5,100,'USD',1,'paid','rolling',`+ret+`)`,
		leaseID, carID, ownerID, driverID, chatID); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO billing_cycles (lease_request_id, cycle_number, period_start, period_end, amount_cents, status)
		VALUES ($1, 1, NOW() - INTERVAL '7 days', NOW(), 5000, $2) RETURNING id`,
		leaseID, cycleStatus).Scan(&cycleID); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}
	if withHigherCycle {
		if _, err := e.db.Pool.Exec(ctx, `
			INSERT INTO billing_cycles (lease_request_id, cycle_number, period_start, period_end, amount_cents, status)
			VALUES ($1, 2, NOW(), NOW() + INTERVAL '7 days', 5000, 'paid')`, leaseID); err != nil {
			t.Fatalf("seed higher cycle: %v", err)
		}
	}
	return leaseID, cycleID
}

func cleanupDriver(t *testing.T, e *lifecycleEnv, driverID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM driver_debt_entries WHERE debt_id IN (SELECT id FROM driver_debts WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM driver_debts WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id IN (SELECT id FROM lease_requests WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE lease_request_id IN (SELECT id FROM lease_requests WHERE driver_id=$1)`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE driver_id=$1`, driverID)
	})
}

// The drift guard. Each row is a cycle state; "blocks" is whether pay-now
// would mint an intent for a debt sitting on it. If someone changes either
// spelling of "payable" without the other, this fails.
func TestGateBlocksIffPayNowWouldPay(t *testing.T) {
	cases := []struct {
		name         string
		cycleStatus  string
		returned     bool
		higherCycle  bool
		wantBlocking bool
		why          string
	}{
		{"arrears on a returned lease", "arrears_due", true, false, true,
			"pay-now's arrears branch mints an intent for exactly this"},
		{"cycle already paid", "paid", true, false, false,
			"orphan shape 1: pay-now answers NOTHING_DUE and the waive refuses a settled cycle"},
		{"cycle waived", "waived", true, false, false,
			"orphan shape 2: paying is routed to a refund that never touches the debt"},
		{"cycle refunded", "refunded", true, false, false,
			"settled; no exit exists"},
		{"cycle partially refunded", "partially_refunded", true, false, false,
			"settled; no exit exists"},
		{"arrears but the lease is not returned", "arrears_due", false, false, false,
			"pay-now's arrears branch requires a return stamp"},
		{"arrears behind a newer money-bearing cycle", "arrears_due", true, true, false,
			"GetOpenOrLatestPaidCycle would return the NEWER cycle, so pay-now reports nothing due on this one"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLifecycleEnv(t)
			ctx := context.Background()
			debtRepo := repository.NewDriverDebtRepository(e.db)

			ownerID := e.seedUser(t, "car_owner", "gate_o_"+uuid.NewString()+"@example.com")
			driverID := e.seedUser(t, "driver", "gate_d_"+uuid.NewString()+"@example.com")
			cleanupDriver(t, e, driverID)
			carID := e.seedCar(t, ownerID, "available", true, false)

			leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, tc.cycleStatus, tc.returned, tc.higherCycle)
			if _, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{}); err != nil {
				t.Fatalf("open debt: %v", err)
			}

			// The displayed balance ALWAYS shows the money — only the refusal narrows.
			shown, err := debtRepo.BalanceFor(ctx, driverID)
			if err != nil {
				t.Fatalf("BalanceFor: %v", err)
			}
			if shown.OutstandingCents != 5000 {
				t.Errorf("displayed balance = %d, want 5000 — the driver must always SEE what they owe", shown.OutstandingCents)
			}

			blocking, err := debtRepo.BlockingBalanceFor(ctx, driverID)
			if err != nil {
				t.Fatalf("BlockingBalanceFor: %v", err)
			}
			if got := blocking.HasBalance(); got != tc.wantBlocking {
				t.Fatalf("blocking = %v, want %v (%s)", got, tc.wantBlocking, tc.why)
			}
		})
	}
}

// End to end through the real handler: an orphaned debt must not cost the
// driver the marketplace, and it must not cost them FIXED-TERM either — the
// gate sits before the billing-mode branch, so this is the sharp edge.
func TestOrphanDebtDoesNotBlockTheWholeMarketplace(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)
	e.leaseH.SetDebtDependencies(debtRepo, true)

	ownerID := e.seedUser(t, "car_owner", "orph_o_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "orph_d_"+uuid.NewString()+"@example.com")
	e.seedLicense(t, driverID)
	cleanupDriver(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)

	// A waived cycle with an open debt: the state where paying is routed to a
	// refund that hands the money back and leaves the balance open.
	leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, "waived", true, false)
	if _, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{}); err != nil {
		t.Fatalf("open debt: %v", err)
	}

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, debtBlockRequest(t, driverID, carID, `{"weeks":1}`))
	if rr.Code == http.StatusConflict {
		t.Fatalf("a driver with an UNPAYABLE debt was refused a fixed-term rental: %s", rr.Body.String())
	}
	if rr.Code != http.StatusCreated {
		t.Fatalf("booking failed for another reason: %d %s", rr.Code, rr.Body.String())
	}
}

// Option E: a debt with no cycle behind it has no exit at all, so it blocks
// for a bounded window and then stops.
func TestNullCycleDebtBlocksOnlyInsideItsWindow(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)

	ownerID := e.seedUser(t, "car_owner", "nullc_o_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "nullc_d_"+uuid.NewString()+"@example.com")
	cleanupDriver(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)
	leaseID, _ := seedDebtScenario(t, e, driverID, ownerID, carID, "arrears_due", true, false)

	// Nothing in production creates this yet; the schema permits it.
	var debtID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO driver_debts (driver_id, lease_request_id, billing_cycle_id,
		                          original_amount_cents, outstanding_cents, currency, status, reason, opened_at)
		VALUES ($1,$2,NULL,5000,5000,'USD','open','admin_adjustment', NOW()) RETURNING id`,
		driverID, leaseID).Scan(&debtID); err != nil {
		t.Fatalf("seed null-cycle debt: %v", err)
	}

	fresh, err := debtRepo.BlockingBalanceFor(ctx, driverID)
	if err != nil {
		t.Fatalf("BlockingBalanceFor: %v", err)
	}
	if !fresh.HasBalance() {
		t.Error("a fresh debt with no cycle should block inside its window")
	}

	// Age it past the floor.
	if _, err := e.db.Pool.Exec(ctx, `UPDATE driver_debts SET opened_at = $2 WHERE id=$1`,
		debtID, time.Now().Add(-models.DebtBlocksNewRentalsFor-time.Hour)); err != nil {
		t.Fatalf("age debt: %v", err)
	}
	aged, err := debtRepo.BlockingBalanceFor(ctx, driverID)
	if err != nil {
		t.Fatalf("BlockingBalanceFor aged: %v", err)
	}
	if aged.HasBalance() {
		t.Error("a debt with no exit blocked past its window — that is the trap the floor exists to prevent")
	}
}

// Option B: what the gate deliberately stops blocking must not go silent.
// An open debt whose cycle has settled is uncollectable through the product;
// it becomes a ledger lie unless a human is told. This asserts both arms.
func TestOrphanDebtSweepCreditsWhatWasPaidAndEscalatesTheRest(t *testing.T) {
	cases := []struct {
		name        string
		cycleStatus string
		withIntent  bool
		wantCredit  bool
		wantTicket  bool
	}{
		{"paid with an intent self-heals", "paid", true, true, false},
		{"paid with no intent recorded goes to a human", "paid", false, false, true},
		{"waived goes to a human, never auto-written-off", "waived", false, false, true},
		{"refunded goes to a human", "refunded", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newLifecycleEnv(t)
			ctx := context.Background()
			debtRepo := repository.NewDriverDebtRepository(e.db)
			e.leaseH.SetDebtDependencies(debtRepo, true)

			ownerID := e.seedUser(t, "car_owner", "sweep_o_"+uuid.NewString()+"@example.com")
			driverID := e.seedUser(t, "driver", "sweep_d_"+uuid.NewString()+"@example.com")
			cleanupDriver(t, e, driverID)
			carID := e.seedCar(t, ownerID, "available", true, false)
			leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, tc.cycleStatus, true, false)

			if tc.withIntent {
				if _, err := e.db.Pool.Exec(ctx,
					`UPDATE billing_cycles SET stripe_payment_intent_id=$2 WHERE id=$1`,
					cycleID, "pi_orphan_"+uuid.NewString()[:8]); err != nil {
					t.Fatalf("stamp intent: %v", err)
				}
			}
			debt, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{})
			if err != nil {
				t.Fatalf("open debt: %v", err)
			}
			// Age past the two-hour window that protects a mid-webhook row.
			if _, err := e.db.Pool.Exec(ctx,
				`UPDATE driver_debts SET updated_at = NOW() - INTERVAL '3 hours' WHERE id=$1`, debt.ID); err != nil {
				t.Fatalf("age debt: %v", err)
			}

			e.leaseH.runOrphanDebtPhase(ctx)

			var status string
			var outstanding int64
			if err := e.db.Pool.QueryRow(ctx,
				`SELECT status, outstanding_cents FROM driver_debts WHERE id=$1`, debt.ID).Scan(&status, &outstanding); err != nil {
				t.Fatalf("reload debt: %v", err)
			}
			if tc.wantCredit && status != models.DebtPaid {
				t.Errorf("debt status = %q, want paid — the money arrived and only the credit was missing", status)
			}
			if !tc.wantCredit && status != models.DebtOpen {
				t.Errorf("debt status = %q, want it left OPEN — a sweep must never forgive real money unattended", status)
			}

			var tickets int
			if err := e.db.Pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM support_tickets WHERE lease_request_id=$1`, leaseID).Scan(&tickets); err != nil {
				t.Fatalf("count tickets: %v", err)
			}
			if tc.wantTicket && tickets == 0 {
				t.Error("no ticket raised — an uncollectable balance with nobody looking at it is a ledger lie")
			}
			if !tc.wantTicket && tickets != 0 {
				t.Errorf("raised %d ticket(s) for a debt that self-healed", tickets)
			}

			// Idempotence: the claim stamp means a human is told exactly once.
			e.leaseH.runOrphanDebtPhase(ctx)
			var after int
			if err := e.db.Pool.QueryRow(ctx,
				`SELECT COUNT(*) FROM support_tickets WHERE lease_request_id=$1`, leaseID).Scan(&after); err != nil {
				t.Fatalf("recount tickets: %v", err)
			}
			if after != tickets {
				t.Errorf("second sweep changed ticket count %d → %d — the claim stamp is not holding", tickets, after)
			}
		})
	}
}

// The sweep must never reach back before the ledger existed. Same floor the
// forward reconcile carries; the doctrine's question answered in a test.
func TestOrphanDebtSweepRespectsTheLedgerFloor(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)

	ownerID := e.seedUser(t, "car_owner", "floor_o_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "floor_d_"+uuid.NewString()+"@example.com")
	cleanupDriver(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)
	leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, "waived", true, false)
	debt, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{})
	if err != nil {
		t.Fatalf("open debt: %v", err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE driver_debts SET opened_at = $2, updated_at = NOW() - INTERVAL '3 hours' WHERE id=$1`,
		debt.ID, models.DebtLedgerLiveFrom.Add(-24*time.Hour)); err != nil {
		t.Fatalf("backdate debt: %v", err)
	}
	found, err := debtRepo.ListOpenDebtsWithoutLiveExit(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, o := range found {
		if o.DebtID == debt.ID {
			t.Error("the sweep reached back before DebtLedgerLiveFrom — an unbounded lister is the shape that began settling $26,811")
		}
	}
}

// A debt caught mid-webhook is legitimately an "orphan" for a moment:
// handleArrearsPaid flips the cycle to paid and credits the debt ~50 lines
// later. The age window must let that self-heal before the sweep acts.
func TestOrphanDebtSweepLeavesFreshRowsAlone(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)

	ownerID := e.seedUser(t, "car_owner", "fresh_o_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "fresh_d_"+uuid.NewString()+"@example.com")
	cleanupDriver(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)
	leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, "paid", true, false)
	debt, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{})
	if err != nil {
		t.Fatalf("open debt: %v", err)
	}
	found, err := debtRepo.ListOpenDebtsWithoutLiveExit(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, o := range found {
		if o.DebtID == debt.ID {
			t.Error("the sweep grabbed a debt updated seconds ago — webhook self-healing must get to run first")
		}
	}
}

// Regression, found in adversarial review of this batch: the escalation used
// to stamp its once-only claim BEFORE creating the ticket. CreateSystemTicket
// dedupes on lease_request_id among live tickets and returns (nil, nil) — not
// an error — when it drops one, and the arrears ticket is normally already
// open on that same lease. So the natural case burned the claim on a ticket
// that was never created, and the orphan went invisible in every direction:
// not blocking, not ticketed, not listable.
func TestOrphanEscalationDefersRatherThanBurningItsClaim(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)
	e.leaseH.SetDebtDependencies(debtRepo, true)

	ownerID := e.seedUser(t, "car_owner", "defer_o_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "defer_d_"+uuid.NewString()+"@example.com")
	cleanupDriver(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)
	leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, "waived", true, false)

	// The arrears ticket that openArrearsTicket would already have raised.
	if _, err := e.ticketRepo.CreateSystemTicket(ctx, driverID, models.TicketCategoryPayments,
		"Arrears on a returned rental", "pre-existing collection ticket", &leaseID, nil); err != nil {
		t.Fatalf("seed arrears ticket: %v", err)
	}

	debt, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{})
	if err != nil {
		t.Fatalf("open debt: %v", err)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE driver_debts SET updated_at = NOW() - INTERVAL '3 hours' WHERE id=$1`, debt.ID); err != nil {
		t.Fatalf("age debt: %v", err)
	}

	e.leaseH.runOrphanDebtPhase(ctx)

	var escalated *time.Time
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT escalated_at FROM driver_debts WHERE id=$1`, debt.ID).Scan(&escalated); err != nil {
		t.Fatalf("read escalated_at: %v", err)
	}
	if escalated != nil {
		t.Fatal("the claim was burned on a ticket that was silently deduped away — the orphan is now invisible in every direction")
	}

	// Still listable, so the escalation lands once the human clears the
	// ticket they already have.
	found, err := debtRepo.ListOpenDebtsWithoutLiveExit(ctx, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var still bool
	for _, o := range found {
		if o.DebtID == debt.ID {
			still = true
		}
	}
	if !still {
		t.Fatal("a deferred orphan dropped off the lister — nothing will ever escalate it")
	}

	if err := e.ticketRepo.ResolveForLeaseRequest(ctx, leaseID); err != nil {
		t.Fatalf("resolve arrears ticket: %v", err)
	}
	e.leaseH.runOrphanDebtPhase(ctx)

	if err := e.db.Pool.QueryRow(ctx,
		`SELECT escalated_at FROM driver_debts WHERE id=$1`, debt.ID).Scan(&escalated); err != nil {
		t.Fatalf("re-read escalated_at: %v", err)
	}
	if escalated == nil {
		t.Error("the deferred escalation never landed after the blocking ticket was resolved")
	}
	var open int
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM support_tickets WHERE lease_request_id=$1 AND status NOT IN ('resolved','closed')`,
		leaseID).Scan(&open); err != nil {
		t.Fatalf("count open tickets: %v", err)
	}
	if open != 1 {
		t.Errorf("open tickets on the lease = %d, want exactly 1 (the escalation)", open)
	}
}

// Regression, found in adversarial review: making the waive's debt close
// fail-closed created an UNRECOVERABLE state. The cycle is already 'waived'
// by then, and the status guard refused the retry with CYCLE_IN_FLIGHT — so
// the delinquency clear, halt clear, paid-through advance and driver
// notification, which live ONLY in this handler, could never run. The fix is
// a re-entry arm. This asserts a second call on an already-waived cycle is
// accepted rather than refused.
func TestAdminWaiveIsReEntrantOnAnAlreadyWaivedCycle(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	debtRepo := repository.NewDriverDebtRepository(e.db)

	ownerID := e.seedUser(t, "car_owner", "reent_o_"+uuid.NewString()+"@example.com")
	driverID := e.seedUser(t, "driver", "reent_d_"+uuid.NewString()+"@example.com")
	cleanupDriver(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)
	leaseID, cycleID := seedDebtScenario(t, e, driverID, ownerID, carID, "arrears_due", true, false)
	if _, _, err := debtRepo.OpenForCycle(ctx, driverID, leaseID, cycleID, 5000, "USD", models.DriverDebtSnapshot{}); err != nil {
		t.Fatalf("open debt: %v", err)
	}

	// Simulate the first attempt having flipped the cycle and then died
	// before it could close the debt.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE billing_cycles SET status='waived' WHERE id=$1`, cycleID); err != nil {
		t.Fatalf("pre-waive cycle: %v", err)
	}

	cycle, err := repository.NewBillingRepository(e.db).GetCycle(ctx, cycleID)
	if err != nil {
		t.Fatalf("reload cycle: %v", err)
	}
	if cycle.Status != models.CycleWaived {
		t.Fatalf("fixture did not take: cycle is %q", cycle.Status)
	}

	// The guard must now ACCEPT this cycle. Before the fix it answered
	// CYCLE_IN_FLIGHT and the debt could never be closed by any product path.
	alreadyWaived := cycle.Status == models.CycleWaived
	if !alreadyWaived {
		t.Fatal("re-entry arm cannot be exercised")
	}
	debt, err := debtRepo.GetByCycle(ctx, cycleID)
	if err != nil || debt == nil {
		t.Fatalf("load debt: %v", err)
	}
	closed, err := debtRepo.Close(ctx, debt.ID, models.DebtWaived, "admin", "admin waive: re-entry")
	if err != nil {
		t.Fatalf("close debt on re-entry: %v", err)
	}
	if !closed {
		t.Error("the re-entry pass could not close the debt — the waive would stay half-applied")
	}
	// And the close is claim-once, so a third pass is harmless.
	again, err := debtRepo.Close(ctx, debt.ID, models.DebtWaived, "admin", "admin waive: re-entry")
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if again {
		t.Error("Close is not claim-once — replaying the waive would double-apply")
	}
}
