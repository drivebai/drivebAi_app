package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Recurring-only (decision 2026-09-17, docs/DESIGN_RECURRING_BILLING.md §11),
// as reconciled after the four-lens review the same day.
//
// The SERVER decides the billing mode. The matrix below is the one the design
// promises: eligible / not × PILOT (named allowlist) / PUBLIC (rolling open to
// everyone) × client build old / new / unknown × body with, without, or
// explicitly fixed billing_mode × RECURRING_ONLY on / off × the listing's
// period. Two rules the review added are pinned here:
//
//   - Refusals bite only under a named pilot or RECURRING_ONLY. With rolling
//     open to everyone and RECURRING_ONLY off, an eligible driver on an old
//     build gets the historical fixed-term lease (WARN-logged) — that is what
//     keeps the deploy dark for the public.
//
//   - Under a named pilot the OWNER must be in it too, or the request falls
//     to fixed-term rather than stranding an owner at the terms gate.
//
//     TEST_DATABASE_URL="postgres://…/scratch?sslmode=disable" \
//     go test ./internal/handlers/ -run RecurringOnly -v
func recurringRequest(t *testing.T, driverID, carID uuid.UUID, body, userAgent string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/listings/%s/lease-requests", carID), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("listingId", carID.String())
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, driverID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func intentRequest(t *testing.T, driverID, leaseID uuid.UUID, userAgent string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/lease-requests/%s/payments/intent", leaseID), strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", leaseID.String())
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, driverID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

type createdLease struct {
	LeaseRequest struct {
		ID                  uuid.UUID `json:"id"`
		BillingMode         string    `json:"billing_mode"`
		BillingInterval     string    `json:"billing_interval"`
		TotalAmount         float64   `json:"total_amount"`
		IntervalAmountCents int64     `json:"interval_amount_cents"`
	} `json:"lease_request"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeCreated(t *testing.T, rr *httptest.ResponseRecorder) createdLease {
	t.Helper()
	var out createdLease
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return out
}

func cleanupLeases(t *testing.T, e *lifecycleEnv, driverID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM lease_requests WHERE driver_id=$1`, driverID)
		_, _ = e.db.Pool.Exec(ctx, `DELETE FROM chats WHERE driver_id=$1`, driverID)
	})
}

// cancelActive frees the one-active-request-per-listing slot between rows.
func cancelActive(t *testing.T, e *lifecycleEnv, driverID uuid.UUID) {
	t.Helper()
	_, _ = e.db.Pool.Exec(context.Background(),
		`UPDATE lease_requests SET status='cancelled' WHERE driver_id=$1 AND status IN ('requested','accepted')`, driverID)
}

const (
	newUA = "DriveBai/41 CFNetwork/3860.500.112 Darwin/25.4.0"
	oldUA = "DriveBai/33 CFNetwork/3860.500.112 Darwin/25.4.0"
	noUA  = ""
)

type eligibility int

const (
	pilotBoth   eligibility = iota // allowlist names driver AND owner
	pilotDriver                    // allowlist names the driver only
	public                         // allowlist empty: everyone eligible
	notEligible                    // allowlist names someone else
)

