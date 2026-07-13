package models

import "testing"

// ── Inspection checklist (DESIGN SPEC item 22, SAFETY CRITICAL) ──────────────

// fullyConfirmedChecklist returns a checklist with every gate affirmatively
// true — the only shape that may unlock capture.
func fullyConfirmedChecklist() InspectVehicleAcceptBody {
	return InspectVehicleAcceptBody{
		VINMatches:            true,
		OdometerReviewed:      true,
		ExteriorOK:            true,
		InteriorOK:            true,
		MechanicalTestDriveOK: true,
		TitleReviewed:         true,
		KeysHandedOver:        true,
		BuyerUnderstandsAcceptanceCompletesPayment: true,
	}
}

// TestInspectVehicleAcceptBody_AllConfirmed: AllConfirmed is true ONLY when
// every field is true. Any single false field — including the zero value —
// blocks acceptance, which is what keeps the pre-capture gate safety-critical.
func TestInspectVehicleAcceptBody_AllConfirmed(t *testing.T) {
	if !fullyConfirmedChecklist().AllConfirmed() {
		t.Fatal("an all-true checklist must be AllConfirmed")
	}
	if (InspectVehicleAcceptBody{}).AllConfirmed() {
		t.Fatal("the zero-value (all-false) checklist must NOT be confirmed")
	}

	// Flip each field to false in isolation; every one must independently
	// block. If a field were dropped from AllConfirmed this loop catches it.
	flips := []struct {
		name string
		set  func(*InspectVehicleAcceptBody)
	}{
		{"vin_matches", func(b *InspectVehicleAcceptBody) { b.VINMatches = false }},
		{"odometer_reviewed", func(b *InspectVehicleAcceptBody) { b.OdometerReviewed = false }},
		{"exterior_ok", func(b *InspectVehicleAcceptBody) { b.ExteriorOK = false }},
		{"interior_ok", func(b *InspectVehicleAcceptBody) { b.InteriorOK = false }},
		{"mechanical_test_drive_ok", func(b *InspectVehicleAcceptBody) { b.MechanicalTestDriveOK = false }},
		{"title_reviewed", func(b *InspectVehicleAcceptBody) { b.TitleReviewed = false }},
		{"keys_handed_over", func(b *InspectVehicleAcceptBody) { b.KeysHandedOver = false }},
		{"buyer_understands_acceptance_completes_payment", func(b *InspectVehicleAcceptBody) {
			b.BuyerUnderstandsAcceptanceCompletesPayment = false
		}},
	}
	if len(flips) != 8 {
		t.Fatalf("expected 8 checklist fields under test, got %d", len(flips))
	}
	for _, f := range flips {
		b := fullyConfirmedChecklist()
		f.set(&b)
		if b.AllConfirmed() {
			t.Errorf("a false %q must block AllConfirmed", f.name)
		}
	}
}

// ── Title condition enum (DESIGN SPEC item 20) ───────────────────────────────

// TestTitleCondition_IsValid: only the eight known brands are valid; anything
// else (empty, wrong case, trailing space, unknown) is rejected. The string
// values are pinned to the DB CHECK constraint in migration 000036.
func TestTitleCondition_IsValid(t *testing.T) {
	valid := map[TitleCondition]string{
		TitleConditionClean:               "clean",
		TitleConditionLienRecorded:        "lien_recorded",
		TitleConditionSalvage:             "salvage",
		TitleConditionRebuilt:             "rebuilt",
		TitleConditionLemonBuyback:        "lemon_buyback",
		TitleConditionFlood:               "flood",
		TitleConditionManufacturerBuyback: "manufacturer_buyback",
		TitleConditionOther:               "other",
	}
	if len(valid) != 8 {
		t.Fatalf("expected exactly 8 title-condition brands, got %d", len(valid))
	}
	for tc, wantStr := range valid {
		if !tc.IsValid() {
			t.Errorf("%q must be a valid title condition", tc)
		}
		// Pin the wire/DB string so a rename can't silently diverge from the
		// migration 000036 CHECK list.
		if string(tc) != wantStr {
			t.Errorf("title condition constant = %q, want DB value %q", tc, wantStr)
		}
	}

	for _, bad := range []TitleCondition{
		"", "CLEAN", "clean ", " clean", "unknown", "lien", "other ", "Salvage",
	} {
		if bad.IsValid() {
			t.Errorf("%q must NOT be a valid title condition", bad)
		}
	}
}

// ── New Car-Sale-release error sentinels ─────────────────────────────────────

// TestCarSaleReleaseErrorSentinels asserts each new sentinel carries its
// documented machine code and a non-empty message, and that the codes are
// distinct. iOS and the HTTP-status mapper both key on these exact codes.
func TestCarSaleReleaseErrorSentinels(t *testing.T) {
	cases := []struct {
		err  *APIError
		code string
	}{
		{ErrTitleRequired, ErrCodeTitleRequired},
		{ErrInspectionChecklistIncomplete, ErrCodeInspectionChecklistIncomplete},
		{ErrTitleConditionRequired, ErrCodeTitleConditionRequired},
		{ErrInvalidTitleCondition, ErrCodeInvalidTitleCondition},
		{ErrTitleConditionOtherRequired, ErrCodeTitleConditionOtherRequired},
		{ErrSellerAddressRequired, ErrCodeSellerAddressRequired},
		{ErrBuyerAddressRequired, ErrCodeBuyerAddressRequired},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("nil sentinel for %s", c.code)
			continue
		}
		if c.err.Code != c.code {
			t.Errorf("sentinel code %q, want %q", c.err.Code, c.code)
		}
		if c.err.Message == "" {
			t.Errorf("%s has empty message", c.code)
		}
		if seen[c.code] {
			t.Errorf("duplicate code %q", c.code)
		}
		seen[c.code] = true
	}

	// INVALID_VIN lives on the car model and gates every new listing.
	if ErrCodeInvalidVIN != "INVALID_VIN" {
		t.Errorf("ErrCodeInvalidVIN = %q, want INVALID_VIN", ErrCodeInvalidVIN)
	}
}
