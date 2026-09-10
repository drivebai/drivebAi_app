package handlers

// Sales rehearsal: the car-sale lifecycle driven against LIVE Stripe test
// mode, with a real Connect account receiving the seller's transfer.
//
//	STRIPE_SECRET_KEY=sk_test_… REHEARSAL=1 \
//	  TEST_DATABASE_URL=postgres://…/mig48_scratch?sslmode=disable \
//	  go test ./internal/handlers/ -run TestSalesRehearsal -v -timeout 45m
//
// Same discipline as the batch-5 rolling rehearsal: every PaymentIntent,
// capture, transfer, reversal, refund and dispute is a real call to Stripe's
// test API, and every state transition runs through the production handlers.
// Only the webhook TRANSPORT is simulated — events are built from the real
// Stripe object and pushed through the same signature verification.

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

const saleRehearsalCents int64 = 900000 // $9,000.00 — a realistic used car

type salesEnv struct {
	*rehearsalEnv
	purchaseH    *PurchaseRequestHandler
	purchaseRepo *repository.PurchaseRequestRepository
}

func newSalesEnv(t *testing.T) *salesEnv {
	t.Helper()
	e := newRehearsalEnv(t)
	db := e.db
	// Honour REHEARSAL_VERBOSE, or a handler that logs-and-swallows its
	// failure leaves the rehearsal saying only "returned nil".
	logger := discardLogger()
	if os.Getenv("REHEARSAL_VERBOSE") == "1" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	purchaseRepo := repository.NewPurchaseRequestRepository(db)
	notifH := NewNotificationHandler(
		repository.NewNotificationRepository(db),
		repository.NewDeviceTokenRepository(db),
		ws.NewHub(logger), nil, logger)

	// The shared payout env builds its handler with a NIL Stripe service, so
	// no transfer it makes is real. A rehearsal whose money never moves
	// proves nothing — build one with the live test-mode service.
	payoutH := NewPayoutHandler(e.payoutRepo, e.leaseRepo,
		repository.NewUserRepository(db), e.ticketRepo,
		e.stripe, ws.NewHub(logger), notifH, payoutTestFeeBPS, logger)
	payoutH.SetPurchaseRepository(purchaseRepo)
	e.payoutH = payoutH
	e.returnH.SetPayoutHandler(payoutH)

	ph := NewPurchaseRequestHandler(purchaseRepo, e.carRepo,
		repository.NewUserRepository(db), repository.NewChatRepository(db), e.leaseRepo,
		e.stripe, ws.NewHub(logger), notifH, nil, t.TempDir(), logger)
	ph.SetPayoutHandler(payoutH)
	ph.SetPayoutRepository(e.payoutRepo)
	return &salesEnv{rehearsalEnv: e, purchaseH: ph, purchaseRepo: purchaseRepo}
}