func TestRecurringOnlyCreationMatrix(t *testing.T) {
	cases := []struct {
		name          string
		elig          eligibility
		recurringOnly bool
		ua            string
		body          string
		wantStatus    int
		wantMode      string
		wantCode      string
		msgHas        string
	}{
		// ---- named pilot, both parties listed: recurring-only bites here
		{"pilot: new build, no field", pilotBoth, false, newUA, `{"weeks":1}`, 201, "rolling", "", ""},
		{"pilot: new build, rolling field", pilotBoth, false, newUA, `{"weeks":1,"billing_mode":"rolling"}`, 201, "rolling", "", ""},
		{"pilot: unknown client at REQUEST time is allowed (pay step gates it)", pilotBoth, false, noUA, `{"weeks":1}`, 201, "rolling", "", ""},
		{"pilot: explicit fixed_term → update required", pilotBoth, false, newUA, `{"weeks":1,"billing_mode":"fixed_term"}`, 409, "", "APP_UPDATE_REQUIRED", "nothing was charged"},
		{"pilot: OLD build → update required, never a silent fixed-term", pilotBoth, false, oldUA, `{"weeks":1}`, 409, "", "APP_UPDATE_REQUIRED", "TestFlight"},
		// ---- named pilot, owner NOT listed: fall to fixed-term, never strand the owner
		{"pilot, owner outside it: new build → fixed-term", pilotDriver, false, newUA, `{"weeks":1}`, 201, "fixed_term", "", ""},
		// ---- public rollout (allowlist empty), RECURRING_ONLY off: DARK for old builds
		{"public: new build, no field → recurring", public, false, newUA, `{"weeks":1}`, 201, "rolling", "", ""},
		{"public: OLD build, no field → historical fixed-term (dark)", public, false, oldUA, `{"weeks":1}`, 201, "fixed_term", "", ""},
		{"public: explicit fixed_term → historical fixed-term (dark)", public, false, newUA, `{"weeks":1,"billing_mode":"fixed_term"}`, 201, "fixed_term", "", ""},
		// ---- public rollout with RECURRING_ONLY on: refusals bite for everyone
		{"public + RECURRING_ONLY: OLD build → update required", public, true, oldUA, `{"weeks":1}`, 409, "", "APP_UPDATE_REQUIRED", "nothing was charged"},
		{"public + RECURRING_ONLY: new build → recurring", public, true, newUA, `{"weeks":1}`, 201, "rolling", "", ""},
		// ---- not eligible: SERVICING fixed-term path, byte for byte
		{"not eligible: no field → fixed-term as before", notEligible, false, oldUA, `{"weeks":1}`, 201, "fixed_term", "", ""},
		{"not eligible: new build, weeks=2 → fixed-term as before", notEligible, false, newUA, `{"weeks":2}`, 201, "fixed_term", "", ""},
		{"not eligible: rolling field → refused as before", notEligible, false, newUA, `{"weeks":1,"billing_mode":"rolling"}`, 503, "", "ROLLING_DISABLED", ""},
		{"not eligible + RECURRING_ONLY → paused, NOT 'update' (they may be on the latest build)", notEligible, true, newUA, `{"weeks":1}`, 409, "", "RENTALS_PAUSED", "paused"},
	}

	e := newLifecycleEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	ownerID := e.seedUser(t, "car_owner", "ro_owner_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "ro_driver_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driverID)
	cleanupLeases(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cancelActive(t, e, driverID)
			switch tc.elig {
			case pilotBoth:
				e.leaseH.SetRollingAllowlist([]uuid.UUID{driverID, ownerID})
			case pilotDriver:
				e.leaseH.SetRollingAllowlist([]uuid.UUID{driverID})
			case public:
				e.leaseH.SetRollingAllowlist(nil)
			case notEligible:
				e.leaseH.SetRollingAllowlist([]uuid.UUID{uuid.New()})
			}
			e.leaseH.SetRecurringOnly(tc.recurringOnly)

			rr := httptest.NewRecorder()
			e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, carID, tc.body, tc.ua))
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
			got := decodeCreated(t, rr)
			if tc.wantStatus == http.StatusCreated {
				if got.LeaseRequest.BillingMode != tc.wantMode {
					t.Fatalf("billing_mode = %q, want %q", got.LeaseRequest.BillingMode, tc.wantMode)
				}
				if tc.wantMode == "rolling" && got.LeaseRequest.BillingInterval != "weekly" {
					t.Errorf("billing_interval = %q, want weekly for a weekly listing", got.LeaseRequest.BillingInterval)
				}
				return
			}
			if got.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q (%s)", got.Error.Code, tc.wantCode, rr.Body.String())
			}
			// Old builds render ONLY the top-level message and discard details
			// (ios APIClient.swift:1439-1444). It must be readable and TRUE.
			if tc.msgHas != "" && !strings.Contains(got.Error.Message, tc.msgHas) {
				t.Errorf("message %q does not say %q", got.Error.Message, tc.msgHas)
			}
			if got.Error.Code == "RENTALS_PAUSED" && strings.Contains(strings.ToLower(got.Error.Message), "update") {
				t.Errorf("paused message tells a latest-build driver to update: %q", got.Error.Message)
			}
		})
	}
	e.leaseH.SetRecurringOnly(false)
}

