package models

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// makeTestCarWithVIN builds a minimal Car suitable for ToResponse marshaling
// tests. Only the fields ToResponse reads are set — anything else stays at
// the zero value, which is fine for a wire-shape assertion.
func makeTestCarWithVIN(t *testing.T, vin string) *Car {
	t.Helper()
	return &Car{
		ID:                uuid.New(),
		OwnerID:           uuid.New(),
		Title:             "2019 Toyota Corolla",
		Make:              "Toyota",
		Model:             "Corolla",
		Year:              2019,
		BodyType:          BodyTypeSedan,
		FuelType:          FuelTypeGas,
		Mileage:           10000,
		IsForRent:         true,
		Currency:          "USD",
		MinYearsLicensed:  2,
		DepositAmount:     500,
		InsuranceCoverage: InsuranceFullCoverage,
		Status:            CarStatusAvailable,
		VIN:               sql.NullString{String: vin, Valid: true},
	}
}

// TestToResponse_HidesVINForPublicViewer pins the security boundary: when a
// CarHandler builds a response for a public surface (Discovery,
// ListAvailableListings) it must pass includeVIN=false, and the resulting
// JSON must NOT contain the VIN — neither as `specs.vin` nor anywhere else
// (defence against a future struct change accidentally re-surfacing it).
//
// VINs are sensitive: a (VIN, make, model) tuple is enough to pull title /
// accident history and can be used in identity-grade vehicle lookups. The
// public listings surface has no UX need for a VIN, so it must be absent.
func TestToResponse_HidesVINForPublicViewer(t *testing.T) {
	const secretVIN = "1HGCM82633A004352"
	car := makeTestCarWithVIN(t, secretVIN)

	resp := car.ToResponse(nil, nil, nil, false)

	if resp.Specs.VIN != nil {
		t.Fatalf("ToResponse(includeVIN=false) must leave Specs.VIN nil; got %q", *resp.Specs.VIN)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretVIN) {
		t.Fatalf("VIN %q leaked into public response JSON: %s", secretVIN, raw)
	}
	// Also confirm the JSON key itself doesn't appear — the omitempty tag
	// should suppress it entirely when the pointer is nil.
	if strings.Contains(string(raw), `"vin"`) {
		t.Fatalf("public response JSON should not include a \"vin\" field at all, got: %s", raw)
	}
}

// TestToResponse_ExposesVINForOwnerViewer pins the other half of the
// contract: owner-side handlers (CarHandler.GetCar, ListCars, …) build
// responses with includeVIN=true and the VIN must round-trip into the JSON
// under specs.vin so the iOS owner-side UI can show / re-edit it.
func TestToResponse_ExposesVINForOwnerViewer(t *testing.T) {
	const ownerVIN = "1HGCM82633A004352"
	car := makeTestCarWithVIN(t, ownerVIN)

	resp := car.ToResponse(nil, nil, nil, true)

	if resp.Specs.VIN == nil {
		t.Fatal("ToResponse(includeVIN=true) must set Specs.VIN")
	}
	if got := *resp.Specs.VIN; got != ownerVIN {
		t.Fatalf("Specs.VIN = %q, want %q", got, ownerVIN)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), ownerVIN) {
		t.Fatalf("owner response JSON missing VIN; got: %s", raw)
	}
}

// TestToResponse_EmptyVINNeverSurfaces guards against the regression where
// a car with no VIN (legacy row, owner skipped the autofill step) would
// emit `"vin": ""` to public surfaces. Even with includeVIN=true, an
// empty/invalid VIN must stay absent from the JSON.
func TestToResponse_EmptyVINNeverSurfaces(t *testing.T) {
	car := makeTestCarWithVIN(t, "")
	car.VIN = sql.NullString{} // invalid + empty
	for _, include := range []bool{false, true} {
		resp := car.ToResponse(nil, nil, nil, include)
		if resp.Specs.VIN != nil {
			t.Fatalf("includeVIN=%v with empty stored VIN should keep Specs.VIN nil, got %q",
				include, *resp.Specs.VIN)
		}
		raw, _ := json.Marshal(resp)
		if strings.Contains(string(raw), `"vin"`) {
			t.Fatalf("includeVIN=%v with empty stored VIN must omit the \"vin\" key; got: %s",
				include, raw)
		}
	}
}
