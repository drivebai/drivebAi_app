package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// The guarantee ledger. A guaranteed payment is not a collected one, and the
// two must never be mistakable for each other.
//
//	TEST_DATABASE_URL=… go test ./internal/repository/ -run TestGuarantee -v
func TestGuaranteeIsClaimedOnceAndRecoveryReimbursesThePlatform(t *testing.T) {
	db, debtRepo, driverID, leaseID, seedCycle, cleanup := debtTestEnv(t)
	defer cleanup()
	_ = debtRepo
	_ = driverID
	ctx := context.Background()
	payoutRepo := NewPayoutRepository(db)
	cycleID := seedCycle(1, 6426)

	var ownerID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT owner_id FROM lease_requests WHERE id = $1`, leaseID).Scan(&ownerID); err != nil {
		t.Fatalf("owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE guarantee_for_cycle_id = $1`, cycleID)
	})

	fee, ownerShare := models.ComputePayoutSplit(6426, 1000)
	row, created, err := payoutRepo.CreateGuarantee(ctx, &models.OwnerPayout{
		LeaseRequestID: &leaseID, OwnerID: ownerID,
		GrossKeptCents: 6426, FeeBPS: 1000, FeeCents: fee, OwnerAmountCents: ownerShare,
		Currency: "USD", Status: models.PayoutPending, Source: models.PayoutSourceAdminSettlement,
	}, cycleID)
	if err != nil || !created {
		t.Fatalf("create guarantee: created=%v err=%v", created, err)
	}

	// One week can never be guaranteed twice.
	_, twice, err := payoutRepo.CreateGuarantee(ctx, &models.OwnerPayout{
		LeaseRequestID: &leaseID, OwnerID: ownerID,
		GrossKeptCents: 6426, FeeBPS: 1000, FeeCents: fee, OwnerAmountCents: ownerShare,
		Currency: "USD", Status: models.PayoutPending, Source: models.PayoutSourceAdminSettlement,
	}, cycleID)
	if err != nil {
		t.Fatalf("second guarantee: %v", err)
	}
	if twice {
		t.Error("the same week was guaranteed twice — the platform paid an owner two weeks for one")
	}

	// Exposure reflects it.
	total, err := payoutRepo.GuaranteeExposureTotal(ctx)
	if err != nil {
		t.Fatalf("exposure: %v", err)
	}
	if total.OutstandingCents < ownerShare {
		t.Errorf("outstanding = %d, expected at least the %d just fronted", total.OutstandingCents, ownerShare)
	}

	// Recovery from the driver reimburses the PLATFORM: outstanding falls,
	// and the owner's payment is untouched.
	ok, err := payoutRepo.ApplyGuaranteeRecovery(ctx, cycleID, ownerShare)
	if err != nil || !ok {
		t.Fatalf("apply recovery: ok=%v err=%v", ok, err)
	}
	after, err := payoutRepo.GetGuaranteeForCycle(ctx, cycleID)
	if err != nil || after == nil {
		t.Fatalf("reread: %v", err)
	}
	if after.OwnerAmountCents != row.OwnerAmountCents {
		t.Errorf("recovery changed what the OWNER was paid: %d then %d", row.OwnerAmountCents, after.OwnerAmountCents)
	}
	var recovered int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT guarantee_recovered_cents FROM owner_payouts WHERE id = $1`, after.ID).Scan(&recovered); err != nil {
		t.Fatalf("read recovered: %v", err)
	}
	if recovered != ownerShare {
		t.Errorf("recovered = %d, want %d", recovered, ownerShare)
	}

	// Recovery can never exceed what was fronted.
	if _, err := payoutRepo.ApplyGuaranteeRecovery(ctx, cycleID, 999999); err != nil {
		t.Fatalf("over-recovery: %v", err)
	}
	_ = db.Pool.QueryRow(ctx,
		`SELECT guarantee_recovered_cents FROM owner_payouts WHERE id = $1`, after.ID).Scan(&recovered)
	if recovered > after.OwnerAmountCents {
		t.Errorf("recovered %d exceeds the %d fronted", recovered, after.OwnerAmountCents)
	}
}

// A collected payout must never be counted as guarantee exposure.
func TestGuaranteeExposureExcludesCollectedPayouts(t *testing.T) {
	db, _, _, leaseID, _, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	payoutRepo := NewPayoutRepository(db)

	var ownerID uuid.UUID
	_ = db.Pool.QueryRow(ctx, `SELECT owner_id FROM lease_requests WHERE id = $1`, leaseID).Scan(&ownerID)
	before, _ := payoutRepo.GuaranteeExposureTotal(ctx)

	if _, _, err := payoutRepo.Create(ctx, &models.OwnerPayout{
		LeaseRequestID: &leaseID, OwnerID: ownerID,
		GrossKeptCents: 15000, FeeBPS: 1000, FeeCents: 1500, OwnerAmountCents: 13500,
		Currency: "USD", Status: models.PayoutPending, Source: models.PayoutSourceReturnCompleted,
	}); err != nil {
		t.Fatalf("create collected payout: %v", err)
	}
	t.Cleanup(func() { _, _ = db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE lease_request_id = $1`, leaseID) })

	after, err := payoutRepo.GuaranteeExposureTotal(ctx)
	if err != nil {
		t.Fatalf("exposure: %v", err)
	}
	if after.OutstandingCents != before.OutstandingCents {
		t.Errorf("a COLLECTED payout moved guarantee exposure: %d -> %d",
			before.OutstandingCents, after.OutstandingCents)
	}
}

