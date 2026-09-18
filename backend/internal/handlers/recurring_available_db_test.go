package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/urlsigner"
)

// The lifecycle env has no car handler; build the real one over the same DB.
func newTestCarHandler(t *testing.T, e *lifecycleEnv) *CarHandler {
	t.Helper()
	return NewCarHandler(e.carRepo,
		repository.NewCarPhotoRepository(e.db),
		repository.NewCarDocumentRepository(e.db),
		repository.NewUserRepository(e.db),
		t.TempDir(),
		&PrivateURLSigner{Signer: urlsigner.New("recurring-available-test"), TTL: time.Hour},
		50, true)
}

// recurring_available is the SERVER's per-viewer, per-listing answer to "will
// a request from you for this car renew?" (review 2026-09-17). It folds in
// the driver's eligibility, the owner's pilot membership, the listing's
// period and the monthly flag — the same predicate CreateLeaseRequest uses —
// so the app can never promise a renewal the server would not create.
func TestRecurringAvailableForMatchesCreation(t *testing.T) {
	e := newLifecycleEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	driver, owner := uuid.New(), uuid.New()

	cases := []struct {
		name      string
		allowlist []uuid.UUID
		monthly   bool
		period    string
		want      bool
	}{
		{"public, weekly", nil, false, "weekly", true},
		{"public, legacy blank period", nil, false, "", true},
		{"public, daily", nil, false, "daily", false},
		{"public, monthly, flag off", nil, false, "monthly", false},
		{"public, monthly, flag on", nil, true, "monthly", true},
		{"pilot: both listed, weekly", []uuid.UUID{driver, owner}, false, "weekly", true},
		{"pilot: driver only → owner would be stranded → false", []uuid.UUID{driver}, false, "weekly", false},
		{"pilot: owner only → driver not eligible → false", []uuid.UUID{owner}, false, "weekly", false},
		{"not eligible", []uuid.UUID{uuid.New()}, false, "weekly", false},
	}
	for _, c := range cases {
		e.leaseH.SetRollingAllowlist(c.allowlist)
		e.leaseH.SetMonthlyEnabled(c.monthly)
		if got := e.leaseH.RecurringAvailableFor(driver, owner, c.period); got != c.want {
			t.Errorf("%s: RecurringAvailableFor = %v, want %v", c.name, got, c.want)
		}
	}
	e.leaseH.SetMonthlyEnabled(false)
	e.leaseH.SetRollingAllowlist(nil)
}