// connectSeller creates a REAL Stripe Connect account in test mode and marks
// the seller ready, so the transfer at completion is a real transfer.
func (e *salesEnv) connectSeller(t *testing.T, sellerID uuid.UUID) string {
	t.Helper()
	// A fresh Express account has `transfers` REQUESTED but not ACTIVE until
	// its owner completes onboarding, and Stripe refuses the transfer with
	// insufficient_capabilities_for_transfer. A custom account prefilled
	// with Stripe's documented test values activates immediately, so the
	// rehearsal exercises a REAL transfer rather than proving our own error
	// handling.
	form := url.Values{}
	form.Set("type", "custom")
	form.Set("country", "US")
	form.Set("email", fmt.Sprintf("seller_%s@example.com", sellerID.String()[:8]))
	form.Set("business_type", "individual")
	form.Set("capabilities[transfers][requested]", "true")
	form.Set("tos_acceptance[date]", fmt.Sprintf("%d", time.Now().Unix()))
	form.Set("tos_acceptance[ip]", "127.0.0.1")
	form.Set("individual[first_name]", "Jenny")
	form.Set("individual[last_name]", "Rosen")
	form.Set("individual[email]", fmt.Sprintf("seller_%s@example.com", sellerID.String()[:8]))
	form.Set("individual[phone]", "+15555555555")
	form.Set("individual[dob][day]", "1")
	form.Set("individual[dob][month]", "1")
	form.Set("individual[dob][year]", "1901")
	form.Set("individual[address][line1]", "address_full_match")
	form.Set("individual[address][city]", "Schenectady")
	form.Set("individual[address][state]", "NY")
	form.Set("individual[address][postal_code]", "12345")
	form.Set("individual[address][country]", "US")
	form.Set("individual[id_number]", "000000000")
	form.Set("individual[ssn_last_4]", "0000")
	form.Set("business_profile[url]", "https://drivebai.com")
	form.Set("business_profile[mcc]", "7523")
	form.Set("external_account", "btok_us_verified")
	acct := e.call(t, "POST", "accounts", form)
	acctID, _ := acct["id"].(string)
	if acctID == "" {
		t.Fatalf("could not create a Connect account: %v", acct)
	}
	// Confirm transfers really are active; otherwise the line below would
	// fail for an environment reason and read like a product defect.
	got := e.call(t, "GET", "accounts/"+acctID, nil)
	caps, _ := got["capabilities"].(map[string]interface{})
	if caps == nil || caps["transfers"] != "active" {
		t.Skipf("Connect account %s has transfers=%v, not active — cannot rehearse a real transfer on this Stripe account",
			acctID, caps["transfers"])
	}
	if _, err := e.db.Pool.Exec(context.Background(),
		`UPDATE users SET stripe_account_id = $2, payout_status = 'ready' WHERE id = $1`,
		sellerID, acctID); err != nil {
		t.Fatalf("mark seller ready: %v", err)
	}
	return acctID
}

// saleFixture drives a purchase to awaiting_inspection with a REAL authorized
// PaymentIntent, which is the state every line below starts from.
type saleFixture struct {
	purchaseID uuid.UUID
	sellerID   uuid.UUID
	buyerID    uuid.UUID
	carID      uuid.UUID
	acctID     string
	intentID   string
}