// The payout EXECUTOR is the last gate before money moves, so the
// seller-payouts cutoff has to hold there too — bounding row creation alone
// left already-created rows free to execute the moment their seller onboarded.
func TestExecutorRefusesPreCutoffSalePayouts(t *testing.T) {
	db, _, _, leaseID, _, cleanup := debtTestEnv(t)
	defer cleanup()
	ctx := context.Background()
	payoutRepo := NewPayoutRepository(db)

	var sellerID, carID uuid.UUID
	_ = db.Pool.QueryRow(ctx, `SELECT owner_id, listing_id FROM lease_requests WHERE id = $1`, leaseID).Scan(&sellerID, &carID)
	// The seller is READY, which is what makes an awaiting_onboarding row
	// executable in the first place.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE users SET stripe_account_id = 'acct_test_exec', payout_status = 'ready' WHERE id = $1`, sellerID); err != nil {
		t.Fatalf("ready seller: %v", err)
	}

	mk := func(completedAt string) uuid.UUID {
		var buyerID uuid.UUID
		_ = db.Pool.QueryRow(ctx, `SELECT driver_id FROM lease_requests WHERE id = $1`, leaseID).Scan(&buyerID)
		var chatID uuid.UUID
		_ = db.Pool.QueryRow(ctx, `SELECT chat_id FROM lease_requests WHERE id = $1`, leaseID).Scan(&chatID)
		var pid uuid.UUID
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO purchase_requests (car_id, seller_id, buyer_id, chat_id, offer_amount_cents,
				currency, status, expires_at, payment_status, completed_at)
			VALUES ($1,$2,$3,$4, 900000, 'USD', 'completed', NOW() + INTERVAL '30 days', 'succeeded', $5::timestamptz)
			RETURNING id`, carID, sellerID, buyerID, chatID, completedAt).Scan(&pid); err != nil {
			t.Fatalf("seed purchase: %v", err)
		}
		if _, _, err := payoutRepo.CreateForSale(ctx, &models.OwnerPayout{
			PurchaseRequestID: &pid, OwnerID: sellerID,
			GrossKeptCents: 900000, FeeBPS: 1000, FeeCents: 90000, OwnerAmountCents: 810000,
			Currency: "USD", Status: models.PayoutAwaitingOnboarding, Source: models.PayoutSourceSaleCompleted,
		}); err != nil {
			t.Fatalf("seed payout: %v", err)
		}
		return pid
	}
	oldSale := mk("2026-07-14T00:00:00Z") // test-era, before seller payouts existed
	newSale := mk("2026-09-10T12:00:00Z") // after the cutoff
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE purchase_request_id IN ($1,$2)`, oldSale, newSale)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id IN ($1,$2)`, oldSale, newSale)
	})

	rows, err := payoutRepo.ListExecutable(ctx, time.Now().UTC(), 100)
	if err != nil {
		t.Fatalf("list executable: %v", err)
	}
	sawOld, sawNew := false, false
	for _, r := range rows {
		if r.PurchaseRequestID == nil {
			continue
		}
		if *r.PurchaseRequestID == oldSale {
			sawOld = true
		}
		if *r.PurchaseRequestID == newSale {
			sawNew = true
		}
	}
	if sawOld {
		t.Error("the executor would transfer platform money for a July test-era sale whose charge does not exist")
	}
	if !sawNew {
		t.Error("the executor skipped a legitimate post-cutoff sale — the bound is too aggressive")
	}
}