// A malformed body is refused, not silently emptied into a fixed-term lease;
// an EMPTY body (the historical wire shape) is still accepted.
func TestRecurringOnlyMalformedBodyIsRefused(t *testing.T) {
	e := newLifecycleEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetRollingAllowlist([]uuid.UUID{uuid.New()})

	ownerID := e.seedUser(t, "car_owner", "ro_mb_o_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "ro_mb_d_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driverID)
	cleanupLeases(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, carID, `{"weeks":"one"`, ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, carID, ``, ""))
	if rr.Code != http.StatusCreated {
		t.Fatalf("empty body = %d, want 201 (%s)", rr.Code, rr.Body.String())
	}
}

// The interval comes from the LISTING. Monthly is built but gated OFF by
// default; daily is never a recurring interval. A non-eligible driver keeps
// the fixed-term path on both.
func TestRecurringOnlyIntervalFromListing(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetRollingAllowlist(nil)

	ownerID := e.seedUser(t, "car_owner", "ro_iv_o_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "ro_iv_d_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driverID)
	cleanupLeases(t, e, driverID)

	monthly := e.seedCar(t, ownerID, "available", true, false)
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE cars SET rent_price_period='monthly', rent_price_amount=600, weekly_rent_price=150 WHERE id=$1`, monthly); err != nil {
		t.Fatalf("make listing monthly: %v", err)
	}

	// Flag OFF (the shipped default): refused with a plain message.
	e.leaseH.SetMonthlyEnabled(false)
	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, monthly, `{"weeks":1}`, newUA))
	if rr.Code != http.StatusConflict || decodeCreated(t, rr).Error.Code != "INTERVAL_NOT_SUPPORTED" {
		t.Fatalf("monthly with the flag off = %d %s, want 409 INTERVAL_NOT_SUPPORTED", rr.Code, rr.Body.String())
	}

	// Flag ON: a monthly recurring lease, interval stored, one cycle = $600.
	e.leaseH.SetMonthlyEnabled(true)
	rr = httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, monthly, `{"weeks":1}`, newUA))
	if rr.Code != http.StatusCreated {
		t.Fatalf("monthly with the flag on: %d %s", rr.Code, rr.Body.String())
	}
	got := decodeCreated(t, rr)
	if got.LeaseRequest.BillingMode != "rolling" || got.LeaseRequest.BillingInterval != "monthly" {
		t.Fatalf("monthly → %s/%s, want rolling/monthly", got.LeaseRequest.BillingMode, got.LeaseRequest.BillingInterval)
	}
	if got.LeaseRequest.TotalAmount != 600 || got.LeaseRequest.IntervalAmountCents != 60000 {
		t.Errorf("quote: total_amount=%.2f interval_amount_cents=%d, want 600.00 / 60000 — the card must quote the cycle, not the weekly figure",
			got.LeaseRequest.TotalAmount, got.LeaseRequest.IntervalAmountCents)
	}
	var stored string
	if err := e.db.Pool.QueryRow(ctx, `SELECT billing_interval FROM lease_requests WHERE id=$1`, got.LeaseRequest.ID).Scan(&stored); err != nil || stored != "monthly" {
		t.Errorf("stored billing_interval = %q (%v), want monthly", stored, err)
	}
	e.leaseH.SetMonthlyEnabled(false)

	// Daily: refused for an eligible driver; fixed-term for a non-eligible one.
	cancelActive(t, e, driverID)
	daily := e.seedCar(t, ownerID, "available", true, false)
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE cars SET rent_price_period='daily', rent_price_amount=25, weekly_rent_price=175 WHERE id=$1`, daily); err != nil {
		t.Fatalf("make listing daily: %v", err)
	}
	rr = httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, daily, `{"weeks":1}`, newUA))
	if rr.Code != http.StatusConflict || decodeCreated(t, rr).Error.Code != "INTERVAL_NOT_SUPPORTED" {
		t.Fatalf("daily for an eligible driver = %d %s", rr.Code, rr.Body.String())
	}
	cancelActive(t, e, driverID)
	e.leaseH.SetRollingAllowlist([]uuid.UUID{uuid.New()})
	rr = httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, daily, `{"weeks":1}`, oldUA))
	if rr.Code != http.StatusCreated || decodeCreated(t, rr).LeaseRequest.BillingMode != "fixed_term" {
		t.Fatalf("non-eligible on daily = %d %s, want 201 fixed_term", rr.Code, rr.Body.String())
	}
}