func (e *salesEnv) startSale(t *testing.T, tag string, cardToken string) *saleFixture {
	t.Helper()
	ctx := context.Background()
	sellerID := e.seedUser(t, "car_owner", fmt.Sprintf("sale_seller_%s_%s@example.com", tag, uuid.NewString()[:8]))
	buyerID := e.seedUser(t, "driver", fmt.Sprintf("sale_buyer_%s_%s@example.com", tag, uuid.NewString()[:8]))
	e.seedLicense(t, buyerID)
	carID := e.seedCar(t, sellerID, "available", true, false)
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE cars SET is_for_sale = TRUE, sale_price = $2 WHERE id = $1`, carID, saleRehearsalCents/100); err != nil {
		t.Fatalf("list car for sale: %v", err)
	}
	acctID := e.connectSeller(t, sellerID)

	var chatID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx,
		`INSERT INTO chats (car_id, driver_id, owner_id) VALUES ($1,$2,$3) RETURNING id`,
		carID, buyerID, sellerID).Scan(&chatID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// A REAL authorized PaymentIntent: manual capture, on a real test card.
	pmID := e.newCardPM(t, cardToken)
	form := url.Values{}
	form.Set("amount", fmt.Sprintf("%d", saleRehearsalCents))
	form.Set("currency", "usd")
	form.Set("capture_method", "manual")
	form.Set("payment_method", pmID)
	form.Set("confirm", "true")
	form.Set("automatic_payment_methods[enabled]", "true")
	form.Set("automatic_payment_methods[allow_redirects]", "never")
	pi := e.call(t, "POST", "payment_intents", form)
	intentID, _ := pi["id"].(string)
	status, _ := pi["status"].(string)
	if intentID == "" {
		t.Fatalf("could not authorize: %v", pi)
	}
	t.Logf("  [%s] authorized %s status=%s amount=%d", tag, intentID, status, saleRehearsalCents)

	var purchaseID uuid.UUID
	if err := e.db.Pool.QueryRow(ctx, `
		INSERT INTO purchase_requests
			(car_id, seller_id, buyer_id, chat_id, offer_amount_cents, currency, status, expires_at,
			 accepted_at, keys_handed_over_at, inspection_deadline_at, payment_intent_id, payment_status,
			 auth_expires_at)
		VALUES ($1,$2,$3,$4,$5,'USD','awaiting_inspection', NOW() + INTERVAL '30 days',
			 NOW() - INTERVAL '3 days', NOW() - INTERVAL '1 hour', NOW() + INTERVAL '47 hours', $6,
			 'requires_capture', NOW() + INTERVAL '7 days')
		RETURNING id`, carID, sellerID, buyerID, chatID, saleRehearsalCents, intentID).Scan(&purchaseID); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE purchase_request_id = $1`, purchaseID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM purchase_requests WHERE id = $1`, purchaseID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
	})
	return &saleFixture{purchaseID: purchaseID, sellerID: sellerID, buyerID: buyerID,
		carID: carID, acctID: acctID, intentID: intentID}
}

// acceptInspection makes the buyer's acceptance, which is what production
// does before any capture: MarkCaptured is status-scoped to
// inspection_accepted, so capturing from awaiting_inspection takes the money
// at Stripe and leaves the row behind.
func (e *salesEnv) acceptInspection(t *testing.T, id uuid.UUID) *models.PurchaseRequest {
	t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(), `
		UPDATE purchase_requests
		SET status = 'inspection_accepted', inspection_accepted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'awaiting_inspection'`, id); err != nil {
		t.Fatalf("accept inspection: %v", err)
	}
	return e.purchaseRow(t, id)
}

func (e *salesEnv) purchaseRow(t *testing.T, id uuid.UUID) *models.PurchaseRequest {
	t.Helper()
	p, err := e.purchaseRepo.GetByID(context.Background(), id)
	if err != nil || p == nil {
		t.Fatalf("load purchase: %v", err)
	}
	return p
}

func (e *salesEnv) salePayout(t *testing.T, purchaseID uuid.UUID) *models.OwnerPayout {
	t.Helper()
	row, err := e.payoutRepo.GetByPurchaseRequestID(context.Background(), purchaseID)
	if err != nil {
		t.Fatalf("load sale payout: %v", err)
	}
	return row
}

// ─── Line 1: authorize → inspect → capture → transfer ───────────────────────

func TestSalesRehearsalLine1_CaptureAndSellerTransfer(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l1", "tok_visa")
	ctx := context.Background()

	before := e.purchaseRow(t, f.purchaseID)
	if before.Status != models.PurchaseStatusAwaitingInspection {
		t.Fatalf("fixture status = %s", before.Status)
	}

	// The buyer accepts the vehicle: capture + seller payout run inside.
	accepted := e.acceptInspection(t, f.purchaseID)
	updated := e.purchaseH.capturePayment(ctx, accepted)
	if updated == nil {
		t.Fatal("capture returned nil — the sale did not complete")
	}
	if updated.Status != models.PurchaseStatusCompleted {
		t.Errorf("status = %s, want completed", updated.Status)
	}
	t.Logf("  captured: purchase now %s", updated.Status)

	// The money actually moved at Stripe.
	pi := e.call(t, "GET", "payment_intents/"+f.intentID, nil)
	if st, _ := pi["status"].(string); st != "succeeded" {
		t.Errorf("stripe intent status = %s, want succeeded", st)
	}
	if got, _ := pi["amount_received"].(float64); int64(got) != saleRehearsalCents {
		t.Errorf("amount_received = %v, want %d", got, saleRehearsalCents)
	}

	// The seller was paid, through the shared ledger.
	row := e.salePayout(t, f.purchaseID)
	if row == nil {
		t.Fatal("no payout row for the sale — the seller has no money and no record")
	}
	wantFee, wantSeller := models.ComputePayoutSplit(saleRehearsalCents, payoutTestFeeBPS)
	if row.GrossKeptCents != saleRehearsalCents || row.FeeCents != wantFee || row.OwnerAmountCents != wantSeller {
		t.Errorf("split wrong: gross=%d fee=%d seller=%d (want %d/%d/%d)",
			row.GrossKeptCents, row.FeeCents, row.OwnerAmountCents, saleRehearsalCents, wantFee, wantSeller)
	}
	if row.PurchaseRequestID == nil || *row.PurchaseRequestID != f.purchaseID {
		t.Error("payout row is not linked to the purchase")
	}
	if row.LeaseRequestID != nil {
		t.Error("a sale payout carries a lease id — the one-source rule is broken")
	}
	if row.Status != models.PayoutPaid || row.StripeTransferID == nil {
		t.Fatalf("seller not paid: status=%s transfer=%v", row.Status, row.StripeTransferID)
	}
	t.Logf("  seller paid: %d¢ of %d¢ (fee %d¢) via %s",
		row.OwnerAmountCents, row.GrossKeptCents, row.FeeCents, *row.StripeTransferID)

	// The transfer exists at Stripe, for the right amount, to the right account.
	tr := e.call(t, "GET", "transfers/"+*row.StripeTransferID, nil)
	if amt, _ := tr["amount"].(float64); int64(amt) != wantSeller {
		t.Errorf("stripe transfer amount = %v, want %d", amt, wantSeller)
	}
	if dest, _ := tr["destination"].(string); dest != f.acctID {
		t.Errorf("transfer destination = %s, want %s", dest, f.acctID)
	}

	// The car is sold.
	var carStatus string
	_ = e.db.Pool.QueryRow(ctx, `SELECT status FROM cars WHERE id = $1`, f.carID).Scan(&carStatus)
	if carStatus != "sold" {
		t.Errorf("car status = %s, want sold", carStatus)
	}
}

// ─── Line 2: the capture is idempotent (crash between capture and mark) ─────

func TestSalesRehearsalLine2_CaptureIsIdempotent(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l2", "tok_visa")
	ctx := context.Background()

	// The real crash window: Stripe captured, then the process died before
	// MarkCaptured committed. The row is left at inspection_accepted, which
	// is exactly what runCaptureRetry looks for.
	first := e.purchaseH.capturePayment(ctx, e.acceptInspection(t, f.purchaseID))
	if first == nil {
		t.Fatal("first capture failed")
	}
	rowA := e.salePayout(t, f.purchaseID)
	transferA := ""
	if rowA.StripeTransferID != nil {
		transferA = *rowA.StripeTransferID
	}
	t.Logf("  captured once: payout %s transfer %s", rowA.ID, transferA)

	// Rewind the row to exactly what the crash would have left: the capture
	// happened at Stripe but MarkCaptured never committed, so the status is
	// inspection_accepted and payment_status is still requires_capture.
	// Age it past the sweep's 2-minute grace, which exists so the sweep
	// never races a capture the request path just started. The updated_at
	// trigger has to be stood down to age a row at all.
	if _, err := e.db.Pool.Exec(ctx,
		`ALTER TABLE purchase_requests DISABLE TRIGGER set_purchase_requests_updated_at`); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	_, uerr := e.db.Pool.Exec(ctx, `
		UPDATE purchase_requests
		SET status = 'inspection_accepted', payment_status = 'requires_capture',
		    completed_at = NULL, updated_at = NOW() - INTERVAL '5 minutes'
		WHERE id = $1`, f.purchaseID)
	// MarkCaptured moves the purchase AND marks the car sold in ONE
	// transaction, so a crash before it commits leaves the car unsold too.
	// Rewinding only the purchase would test a state the system cannot
	// actually reach.
	if _, cerr := e.db.Pool.Exec(ctx, `
		UPDATE cars SET status = 'available', is_paused = FALSE, is_for_sale = TRUE,
		    archived_at = NULL, reserved_by_purchase_request_id = NULL
		WHERE id = $1`, f.carID); cerr != nil {
		t.Fatalf("rewind car: %v", cerr)
	}
	if _, err := e.db.Pool.Exec(ctx,
		`ALTER TABLE purchase_requests ENABLE TRIGGER set_purchase_requests_updated_at`); err != nil {
		t.Fatalf("re-enable trigger: %v", err)
	}
	if uerr != nil {
		t.Fatalf("rewind: %v", uerr)
	}
	e.purchaseH.runCaptureRetry(ctx)

	after := e.purchaseRow(t, f.purchaseID)
	if after.Status != models.PurchaseStatusCompleted {
		t.Errorf("retry did not finish the sale: status = %s", after.Status)
	}
	rowB := e.salePayout(t, f.purchaseID)
	if rowA.ID != rowB.ID {
		t.Errorf("retry created a SECOND payout row: %s then %s", rowA.ID, rowB.ID)
	}
	if rowB.StripeTransferID != nil && transferA != "" && *rowB.StripeTransferID != transferA {
		t.Errorf("retry made a SECOND transfer: %s then %s", transferA, *rowB.StripeTransferID)
	}
	t.Logf("  retry safe: one payout %s, one transfer %v", rowB.ID, rowB.StripeTransferID)

	// Stripe agrees only one capture happened.
	pi := e.call(t, "GET", "payment_intents/"+f.intentID, nil)
	if got, _ := pi["amount_received"].(float64); int64(got) != saleRehearsalCents {
		t.Errorf("amount_received = %v after the retry, want %d (no double capture)", got, saleRehearsalCents)
	}
	t.Logf("  stripe amount_received still %d¢", saleRehearsalCents)

	// And capturing from a state that is NOT inspection_accepted must take
	// no money at all: the row is completed now.
	if again := e.purchaseH.capturePayment(ctx, after); again != nil {
		t.Error("capture from 'completed' was allowed — money could be taken with the row left behind")
	}
}

// ─── Line 3: the inspection window expires into a capture ───────────────────

func TestSalesRehearsalLine3_WindowExpiresIntoCapture(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l3", "tok_visa")
	ctx := context.Background()

	// Push the deadline into the past, exactly as 48 hours of silence would.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE purchase_requests SET inspection_deadline_at = NOW() - INTERVAL '1 minute' WHERE id = $1`,
		f.purchaseID); err != nil {
		t.Fatalf("age the window: %v", err)
	}

	e.purchaseH.runInspectionExpiry(ctx)

	after := e.purchaseRow(t, f.purchaseID)
	if after.Status != models.PurchaseStatusCompleted {
		t.Fatalf("silence did not complete the sale: status = %s", after.Status)
	}
	if after.InspectionAutoAcceptedAt == nil {
		t.Error("no record that the WINDOW completed this sale rather than the buyer")
	}
	t.Logf("  window closed → %s, auto-accepted at %v", after.Status, after.InspectionAutoAcceptedAt)

	row := e.salePayout(t, f.purchaseID)
	if row == nil || row.Status != models.PayoutPaid {
		t.Fatalf("seller not paid after window expiry: %v", row)
	}
	t.Logf("  seller paid %d¢ after the window closed", row.OwnerAmountCents)

	// Running the sweep again must not pay twice.
	e.purchaseH.runInspectionExpiry(ctx)
	again := e.salePayout(t, f.purchaseID)
	if again.ID != row.ID {
		t.Error("a second sweep created another payout")
	}
}

