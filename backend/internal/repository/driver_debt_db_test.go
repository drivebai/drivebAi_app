package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// Driver debt ledger. DB-gated; the database must be migrated through 000058.
//
//	TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//	  go test ./internal/repository/ -run TestDriverDebt -v
func debtTestEnv(t *testing.T) (*database.DB, *DriverDebtRepository, uuid.UUID, uuid.UUID, func(int, int64) uuid.UUID, func()) {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated >=000058) to run driver debt tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx := context.Background()
	ownerID, driverID, carID, chatID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []struct {
		id    uuid.UUID
		email string
		role  string
	}{{ownerID, "debt_owner_" + ownerID.String() + "@example.com", "car_owner"},
		{driverID, "debt_driver_" + driverID.String() + "@example.com", "driver"}} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO users (id, email, password_hash, role, first_name, last_name, is_email_verified, onboarding_status)
			VALUES ($1, $2, 'x', $3, 'D', 'T', TRUE, 'created')`, u.id, u.email, u.role); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO cars (id, owner_id, make, model, year, title, weekly_rent_price, currency, status)
		VALUES ($1, $2, 'Honda', 'CR-V', 2021, 'Debt Test Car', 149.94, 'USD', 'available')`,
		carID, ownerID); err != nil {
		t.Fatalf("seed car: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO chats (id, car_id, driver_id, owner_id) VALUES ($1,$2,$3,$4)`,
		chatID, carID, driverID, ownerID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	leaseRepo := NewLeaseRequestRepository(db)
	lr, err := leaseRepo.CreateLeaseRequest(ctx, &models.LeaseRequest{
		ChatID: chatID, ListingID: carID, OwnerID: ownerID, DriverID: driverID,
		WeeklyPrice: 149.94, Currency: "USD", Weeks: 1, BillingMode: models.BillingModeRolling,
	})
	if err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	// A debt points at a real uncollected week; the FK enforces that, so the
	// fixture must seed one rather than invent an id.
	seedCycle := func(n int, cents int64) uuid.UUID {
		var id uuid.UUID
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO billing_cycles (lease_request_id, cycle_number, period_start, period_end, amount_cents, status)
			VALUES ($1, $2, NOW() - INTERVAL '7 days', NOW(), $3, 'arrears_due')
			RETURNING id`, lr.ID, n, cents).Scan(&id); err != nil {
			t.Fatalf("seed cycle: %v", err)
		}
		return id
	}

	cleanup := func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM driver_debt_entries WHERE debt_id IN (SELECT id FROM driver_debts WHERE driver_id=$1)`, driverID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM driver_debts WHERE driver_id=$1`, driverID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, lr.ID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE chat_id=$1`, chatID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM chats WHERE id=$1`, chatID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM cars WHERE id=$1`, carID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, ownerID, driverID)
		db.Close()
	}
	return db, NewDriverDebtRepository(db), driverID, lr.ID, seedCycle, cleanup
}

