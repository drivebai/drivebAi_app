package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Line 0 — the checkout path itself. Every other line seeds its consent by
// hand; this one drives CreatePaymentIntent for a rolling lease against the
// live test-mode API and proves what the row records: the v3 package (the
// text that discloses the persistent balance), and — once the payment
// webhook activates it — the saved card's brand, last4 and fingerprint.
func TestBatch5_Line0_CheckoutRecordsV3AndCardFingerprint(t *testing.T) {
	e := newRehearsalEnv(t)
	ctx := context.Background()
	run := uuid.NewString()[:8]
	owner := e.seedUser(t, "car_owner", "rh_o_v3_"+run+"@example.com")
	driver := e.seedUser(t, "driver", "rh_d_v3_"+run+"@example.com")
	e.seedLicense(t, driver)
	car := e.seedCar(t, owner, "available", true, false)

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, rollingLeaseReq(t, driver, car))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create rolling lease: %d (%s)", rr.Code, rr.Body.String())
	}
	var created struct {
		LeaseRequest struct {
			ID uuid.UUID `json:"id"`
		} `json:"lease_request"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	leaseID := created.LeaseRequest.ID
	t.Cleanup(func() {
		e.db.Pool.Exec(ctx, `DELETE FROM key_handovers WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM billing_cycles WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM lease_billing_consents WHERE lease_request_id=$1`, leaseID)
		e.db.Pool.Exec(ctx, `DELETE FROM payments WHERE lease_request_id=$1`, leaseID)
	})
	if _, err := e.leaseRepo.AcceptLeaseRequest(ctx, leaseID, owner); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// The driver's Stripe customer, bound as production binds it (H6).
	cust := e.call(t, "POST", "customers", url.Values{"email": {"rh_v3_" + run + "@example.com"}})
	customerID := str(cust, "id")
	if customerID == "" {
		t.Fatalf("create customer: %v", cust)
	}
	if err := repository.NewUserRepository(e.db).SetStripeCustomerID(ctx, driver, customerID); err != nil {
		t.Fatalf("bind customer: %v", err)
	}

	// Checkout. The rolling branch records the consent BEFORE the intent.
	rr = httptest.NewRecorder()
	e.leaseH.CreatePaymentIntent(rr, paymentIntentReq(t, driver, leaseID))
	if rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("checkout: %d (%s)", rr.Code, rr.Body.String())
	}
	consent, err := e.billingRepo.GetActiveConsent(ctx, leaseID)
	if err != nil || consent == nil {
		t.Fatalf("consent after checkout: %v (%v)", consent, err)
	}
	if consent.TermsVersion != models.TermsVersionRollingV3 {
		t.Errorf("checkout recorded terms %q, want %q", consent.TermsVersion, models.TermsVersionRollingV3)
	}
	if consent.DisclosureText != models.RollingDriverDisclosureV3(consent.AmountCents) {
		t.Errorf("checkout recorded text that is not the v3 package for $%.2f", float64(consent.AmountCents)/100)
	}
	if consent.ActivatedAt != nil {
		t.Fatal("consent active before any payment")
	}

	// The intent the handler created, from Stripe's side; the driver pays
	// it with a saveable test card, as PaymentSheet would.
	list := e.call(t, "GET", "payment_intents?customer="+customerID+"&limit=1", nil)
	data, _ := list["data"].([]interface{})
	if len(data) != 1 {
		t.Fatalf("expected one intent on the customer, got %v", list)
	}
	intentID := str(data[0].(map[string]interface{}), "id")
	confirmed := e.call(t, "POST", "payment_intents/"+intentID+"/confirm", url.Values{
		"payment_method": {"pm_card_visa"},
		"return_url":     {"https://drivebai.com/return"},
	})
	if str(confirmed, "status") != "succeeded" {
		t.Fatalf("confirm: %v", confirmed)
	}

	// The payment webhook: SetPaid, then activation with the card's details.
	if code := e.deliverPIEvent(t, "payment_intent.succeeded", intentID); code != 200 {
		t.Fatalf("payment webhook: %d", code)
	}
	consent, _ = e.billingRepo.GetActiveConsent(ctx, leaseID)
	if consent == nil || consent.ActivatedAt == nil {
		t.Fatal("consent not activated by the payment webhook")
	}
	if consent.CardFingerprint == nil || *consent.CardFingerprint == "" {
		t.Error("activation recorded no card fingerprint — returning-debtor matching would be blind")
	}
	if consent.CardBrand == nil || *consent.CardBrand != "visa" {
		t.Errorf("card brand = %v, want visa", consent.CardBrand)
	}
	if consent.CardLast4 == nil || *consent.CardLast4 != "4242" {
		t.Errorf("card last4 = %v, want 4242", consent.CardLast4)
	}
	if consent.CardFingerprint != nil && consent.CardBrand != nil && consent.CardLast4 != nil {
		t.Logf("  line 0: checkout recorded %s; webhook activated with %s •••• %s, fingerprint %s",
			consent.TermsVersion, *consent.CardBrand, *consent.CardLast4, *consent.CardFingerprint)
	}
}

func rollingLeaseReq(t *testing.T, driverID, listingID uuid.UUID) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("listingId", listingID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/listings/"+listingID.String()+"/lease-requests",
		strings.NewReader(`{"weeks":1,"billing_mode":"rolling"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, driverID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}