// ─── Line 4: refund claws back the seller's share ───────────────────────────

func TestSalesRehearsalLine4_RefundReversesSellerTransfer(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l4", "tok_visa")
	ctx := context.Background()

	completed := e.purchaseH.capturePayment(ctx, e.acceptInspection(t, f.purchaseID))
	if completed == nil {
		t.Fatal("capture failed")
	}
	paid := e.salePayout(t, f.purchaseID)
	if paid.Status != models.PayoutPaid || paid.StripeTransferID == nil {
		t.Fatalf("seller was not paid, nothing to reverse: %v", paid)
	}
	transferID := *paid.StripeTransferID

	// Refund the buyer in full, then claw the seller's share back.
	refund := e.call(t, "POST", "refunds", url.Values{
		"payment_intent": {f.intentID},
		"amount":         {fmt.Sprintf("%d", saleRehearsalCents)},
	})
	if rid, _ := refund["id"].(string); rid == "" {
		t.Fatalf("refund failed: %v", refund)
	}
	e.payoutH.ReverseSalePayoutForRefund(ctx, f.purchaseID, saleRehearsalCents, "rehearsal")

	after := e.salePayout(t, f.purchaseID)
	if after.Status != models.PayoutReversed {
		t.Errorf("payout status = %s, want reversed", after.Status)
	}
	// The model does not carry the reversal amount; read the ledger directly.
	var reversedCents int64
	if err := e.db.Pool.QueryRow(ctx,
		`SELECT reversed_amount_cents FROM owner_payouts WHERE id = $1`, after.ID).Scan(&reversedCents); err != nil {
		t.Fatalf("read reversed amount: %v", err)
	}
	if reversedCents != paid.OwnerAmountCents {
		t.Errorf("reversed %d¢, want the seller's full share %d¢", reversedCents, paid.OwnerAmountCents)
	}
	t.Logf("  refunded %d¢ to buyer, reversed %d¢ from seller", saleRehearsalCents, reversedCents)

	// Stripe agrees the reversal happened on the real transfer.
	tr := e.call(t, "GET", "transfers/"+transferID, nil)
	if rev, _ := tr["amount_reversed"].(float64); int64(rev) != paid.OwnerAmountCents {
		t.Errorf("stripe amount_reversed = %v, want %d", rev, paid.OwnerAmountCents)
	}

	// Reversing twice must not take the seller's money twice.
	e.payoutH.ReverseSalePayoutForRefund(ctx, f.purchaseID, saleRehearsalCents, "rehearsal_replay")
	tr2 := e.call(t, "GET", "transfers/"+transferID, nil)
	if rev, _ := tr2["amount_reversed"].(float64); int64(rev) != paid.OwnerAmountCents {
		t.Errorf("replay changed amount_reversed to %v, want %d", rev, paid.OwnerAmountCents)
	}
	t.Logf("  replay safe: amount_reversed still %d¢", paid.OwnerAmountCents)
}