func TestDriverDebt_OpensOnceAndSums(t *testing.T) {
	_, repo, driverID, leaseID, seedCycle, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	cycleA := seedCycle(1, 6426)
	snap := models.DriverDebtSnapshot{Email: "d@example.com", Phone: "+15550100", Name: "Debt Tester", CardFingerprint: "fp_abc", CardLast4: "4242"}

	d1, created, err := repo.OpenForCycle(ctx, driverID, leaseID, cycleA, 6426, "USD", snap)
	if err != nil || !created || d1 == nil {
		t.Fatalf("first open: created=%v err=%v", created, err)
	}
	// Re-entry (a sweep running twice) must not raise a second debt.
	d2, created2, err := repo.OpenForCycle(ctx, driverID, leaseID, cycleA, 6426, "USD", snap)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if created2 {
		t.Error("second open created a duplicate debt for the same cycle")
	}
	if d2.ID != d1.ID {
		t.Errorf("second open returned a different debt: %s vs %s", d2.ID, d1.ID)
	}

	// A second debt on a DIFFERENT rental week must sum into one balance —
	// the whole point of a driver-level ledger.
	if _, _, err := repo.OpenForCycle(ctx, driverID, leaseID, seedCycle(2, 2142), 2142, "USD", snap); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	bal, err := repo.BalanceFor(ctx, driverID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal.OutstandingCents != 6426+2142 {
		t.Errorf("balance = %d, want %d (two debts must sum)", bal.OutstandingCents, 6426+2142)
	}
	if bal.OpenDebtCount != 2 {
		t.Errorf("open debt count = %d, want 2", bal.OpenDebtCount)
	}
	if !bal.HasBalance() {
		t.Error("HasBalance() false with money outstanding")
	}
}

func TestDriverDebt_PartialPaymentAndIdempotency(t *testing.T) {
	_, repo, driverID, leaseID, seedCycle, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	cycleA := seedCycle(1, 6426)

	debt, _, err := repo.OpenForCycle(ctx, driverID, leaseID, cycleA, 10000, "USD", models.DriverDebtSnapshot{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Partial payment leaves the debt open with a reduced balance.
	after, applied, err := repo.ApplyPayment(ctx, debt.ID, 3000, "pi_partial_1", "driver")
	if err != nil || !applied {
		t.Fatalf("partial: applied=%v err=%v", applied, err)
	}
	if after.OutstandingCents != 7000 || after.Status != models.DebtOpen {
		t.Errorf("after partial: outstanding=%d status=%s, want 7000/open", after.OutstandingCents, after.Status)
	}

	// The SAME intent redelivered must move no money at all.
	again, applied2, err := repo.ApplyPayment(ctx, debt.ID, 3000, "pi_partial_1", "driver")
	if err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if applied2 {
		t.Error("redelivered webhook applied the payment twice")
	}
	if again.OutstandingCents != 7000 {
		t.Errorf("redelivery changed the balance to %d, want 7000", again.OutstandingCents)
	}

	// Paying the rest closes it.
	done, applied3, err := repo.ApplyPayment(ctx, debt.ID, 7000, "pi_partial_2", "driver")
	if err != nil || !applied3 {
		t.Fatalf("final: applied=%v err=%v", applied3, err)
	}
	if done.OutstandingCents != 0 || done.Status != models.DebtPaid || done.ClosedAt == nil {
		t.Errorf("after final: outstanding=%d status=%s closed=%v", done.OutstandingCents, done.Status, done.ClosedAt)
	}
	bal, _ := repo.BalanceFor(ctx, driverID)
	if bal.OutstandingCents != 0 || bal.HasBalance() {
		t.Errorf("balance after payoff = %d, want 0", bal.OutstandingCents)
	}
}

func TestDriverDebt_OverpaymentNeverGoesNegative(t *testing.T) {
	_, repo, driverID, leaseID, seedCycle, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	cycleA := seedCycle(1, 6426)
	debt, _, err := repo.OpenForCycle(ctx, driverID, leaseID, cycleA, 5000, "USD", models.DriverDebtSnapshot{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	after, applied, err := repo.ApplyPayment(ctx, debt.ID, 9000, "pi_over", "driver")
	if err != nil || !applied {
		t.Fatalf("overpay: applied=%v err=%v", applied, err)
	}
	if after.OutstandingCents != 0 {
		t.Errorf("overpayment left outstanding=%d, want 0 (never negative)", after.OutstandingCents)
	}
}

func TestDriverDebt_WaiveClosesAndIsClaimedOnce(t *testing.T) {
	_, repo, driverID, leaseID, seedCycle, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	cycleA := seedCycle(1, 6426)
	debt, _, err := repo.OpenForCycle(ctx, driverID, leaseID, cycleA, 4000, "USD", models.DriverDebtSnapshot{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ok, err := repo.Close(ctx, debt.ID, models.DebtWaived, "admin", "goodwill")
	if err != nil || !ok {
		t.Fatalf("waive: ok=%v err=%v", ok, err)
	}
	twice, err := repo.Close(ctx, debt.ID, models.DebtWaived, "admin", "again")
	if err != nil {
		t.Fatalf("second waive: %v", err)
	}
	if twice {
		t.Error("waive was not claimed-once")
	}
	bal, _ := repo.BalanceFor(ctx, driverID)
	if bal.OutstandingCents != 0 {
		t.Errorf("waived debt still counts: %d", bal.OutstandingCents)
	}
}

func TestDriverDebt_FingerprintMatchExcludesSelf(t *testing.T) {
	_, repo, driverID, leaseID, seedCycle, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	cycleA := seedCycle(1, 6426)
	snap := models.DriverDebtSnapshot{CardFingerprint: "fp_shared_1"}
	if _, _, err := repo.OpenForCycle(ctx, driverID, leaseID, cycleA, 3000, "USD", snap); err != nil {
		t.Fatalf("open: %v", err)
	}
	// The same person asking about themselves is not a match.
	self, err := repo.MatchOpenDebtsByFingerprint(ctx, "fp_shared_1", driverID)
	if err != nil {
		t.Fatalf("match self: %v", err)
	}
	if len(self) != 0 {
		t.Errorf("own debt reported as a match against self: %d rows", len(self))
	}
	// A different account presenting the same card IS a review signal.
	other, err := repo.MatchOpenDebtsByFingerprint(ctx, "fp_shared_1", uuid.New())
	if err != nil {
		t.Fatalf("match other: %v", err)
	}
	if len(other) != 1 {
		t.Errorf("fingerprint match = %d rows, want 1", len(other))
	}
}
