package handlers

// Batch-5 rehearsal: the rolling lifecycle driven against LIVE Stripe test
// mode, with the customer and its saved card living on a Stripe TEST CLOCK.
//
//	STRIPE_SECRET_KEY=sk_test_… REHEARSAL=1 \
//	  TEST_DATABASE_URL=postgres://…/mig48_scratch?sslmode=disable \
//	  go test ./internal/handlers/ -run TestBatch5 -v -timeout 30m
//
// What is REAL here: every PaymentIntent, confirm, charge, refund, dispute,
// SetupIntent and test-clock advance is a live call to Stripe's test API,
// and every state transition runs through the production handlers.
//
// What is SIMULATED: webhook TRANSPORT. Without the Stripe CLI we cannot
// receive Stripe's own delivery, so each event is built from the REAL
// object retrieved from Stripe, signed with the local webhook secret, and
// pushed through HandleWebhook — the same signature verification, routing
// and handler path production uses. Only the network hop is ours.
//
// Our engine's clock is the DATABASE clock (rental_ends_at + time.Now()),
// not Stripe's; the Stripe test clock governs Stripe-side time. Weeks are
// advanced on BOTH: the lease rows are aged (exactly as the 60s scanner
// would eventually see them) and the test clock is advanced in step.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

const (
	rehearsalWebhookSecret = "whsec_rehearsal_local_only"
	rehearsalWeeklyCents   = 15000
	// Stated here in cents, INDEPENDENTLY of models.ComputeReturnRefund, so
	// the rehearsal has its own arithmetic to check the engine against
	// (harness review: expectations computed by the function under test
	// prove nothing). $150.00 / 7 days = $21.42 per day, integer cents.
	rehearsalPerDayCents = 2142

	// Stripe TEST TOKENS, not raw PANs: this account (like most) has raw
	// card data APIs disabled, and tokens are the supported way to pick a
	// specific test behaviour.
	cardSuccess    = "tok_visa"
	cardMastercard = "tok_mastercard"
	cardDecline    = "tok_chargeCustomerFail" // attaches, then fails on every charge
	cardDispute    = "tok_createDispute"      // succeeds, then Stripe raises a dispute
)

type rehearsalEnv struct {
	*payoutEnv
	sk          string
	stripe      *stripeService.Service
	billingRepo *repository.BillingRepository
}

func newRehearsalEnv(t *testing.T) *rehearsalEnv {
	t.Helper()
	if os.Getenv("REHEARSAL") != "1" {
		t.Skip("set REHEARSAL=1 (and STRIPE_SECRET_KEY) to run the live test-clock rehearsal")
	}
	sk := os.Getenv("STRIPE_SECRET_KEY")
	if !strings.HasPrefix(sk, "sk_test_") {
		t.Fatalf("rehearsal requires a TEST secret key (sk_test_…); refusing to run")
	}
	e := newPayoutEnv(t)
	// REHEARSAL_VERBOSE=1 surfaces handler errors that are otherwise
	// logged-and-swallowed — the rehearsal must be able to see them.
	logger := discardLogger()
	if os.Getenv("REHEARSAL_VERBOSE") == "1" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	svc := stripeService.NewService(sk, "pk_test_rehearsal", rehearsalWebhookSecret, payoutTestFeeBPS, logger)

	// Full production wiring, with the real Stripe service everywhere.
	db := e.db
	billingRepo := repository.NewBillingRepository(db)
	disputeRepo := repository.NewChargeDisputeRepository(db)
	notifH := NewNotificationHandler(
		repository.NewNotificationRepository(db),
		repository.NewDeviceTokenRepository(db),
		ws.NewHub(logger), nil, logger)

	leaseH := NewLeaseRequestHandler(
		e.leaseRepo, e.carRepo,
		repository.NewCarDocumentRepository(db),
		repository.NewUserRepository(db),
		repository.NewChatRepository(db),
		repository.NewDocumentRepository(db),
		repository.NewSharedDocumentRepository(db),
		repository.NewKeyHandoverRepository(db),
		svc, ws.NewHub(logger), notifH, nil, time.Hour, logger)
	leaseH.SetTicketRepository(e.ticketRepo)
	leaseH.SetDisputeDependencies(disputeRepo, e.payoutRepo)
	leaseH.SetReturnRepositoryForDisputes(e.returnRepo)
	leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	returnH := NewVehicleReturnHandler(
		e.returnRepo, e.leaseRepo, e.carRepo,
		repository.NewUserRepository(db),
		repository.NewChatRepository(db),
		svc, ws.NewHub(logger), notifH, logger)
	returnH.SetTicketRepository(e.ticketRepo)
	returnH.SetDisputeRepository(disputeRepo)
	returnH.SetPayoutHandler(e.payoutH)
	returnH.SetBillingDependencies(billingRepo, e.payoutRepo, payoutTestFeeBPS)

	e.leaseH = leaseH
	e.returnH = returnH
	return &rehearsalEnv{payoutEnv: e, sk: sk, stripe: svc, billingRepo: billingRepo}
}