// ─── Line 5: a dispute withholds an unpaid seller payout ────────────────────

func TestSalesRehearsalLine5_DisputeWithholdsUnpaidSellerPayout(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l5", "tok_visa")
	ctx := context.Background()

	// Seller is NOT onboarded, so the payout parks instead of transferring —
	// the state a dispute must be able to stop.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE users SET stripe_account_id = NULL, payout_status = 'none' WHERE id = $1`, f.sellerID); err != nil {
		t.Fatalf("un-onboard seller: %v", err)
	}
	completed := e.purchaseH.capturePayment(ctx, e.acceptInspection(t, f.purchaseID))
	if completed == nil {
		t.Fatal("capture failed")
	}
	row := e.salePayout(t, f.purchaseID)
	if row == nil || row.Status != models.PayoutAwaitingOnboarding {
		t.Fatalf("expected an escrowed payout, got %v", row)
	}
	t.Logf("  seller not onboarded: %d¢ parked as %s", row.OwnerAmountCents, row.Status)

	// The charge that funds it is what a dispute names.
	pi := e.call(t, "GET", "payment_intents/"+f.intentID, nil)
	chargeID, _ := pi["latest_charge"].(string)
	if chargeID == "" {
		t.Fatalf("no charge on the intent: %v", pi)
	}
	// Stamp the funding charge the way MarkPaid would, so the withhold has
	// something to match on.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE owner_payouts SET source_charge_id = $2 WHERE id = $1`, row.ID, chargeID); err != nil {
		t.Fatalf("stamp charge: %v", err)
	}

	n, werr := e.payoutRepo.WithholdUnpaidByChargeID(ctx, chargeID, "dispute: rehearsal")
	if werr != nil {
		t.Fatalf("withhold: %v", werr)
	}
	if n != 1 {
		t.Errorf("withheld %d rows, want 1 — a disputed sale kept marching toward a transfer", n)
	}
	after := e.salePayout(t, f.purchaseID)
	if after.Status != models.PayoutWithheld {
		t.Errorf("payout status = %s, want withheld", after.Status)
	}
	t.Logf("  dispute withheld the seller payout by charge %s", chargeID)
}