// The pay step is where the consent sheet matters and money moves. A rolling
// lease refuses an intent from a client that cannot show the sheet — an OLD
// build or an UNKNOWN one — before any Stripe call.
func TestRecurringOnlyPayStepRefusesUnprovenClients(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	e.leaseH.SetRollingAllowlist(nil)

	ownerID := e.seedUser(t, "car_owner", "ro_pay_o_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "ro_pay_d_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driverID)
	cleanupLeases(t, e, driverID)
	carID := e.seedCar(t, ownerID, "available", true, false)

	rr := httptest.NewRecorder()
	e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, carID, `{"weeks":1}`, newUA))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	leaseID := decodeCreated(t, rr).LeaseRequest.ID
	if _, err := e.db.Pool.Exec(ctx, `UPDATE lease_requests SET status='accepted', accepted_at=NOW() WHERE id=$1`, leaseID); err != nil {
		t.Fatalf("accept: %v", err)
	}

	for _, ua := range []string{oldUA, noUA} {
		rr = httptest.NewRecorder()
		e.leaseH.CreatePaymentIntent(rr, intentRequest(t, driverID, leaseID, ua))
		if rr.Code != http.StatusConflict || decodeCreated(t, rr).Error.Code != "APP_UPDATE_REQUIRED" {
			t.Errorf("intent with UA %q = %d %s, want 409 APP_UPDATE_REQUIRED before any Stripe call", ua, rr.Code, rr.Body.String())
		}
	}
	// A proven client gets PAST the gate (it then meets whatever Stripe
	// wiring the env has; the point is that the refusal is not the build).
	rr = httptest.NewRecorder()
	e.leaseH.CreatePaymentIntent(rr, intentRequest(t, driverID, leaseID, newUA))
	if code := decodeCreated(t, rr).Error.Code; code == "APP_UPDATE_REQUIRED" {
		t.Errorf("a build-41 client was refused at the pay step: %s", rr.Body.String())
	}
}

// The owner's "payment received" line, from the build-41 run (2026-09-18).
// The live run showed an owner being told "Driver T paid for 1 week(s)" on a
// lease that renews weekly forever — fixed-term wording on a recurring
// rental, the same shape that made 2026-09-14 invisible.
func TestOwnerPaidBodyNamesTheCadence(t *testing.T) {
	weekly := &models.LeaseRequest{
		BillingMode: models.BillingModeRolling, BillingInterval: "weekly",
		WeeklyPrice: 150, Weeks: 1, Currency: "USD",
	}
	monthly := &models.LeaseRequest{
		BillingMode: models.BillingModeRolling, BillingInterval: "monthly",
		WeeklyPrice: 150, Weeks: 1, Currency: "USD",
	}
	fixed := &models.LeaseRequest{
		BillingMode: models.BillingModeFixedTerm, BillingInterval: "weekly",
		WeeklyPrice: 150, Weeks: 2, Currency: "USD",
	}

	got := ownerPaidBody("Driver T", "2021 Honda CR-V", weekly)
	for _, want := range []string{"first week", "150.00 per week", "renewing until they return it"} {
		if !strings.Contains(got, want) {
			t.Errorf("weekly owner line missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "week(s)") {
		t.Errorf("weekly recurring lease still uses fixed-term wording: %s", got)
	}

	got = ownerPaidBody("Driver T", "2022 Toyota Camry", monthly)
	for _, want := range []string{"first month", "600.00 per month"} {
		if !strings.Contains(got, want) {
			t.Errorf("monthly owner line missing %q: %s", want, got)
		}
	}

	// Fixed-term keeps the historical sentence byte for byte.
	if got := ownerPaidBody("Driver T", "2021 Honda CR-V", fixed); got != "Driver T paid for 2 week(s) of 2021 Honda CR-V — coordinate pickup in chat" {
		t.Errorf("fixed-term wording changed: %s", got)
	}
}