// End to end: the flag rides on the GET /listings item the driver opens and
// flips with the owner's pilot membership.
func TestListingDetailCarriesRecurringAvailable(t *testing.T) {
	e := newLifecycleEnv(t)
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	carH := newTestCarHandler(t, e)
	carH.SetRecurringAvailability(e.leaseH.RecurringAvailableFor)
	carH.SetRentRefusal(e.leaseH.RentRefusalFor)

	ownerID := e.seedUser(t, "car_owner", "ra_o_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "ra_d_"+uuid.NewString()[:8]+"@example.com")
	carID := e.seedCar(t, ownerID, "available", true, false)

	// GET /cars/{id} is owner-only; a DRIVER's listing detail is the item
	// from GET /listings, so that is where the flag must ride.
	get := func() bool {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/listings?status=available", nil)
		ctx := context.WithValue(req.Context(), httputil.UserIDKey, driverID)
		rr := httptest.NewRecorder()
		carH.ListAvailableListings(rr, req.WithContext(ctx))
		if rr.Code != http.StatusOK {
			t.Fatalf("GET listings: %d %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Listings []struct {
				ID                 uuid.UUID `json:"id"`
				RecurringAvailable bool      `json:"recurring_available"`
			} `json:"listings"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, l := range out.Listings {
			if l.ID == carID {
				return l.RecurringAvailable
			}
		}
		t.Fatalf("car %s not in the driver's listings", carID)
		return false
	}

	e.leaseH.SetRollingAllowlist([]uuid.UUID{driverID, ownerID})
	if !get() {
		t.Error("pilot pair: recurring_available should be true")
	}
	e.leaseH.SetRollingAllowlist([]uuid.UUID{driverID})
	if get() {
		t.Error("owner outside the pilot: recurring_available should be false — the CTA must not promise a renewal the server will not create")
	}
	e.leaseH.SetRollingAllowlist(nil)
	if !get() {
		t.Error("public rollout: recurring_available should be true")
	}
}

// A listing this viewer cannot rent at all must say so on the listing, and
// the 409 they would get must use the SAME words — otherwise the app offers
// a button whose only outcome is a refusal (review 2026-09-18, §1a).
func TestRentRefusalMatchesTheRefusalCopy(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)

	ownerID := e.seedUser(t, "car_owner", "rr_o_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "rr_d_"+uuid.NewString()[:8]+"@example.com")
	e.seedLicense(t, driverID)
	cleanupLeases(t, e, driverID)

	for _, tc := range []struct {
		period    string
		monthly   bool
		eligible  bool
		wantNotic bool
	}{
		{"weekly", false, true, false},   // rentable, recurring
		{"monthly", false, true, true},   // eligible + monthly off → refused outright
		{"monthly", true, true, false},   // flag on → rentable
		{"daily", false, true, true},     // eligible + daily → never recurring
		{"daily", false, false, false},   // NOT eligible → fixed-term still works
		{"monthly", false, false, false}, // NOT eligible → fixed-term still works
	} {
		carID := e.seedCar(t, ownerID, "available", true, false)
		if _, err := e.db.Pool.Exec(ctx,
			`UPDATE cars SET rent_price_period=$2, rent_price_amount=200, weekly_rent_price=150 WHERE id=$1`, carID, tc.period); err != nil {
			t.Fatalf("set period: %v", err)
		}
		if tc.eligible {
			e.leaseH.SetRollingAllowlist(nil)
		} else {
			e.leaseH.SetRollingAllowlist([]uuid.UUID{uuid.New()})
		}
		e.leaseH.SetMonthlyEnabled(tc.monthly)

		reason := e.leaseH.RentRefusalFor(driverID, ownerID, tc.period)
		if (reason != "") != tc.wantNotic {
			t.Errorf("%s/monthly=%v/eligible=%v: reason=%q, want notice=%v", tc.period, tc.monthly, tc.eligible, reason, tc.wantNotic)
			continue
		}
		// Whatever the listing says, the request must say the same thing.
		cancelActive(t, e, driverID)
		rr := httptest.NewRecorder()
		e.leaseH.CreateLeaseRequest(rr, recurringRequest(t, driverID, carID, `{"weeks":1}`, newUA))
		got := decodeCreated(t, rr)
		if tc.wantNotic {
			if rr.Code != http.StatusConflict || got.Error.Code != "INTERVAL_NOT_SUPPORTED" {
				t.Errorf("%s: request = %d %s, want 409 INTERVAL_NOT_SUPPORTED", tc.period, rr.Code, rr.Body.String())
			} else if got.Error.Message != reason {
				t.Errorf("%s: listing says %q, refusal says %q — they must match", tc.period, reason, got.Error.Message)
			}
		} else if rr.Code != http.StatusCreated {
			t.Errorf("%s/monthly=%v/eligible=%v: request = %d %s, want 201", tc.period, tc.monthly, tc.eligible, rr.Code, rr.Body.String())
		}
	}
	e.leaseH.SetMonthlyEnabled(false)
	e.leaseH.SetRollingAllowlist(nil)
}

// The refusal must reach the WIRE, not just the predicate: without
// SetRentRefusal wired in main.go the field is silently absent and the app
// shows a button again (review 2026-09-18, finding 5).
func TestRentUnavailableReasonReachesTheListingJSON(t *testing.T) {
	e := newLifecycleEnv(t)
	ctx := context.Background()
	billingRepo := repository.NewBillingRepository(e.db)
	e.leaseH.SetBillingDependencies(billingRepo, payoutTestFeeBPS, true)
	carH := newTestCarHandler(t, e)
	carH.SetRecurringAvailability(e.leaseH.RecurringAvailableFor)
	carH.SetRentRefusal(e.leaseH.RentRefusalFor)

	ownerID := e.seedUser(t, "car_owner", "rw_o_"+uuid.NewString()[:8]+"@example.com")
	driverID := e.seedUser(t, "driver", "rw_d_"+uuid.NewString()[:8]+"@example.com")
	carID := e.seedCar(t, ownerID, "available", true, false)
	if _, err := e.db.Pool.Exec(ctx,
		`UPDATE cars SET rent_price_period='monthly', rent_price_amount=600, weekly_rent_price=150 WHERE id=$1`, carID); err != nil {
		t.Fatalf("make monthly: %v", err)
	}
	e.leaseH.SetRollingAllowlist(nil) // eligible
	e.leaseH.SetMonthlyEnabled(false) // ...but monthly is off → refused outright

	read := func(viewer uuid.UUID) (string, bool) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/listings?status=available", nil)
		ctxv := context.WithValue(req.Context(), httputil.UserIDKey, viewer)
		rr := httptest.NewRecorder()
		carH.ListAvailableListings(rr, req.WithContext(ctxv))
		if rr.Code != http.StatusOK {
			t.Fatalf("listings: %d %s", rr.Code, rr.Body.String())
		}
		var out struct {
			Listings []struct {
				ID                    uuid.UUID `json:"id"`
				RentUnavailableReason string    `json:"rent_unavailable_reason"`
			} `json:"listings"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, l := range out.Listings {
			if l.ID == carID {
				return l.RentUnavailableReason, true
			}
		}
		return "", false
	}

	reason, found := read(driverID)
	if !found {
		t.Fatal("seeded car missing from listings")
	}
	if reason == "" {
		t.Fatal("rent_unavailable_reason absent from the listing JSON — the app would show a button that can only 409")
	}
	if want := e.leaseH.RentRefusalFor(driverID, ownerID, "monthly"); reason != want {
		t.Errorf("wire says %q, predicate says %q", reason, want)
	}
	// A driver who is NOT eligible can rent it fixed-term: no notice.
	e.leaseH.SetRollingAllowlist([]uuid.UUID{uuid.New()})
	if r2, _ := read(driverID); r2 != "" {
		t.Errorf("non-eligible viewer got a refusal notice %q — fixed-term still works for them", r2)
	}
	e.leaseH.SetRollingAllowlist(nil)
}
