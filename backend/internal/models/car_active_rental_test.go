package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Pins the wire shape of the new active_rental sub-object attached to
// CarResponse. The iOS owner-side My Cars grid keys off these exact JSON
// field names to render the "Rented to Jamie R. · 4 weeks · $180/wk" line
// and the derived Rented/Reserved status chip. A drift here would silently
// break the client without any Swift-side compile error.
func TestCarResponse_ActiveRental_WireShape(t *testing.T) {
	pickup := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	end := pickup.AddDate(0, 0, 4*7)

	resp := &CarResponse{
		ID:      uuid.New(),
		OwnerID: uuid.New(),
		Title:   "2019 Toyota Corolla",
		ActiveRental: &ActiveRentalSummary{
			LeaseRequestID:     uuid.New(),
			DriverID:           uuid.New(),
			DriverName:         "Jamie Rivera",
			Weeks:              4,
			WeeklyPriceCents:   18000,
			PickupConfirmedAt:  RFC3339Time(pickup),
			PlannedEndAt:       RFC3339Time(end),
			CurrentEarnedCents: 36000,
		},
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	// Every field the client reads must appear under its exact JSON name.
	for _, key := range []string{
		`"active_rental"`,
		`"lease_request_id"`,
		`"driver_id"`,
		`"driver_name":"Jamie Rivera"`,
		`"weeks":4`,
		`"weekly_price_cents":18000`,
		`"pickup_confirmed_at"`,
		`"planned_end_at"`,
		`"current_earned_cents":36000`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("active_rental JSON missing %s; got: %s", key, raw)
		}
	}
}

// A car with no active rental (nothing paid + picked-up + not returned)
// must OMIT the active_rental key entirely — not emit it as null. The
// omitempty tag on the pointer field is what keeps Discovery + admin
// surfaces free of rental metadata that doesn't belong to them.
func TestCarResponse_ActiveRental_OmittedWhenNil(t *testing.T) {
	resp := &CarResponse{
		ID:      uuid.New(),
		OwnerID: uuid.New(),
		Title:   "2019 Toyota Corolla",
		// ActiveRental deliberately left nil.
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"active_rental"`) {
		t.Fatalf("CarResponse with nil ActiveRental should omit the key entirely; got: %s", raw)
	}
}