// ─── Raw Stripe helpers (test-file local; prod service stays clean) ─────────

func (e *rehearsalEnv) call(t *testing.T, method, path string, form url.Values) map[string]interface{} {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, "https://api.stripe.com/v1/"+path, body)
	if err != nil {
		t.Fatalf("stripe req: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.sk)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stripe %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("stripe decode %s: %v (%s)", path, err, string(raw)[:min(len(raw), 300)])
	}
	if resp.StatusCode >= 400 {
		if e, ok := out["error"].(map[string]interface{}); ok {
			t.Logf("    stripe %s %s → %d %v/%v", method, path, resp.StatusCode, e["code"], e["message"])
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func str(m map[string]interface{}, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func num(m map[string]interface{}, k string) int64 {
	if v, ok := m[k].(float64); ok {
		return int64(v)
	}
	return 0
}

// createTestClock freezes Stripe time at `at`.
func (e *rehearsalEnv) createTestClock(t *testing.T, at time.Time) string {
	t.Helper()
	out := e.call(t, "POST", "test_helpers/test_clocks", url.Values{
		"frozen_time": {fmt.Sprintf("%d", at.Unix())},
		"name":        {"drivebai-rehearsal"},
	})
	id := str(out, "id")
	if id == "" {
		t.Fatalf("create test clock failed: %v", out)
	}
	t.Cleanup(func() { e.call(t, "DELETE", "test_helpers/test_clocks/"+id, nil) })
	return id
}

// advanceTestClock moves Stripe time forward and waits for it to settle.
func (e *rehearsalEnv) advanceTestClock(t *testing.T, clockID string, to time.Time) {
	t.Helper()
	e.call(t, "POST", "test_helpers/test_clocks/"+clockID+"/advance", url.Values{
		"frozen_time": {fmt.Sprintf("%d", to.Unix())},
	})
	for i := 0; i < 60; i++ {
		out := e.call(t, "GET", "test_helpers/test_clocks/"+clockID, nil)
		switch str(out, "status") {
		case "ready":
			return
		case "internal_failure":
			t.Fatalf("test clock advance failed: %v", out)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("test clock did not become ready")
}

// newCardPM mints a PaymentMethod from a Stripe test token, which selects
// the card behaviour (success / decline / dispute) the line needs.
func (e *rehearsalEnv) newCardPM(t *testing.T, token string) string {
	id, _, _ := e.newCardPMDetail(t, token)
	return id
}

// newCardPMDetail also returns the brand and last4 Stripe assigned, so the
// consent record mirrors the real card rather than a guess.
func (e *rehearsalEnv) newCardPMDetail(t *testing.T, token string) (id, brand, last4 string) {
	t.Helper()
	out := e.call(t, "POST", "payment_methods", url.Values{
		"type":        {"card"},
		"card[token]": {token},
	})
	id = str(out, "id")
	if id == "" {
		t.Fatalf("create PM from %s failed: %v", token, out)
	}
	if card, ok := out["card"].(map[string]interface{}); ok {
		brand, last4 = str(card, "brand"), str(card, "last4")
	}
	return id, brand, last4
}

func (e *rehearsalEnv) attachPM(t *testing.T, pmID, customerID string) {
	t.Helper()
	out := e.call(t, "POST", "payment_methods/"+pmID+"/attach", url.Values{"customer": {customerID}})
	if str(out, "id") == "" {
		t.Fatalf("attach PM: %v", out)
	}
}

// bookingCharge is the driver's on-session first payment: it charges the
// card AND saves it under the stored-credential framework, exactly like the
// PaymentSheet call the app makes.
func (e *rehearsalEnv) bookingCharge(t *testing.T, customerID, pmID string, cents int64, meta map[string]string) map[string]interface{} {
	t.Helper()
	form := url.Values{
		"amount":                 {fmt.Sprintf("%d", cents)},
		"currency":               {"usd"},
		"customer":               {customerID},
		"payment_method":         {pmID},
		"confirm":                {"true"},
		"off_session":            {"false"},
		"setup_future_usage":     {"off_session"},
		"payment_method_types[]": {"card"},
	}
	for k, v := range meta {
		form.Set("metadata["+k+"]", v)
	}
	pi := e.call(t, "POST", "payment_intents", form)
	if str(pi, "status") != "succeeded" {
		t.Fatalf("booking charge not succeeded: %v", pi)
	}
	return pi
}

// ─── Webhook injection: real object, real signature, real routing ──────────

func (e *rehearsalEnv) deliverWebhook(t *testing.T, eventType string, object map[string]interface{}) int {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{
		"id":     "evt_rehearsal_" + uuid.New().String()[:8],
		"object": "event",
		"type":   eventType,
		// stripe-go's ConstructEvent REJECTS an event whose api_version
		// differs from the version the library pins — the same version the
		// production webhook endpoints are pinned to.
		"api_version": "2025-02-24.acacia",
		"created":     time.Now().Unix(),
		"data":        map[string]interface{}{"object": object},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(rehearsalWebhookSecret))
	mac.Write([]byte(ts + "." + string(payload)))
	sig := "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stripe/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Stripe-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	e.leaseH.HandleWebhook(rr, req)
	return rr.Code
}

// deliverPIEvent retrieves the CURRENT object from Stripe and delivers it.
func (e *rehearsalEnv) deliverPIEvent(t *testing.T, eventType, intentID string) int {
	t.Helper()
	pi := e.call(t, "GET", "payment_intents/"+intentID, nil)
	return e.deliverWebhook(t, eventType, pi)
}

// ─── Lease scaffolding on the local DB ─────────────────────────────────────

type rehearsalLease struct {
	leaseID, carID, owner, driver uuid.UUID
	customerID, pmID              string
	clockID                       string
	pickup                        time.Time
}

// startRollingRental creates a rolling lease, pays week 1 with a REAL saved
// card on a test clock, drives the payment webhook, and confirms pickup —
// then lets the bootstrap sweep cycle week 1.
func (e *rehearsalEnv) startRollingRental(t *testing.T, tag, cardToken string, clockID string, now time.Time) *rehearsalLease {
	t.Helper()
	ctx := context.Background()
	runID := uuid.New().String()[:8]
	owner := e.seedUser(t, "car_owner", "rh_o_"+tag+"_"+runID+"@example.com")
	driver := e.seedUser(t, "driver", "rh_d_"+tag+"_"+runID+"@example.com")
	e.seedLicense(t, driver)
	leaseID, carID := e.seedActiveRental(t, owner, driver)
	e.cleanupLedger(t, leaseID)
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM vehicle_returns WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM charge_disputes WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM owner_payouts WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_amendment_offers WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
	})

	// Customer lives ON the test clock; card saved through a real charge.
	cust := e.call(t, "POST", "customers", url.Values{
		"test_clock": {clockID},
		"email":      {"rh_" + tag + "_" + runID + "@example.com"},
	})
	customerID := str(cust, "id")
	if customerID == "" {
		t.Fatalf("create clocked customer: %v", cust)
	}
	if err := repository.NewUserRepository(e.db).SetStripeCustomerID(ctx, driver, customerID); err != nil {
		t.Fatalf("bind customer: %v", err)
	}
	pmID, pmBrand, pmLast4 := e.newCardPMDetail(t, cardToken)
	e.attachPM(t, pmID, customerID)

	// The lease becomes rolling with its consent recorded (what the app's
	// consent screen + CreatePaymentIntent rolling branch produce).
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET billing_mode='rolling', weeks=1 WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("set rolling: %v", err)
	}
	if _, err := e.billingRepo.CreateConsent(ctx, &models.BillingConsent{
		LeaseRequestID: leaseID, DriverID: driver, AmountCents: rehearsalWeeklyCents,
		TermsVersion:   models.TermsVersionRollingV2,
		DisclosureText: models.RollingDriverDisclosureV2(rehearsalWeeklyCents),
	}); err != nil {
		t.Fatalf("consent: %v", err)
	}

	// seedActiveRental already drove accept → paid → pickup; week 1's money
	// is the REAL card charge below, recorded on the payments row exactly as
	// the booking webhook would.
	pi := e.bookingCharge(t, customerID, pmID, rehearsalWeeklyCents, nil)
	intentID := str(pi, "id")
	seedPaymentAt(t, e.payoutEnv, leaseID, rehearsalWeeklyCents, "succeeded", &intentID)
	// Activate the mandate from the real saved PM (what the payment webhook
	// does on a rolling lease).
	if ok, err := e.billingRepo.ActivateConsent(ctx, leaseID, str(pi, "payment_method"), pmBrand, pmLast4, "fp_rehearsal"); err != nil || !ok {
		t.Fatalf("activate consent: ok=%v err=%v", ok, err)
	}
	var pickup time.Time
	e.db.Pool.QueryRow(ctx, `SELECT pickup_confirmed_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&pickup)

	e.leaseH.runBillingSweep(ctx) // bootstrap week 1 into the cycle ledger
	return &rehearsalLease{leaseID: leaseID, carID: carID, owner: owner, driver: driver,
		customerID: customerID, pmID: pmID, clockID: clockID, pickup: pickup}
}

// ageWeeks moves the lease's clock back so the engine sees the next charge
// as due, mirroring what the 60s scanner sees when real time passes.
func (e *rehearsalEnv) ageLease(t *testing.T, leaseID uuid.UUID, d time.Duration) {
	t.Helper()
	if _, err := e.db.Pool.Exec(context.Background(), `
		UPDATE lease_requests
		SET pickup_confirmed_at = pickup_confirmed_at - $2::interval,
		    rental_ends_at = rental_ends_at - $2::interval
		WHERE id = $1`, leaseID, fmt.Sprintf("%d seconds", int(d.Seconds()))); err != nil {
		t.Fatalf("age lease: %v", err)
	}
	e.ageCyclesOnly(t, leaseID, d)
}

// ageCyclesOnly ages the cycle ledger and its payout rows WITHOUT making
// the lease due again — how you let already-charged weeks become consumed
// (and therefore payable) without provoking another charge.
func (e *rehearsalEnv) ageCyclesOnly(t *testing.T, leaseID uuid.UUID, d time.Duration) {
	t.Helper()
	iv := fmt.Sprintf("%d seconds", int(d.Seconds()))
	if _, err := e.db.Pool.Exec(context.Background(), `
		UPDATE billing_cycles SET period_start = period_start - $2::interval, period_end = period_end - $2::interval
		WHERE lease_request_id = $1`, leaseID, iv); err != nil {
		t.Fatalf("age cycles: %v", err)
	}
	// Promotion keys on the PAYOUT row's period_end, so it must move too.
	if _, err := e.db.Pool.Exec(context.Background(), `
		UPDATE owner_payouts SET period_start = period_start - $2::interval, period_end = period_end - $2::interval
		WHERE lease_request_id = $1 AND period_end IS NOT NULL`, leaseID, iv); err != nil {
		t.Fatalf("age payouts: %v", err)
	}
}

// sweepAndSettle runs the engine, then delivers the resulting charge's
// webhook (Stripe's own delivery is the only piece we stand in for).
func (e *rehearsalEnv) sweepAndSettle(t *testing.T, leaseID uuid.UUID) *models.BillingCycle {
	t.Helper()
	ctx := context.Background()
	e.leaseH.runBillingSweep(ctx)
	cycles, _ := e.billingRepo.ListCyclesForLease(ctx, leaseID)
	if len(cycles) == 0 {
		return nil
	}
	latest := cycles[len(cycles)-1]
	if latest.StripePaymentIntentID != nil && *latest.StripePaymentIntentID != "" {
		pi := e.call(t, "GET", "payment_intents/"+*latest.StripePaymentIntentID, nil)
		if str(pi, "status") == "succeeded" {
			if code := e.deliverWebhook(t, "payment_intent.succeeded", pi); code != 200 {
				t.Fatalf("webhook route rejected a real success event: %d", code)
			}
		}
	}
	fresh, _ := e.billingRepo.GetCycle(ctx, latest.ID)
	return fresh
}

func (e *rehearsalEnv) cycleCount(t *testing.T, leaseID uuid.UUID) int {
	var n int
	e.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM billing_cycles WHERE lease_request_id=$1`, leaseID).Scan(&n)
	return n
}

func (e *rehearsalEnv) paidThrough(t *testing.T, leaseID uuid.UUID) time.Time {
	var v time.Time
	e.db.Pool.QueryRow(context.Background(), `SELECT rental_ends_at FROM lease_requests WHERE id=$1`, leaseID).Scan(&v)
	return v
}

// countPIsForCycle asks STRIPE how many intents exist for a cycle — the
// idempotency proof (never two charges for one week). Listed by customer
// rather than via the search API, which is eventually consistent and
// reported 0 for an intent that had demonstrably just been created.
func (e *rehearsalEnv) countPIsForCycle(t *testing.T, customerID string, cycleID uuid.UUID) int {
	t.Helper()
	out := e.call(t, "GET", "payment_intents?limit=100&customer="+url.QueryEscape(customerID), nil)
	data, ok := out["data"].([]interface{})
	if !ok {
		return -1
	}
	n := 0
	for _, item := range data {
		pi, _ := item.(map[string]interface{})
		md, _ := pi["metadata"].(map[string]interface{})
		if md != nil && str(md, "billing_cycle_id") == cycleID.String() {
			n++
		}
	}
	return n
}

// refundTotal asks Stripe what has actually been returned to the driver
// for an intent — the independent check behind every refund assertion.
func (e *rehearsalEnv) refundTotal(t *testing.T, intentID string) int64 {
	t.Helper()
	if intentID == "" {
		return 0
	}
	out := e.call(t, "GET", "refunds?payment_intent="+intentID, nil)
	var total int64
	if data, ok := out["data"].([]interface{}); ok {
		for _, r := range data {
			total += num(r.(map[string]interface{}), "amount")
		}
	}
	return total
}
