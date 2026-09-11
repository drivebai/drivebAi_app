package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
)

// Line 11 — a sale whose charge does not exist on this Stripe account is
// never paid from platform balance, on the live test-mode API: the executor
// asks Stripe, gets resource_missing, and parks the row withheld with a
// ticket. An admin revive is refused the same way. No transfer exists.
func TestSalesRehearsalLine11_UnresolvableChargeNeverTransfers(t *testing.T) {
	e := newSalesEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	seller := e.seedUser(t, "car_owner", "rh_s11_"+run+"@example.com")
	buyer := e.seedUser(t, "driver", "rh_b11_"+run+"@example.com")
	e.seedLicense(t, buyer)
	car := e.seedCar(t, seller, "sold", true, false)
	e.connectSeller(t, seller) // a REAL account with transfers active — the refusal must be ours
	t.Cleanup(func() { e.db.Pool.Exec(ctx, `DELETE FROM support_tickets WHERE user_id = $1`, seller) })

	var chatID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `INSERT INTO chats (car_id, driver_id, owner_id) VALUES ($1,$2,$3) RETURNING id`, car, buyer, seller).Scan(&chatID); err != nil {
		t.Fatalf("chat: %v", err)
	}
	intent := "pi_does_not_exist_" + run
	var pid uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_requests (car_id, seller_id, buyer_id, chat_id, offer_amount_cents, currency, status,
			expires_at, payment_status, payment_intent_id, completed_at)
		VALUES ($1,$2,$3,$4, 900000, 'USD', 'completed', NOW() + INTERVAL '30 days', 'succeeded', $5, NOW())
		RETURNING id`, car, seller, buyer, chatID, intent).Scan(&pid); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	row, _, err := e.payoutRepo.CreateForSale(ctx, &models.OwnerPayout{
		PurchaseRequestID: &pid, OwnerID: seller,
		GrossKeptCents: 900000, FeeBPS: 1000, FeeCents: 90000, OwnerAmountCents: 810000,
		Currency: "USD", Status: models.PayoutPending, Source: models.PayoutSourceSaleCompleted,
	})
	if err != nil || row == nil {
		t.Fatalf("payout row: %v", err)
	}
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE purchase_request_id = $1`, pid)
		e.db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id = $1`, pid)
		e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
	})

	e.payoutH.executePayout(ctx, row)

	after, _ := e.payoutRepo.GetByPurchaseRequestID(ctx, pid)
	if after == nil || after.Status != models.PayoutWithheld {
		t.Fatalf("payout after execute = %v, want withheld", after)
	}
	if tr, ferr := e.stripe.FindTransferByGroup("sale-" + pid.String()); ferr != nil || tr != nil {
		t.Fatalf("a transfer exists for an unfundable sale (%v, err=%v) — platform money moved", tr, ferr)
	}
	var tickets int
	_ = e.db.Pool.QueryRow(ctx, `SELECT count(*) FROM support_tickets WHERE user_id=$1 AND subject LIKE 'Payout withheld%'`, seller).Scan(&tickets)
	if tickets != 1 {
		t.Errorf("withheld tickets = %d, want 1", tickets)
	}
	revived, rerr := e.payoutH.AdminReviveWithheld(ctx, after.ID, "line 11: revive must not bypass the gate")
	if rerr != nil || revived == nil || revived.Status != models.PayoutWithheld {
		t.Fatalf("revive = %v (err %v), want withheld again", revived, rerr)
	}
	if tr, _ := e.stripe.FindTransferByGroup("sale-" + pid.String()); tr != nil {
		t.Fatal("the revive transferred against a missing charge")
	}
	t.Logf("  line 11: %s parked withheld against %s; no transfer on Stripe; revive refused", after.ID, intent)
}