// ─── Line 6: an admin upholding a rejection never pays the seller ───────────

func TestSalesRehearsalLine6_RejectionUpheldPaysNobodyEarly(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l6", "tok_visa")
	ctx := context.Background()

	// Buyer rejects before any capture.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE purchase_requests SET status = 'inspection_rejected' WHERE id = $1`, f.purchaseID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if row := e.salePayout(t, f.purchaseID); row != nil {
		t.Fatal("a payout exists before any capture — the seller would be paid for a rejected sale")
	}

	// The window sweep must not complete a REJECTED sale.
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE purchase_requests SET inspection_deadline_at = NOW() - INTERVAL '1 minute' WHERE id = $1`,
		f.purchaseID); err != nil {
		t.Fatalf("age: %v", err)
	}
	e.purchaseH.runInspectionExpiry(ctx)
	after := e.purchaseRow(t, f.purchaseID)
	if after.Status != models.PurchaseStatusInspectionRejected {
		t.Errorf("the sweep overrode a rejection: status = %s", after.Status)
	}
	if row := e.salePayout(t, f.purchaseID); row != nil {
		t.Error("the sweep paid the seller on a rejected sale")
	}
	t.Logf("  rejection stands, no payout: status=%s", after.Status)

	// The authorization is still releasable — the buyer's money is untouched.
	pi := e.call(t, "GET", "payment_intents/"+f.intentID, nil)
	if st, _ := pi["status"].(string); st != "requires_capture" {
		t.Errorf("intent status = %s, want requires_capture (money never taken)", st)
	}
	if got, _ := pi["amount_received"].(float64); int64(got) != 0 {
		t.Errorf("amount_received = %v, want 0 on a rejected sale", got)
	}
	t.Logf("  buyer's money untouched: status=requires_capture, received=0")
}

// ─── Line 7: the kill switch stops sales starting, not sales landing ────────

func TestSalesRehearsalLine7_KillSwitchLetsInFlightSalesLand(t *testing.T) {
	e := newSalesEnv(t)
	f := e.startSale(t, "l7", "tok_visa")
	ctx := context.Background()

	e.purchaseH.SetSalesDisabled(true)
	defer e.purchaseH.SetSalesDisabled(false)

	// An in-flight sale still completes: stranding a buyer who already
	// authorized money and took delivery is worse than the problem the
	// switch exists to contain.
	updated := e.purchaseH.capturePayment(ctx, e.acceptInspection(t, f.purchaseID))
	if updated == nil || updated.Status != models.PurchaseStatusCompleted {
		t.Fatalf("kill switch stranded an in-flight sale: %v", updated)
	}
	row := e.salePayout(t, f.purchaseID)
	if row == nil || row.Status != models.PayoutPaid {
		t.Fatalf("in-flight sale did not pay its seller: %v", row)
	}
	t.Logf("  switch on: in-flight sale completed and paid the seller %d¢", row.OwnerAmountCents)
}
