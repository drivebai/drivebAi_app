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
