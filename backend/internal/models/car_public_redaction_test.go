package models

import (
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Behavioral tests for the guest-mode redaction: build a real Car, run it
// through ToResponse (the authenticated shape) and RedactForPublic (the
// anonymous shape), marshal to JSON, and assert on the wire bytes. No
// source-text scanning — these fail on behavior, not phrasing.

var redactTestSecret = []byte("test-secret")

func fullTestCar(t *testing.T) (*Car, *User) {
	t.Helper()
	lat, lng := 40.74117168, -73.82251554
	car := &Car{
		ID:           uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		OwnerID:      uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		Title:        "2015 Nissan Sentra",
		Make:         "Nissan",
		Model:        "Sentra",
		Year:         2015,
		Mileage:      10000,
		VIN:          sql.NullString{String: "1N4AL3AP4FC123456", Valid: true},
		Address:      sql.NullString{String: "County Road 3100 3033, Independence, 67301", Valid: true},
		Street:       sql.NullString{String: "County Road 3100 3033", Valid: true},
		Block:        sql.NullString{String: "4B", Valid: true},
		Zip:          sql.NullString{String: "67301", Valid: true},
		Neighborhood: sql.NullString{String: "Independence", Valid: true},
		Area:         sql.NullString{String: "Independence", Valid: true},
		Latitude:     sql.NullFloat64{Float64: lat, Valid: true},
		Longitude:    sql.NullFloat64{Float64: lng, Valid: true},
		IsForRent:    true,
		TotalEarned:  4200.50,
		RentedWeeks:  12,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	photoURL := "/uploads/22222222-2222-2222-2222-222222222222/profile_x.jpg"
	owner := &User{
		ID:              car.OwnerID,
		FirstName:       "Alice",
		LastName:        "Smithers",
		ProfilePhotoURL: &photoURL,
	}
	return car, owner
}

// TestToResponse_AuthenticatedShapeUnchanged pins the signed-in contract:
// exact coordinates, full address, owner full name + photo, earnings, and
// precision "exact" are all present.
func TestToResponse_AuthenticatedShapeUnchanged(t *testing.T) {
	car, owner := fullTestCar(t)
	resp := car.ToResponse(nil, nil, owner, false)

	if resp.Location.Precision != LocationPrecisionExact {
		t.Errorf("precision = %q, want exact", resp.Location.Precision)
	}
	if resp.Location.Latitude == nil || *resp.Location.Latitude != 40.74117168 {
		t.Error("authenticated latitude must be the exact value")
	}
	if resp.Location.Street == "" || resp.Location.Zip == "" || resp.Location.Address == "" {
		t.Error("authenticated response must carry the full street address")
	}
	if resp.Owner == nil || resp.Owner.Name != "Alice Smithers" {
		t.Errorf("authenticated owner name = %v, want full name", resp.Owner)
	}
	if resp.Owner.ProfilePhotoURL == nil {
		t.Error("authenticated owner photo must be present")
	}
	if resp.TotalEarned != 4200.50 {
		t.Error("authenticated total_earned must be preserved")
	}
}

// TestRedactForPublic_JSONLacksPII is THE guest-mode guarantee: the marshaled
// anonymous JSON must not contain precise coordinates, any street-address
// component, the owner's surname, photo, or UUIDs, the VIN, or earnings.
func TestRedactForPublic_JSONLacksPII(t *testing.T) {
	car, owner := fullTestCar(t)
	resp := car.ToResponse(nil, nil, owner, false)
	resp.RedactForPublic(owner.FirstName, redactTestSecret)

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, banned := range []string{
		"County Road",         // street / address line
		"67301",               // zip
		"\"4B\"",              // block
		"Smithers",            // owner surname
		"profile_x.jpg",       // owner photo URL
		"40.74117168",         // exact latitude
		"-73.82251554",        // exact longitude
		"1N4AL3AP4FC123456",   // VIN (excluded upstream, pinned here too)
		"4200.5",              // owner earnings
		car.OwnerID.String(),  // owner UUID (top-level and nested)
	} {
		if strings.Contains(body, banned) {
			t.Errorf("anonymous JSON must not contain %q\nbody: %s", banned, body)
		}
	}

	// What guests DO keep.
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	loc := decoded["location"].(map[string]any)
	if loc["precision"] != "approximate" {
		t.Errorf("precision = %v, want approximate", loc["precision"])
	}
	if loc["neighborhood"] != "Independence" || loc["area"] != "Independence" {
		t.Error("guest response must keep the area labels")
	}
	if loc["latitude"] == nil || loc["longitude"] == nil {
		t.Error("guest response must keep displaced coordinates for the area circle")
	}
	ownerObj := decoded["owner"].(map[string]any)
	if ownerObj["name"] != "Alice" {
		t.Errorf("guest owner name = %v, want first name only", ownerObj["name"])
	}
	if _, hasPhoto := ownerObj["profile_photo_url"]; hasPhoto {
		t.Error("guest owner photo key must be absent entirely")
	}
	if decoded["rented_weeks"].(float64) != 12 {
		t.Error("rented_weeks (non-identifying) must be kept")
	}
}

// TestRedactForPublic_DisplacementProperties: the published point is never
// the true point (min 300 m), never further than 700 m, stable across calls
// (no averaging attack), and different per car.
func TestRedactForPublic_DisplacementProperties(t *testing.T) {
	car, owner := fullTestCar(t)
	trueLat, trueLng := car.Latitude.Float64, car.Longitude.Float64

	metersBetween := func(lat1, lng1, lat2, lng2 float64) float64 {
		dLat := (lat2 - lat1) * 111320
		dLng := (lng2 - lng1) * 111320 * math.Cos(lat1*math.Pi/180)
		return math.Hypot(dLat, dLng)
	}

	a := car.ToResponse(nil, nil, owner, false)
	a.RedactForPublic(owner.FirstName, redactTestSecret)
	b := car.ToResponse(nil, nil, owner, false)
	b.RedactForPublic(owner.FirstName, redactTestSecret)

	if *a.Location.Latitude != *b.Location.Latitude || *a.Location.Longitude != *b.Location.Longitude {
		t.Fatal("displacement must be deterministic per car — jitter can be averaged away")
	}

	d := metersBetween(trueLat, trueLng, *a.Location.Latitude, *a.Location.Longitude)
	// 4-decimal rounding moves the point by up to ~16 m; allow that slack.
	if d < 280 || d > 720 {
		t.Errorf("displacement = %.0f m, want within [300,700] (±rounding)", d)
	}

	// A different car must displace differently (fixed UUIDs → deterministic).
	car2, _ := fullTestCar(t)
	car2.ID = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	c := car2.ToResponse(nil, nil, owner, false)
	c.RedactForPublic(owner.FirstName, redactTestSecret)
	if *c.Location.Latitude == *a.Location.Latitude && *c.Location.Longitude == *a.Location.Longitude {
		t.Error("different cars must not share a displacement vector")
	}
}

// TestRedactForPublic_EmptyFirstName: a user row with an empty first name
// must yield an empty guest label — never a surname fallback.
func TestRedactForPublic_EmptyFirstName(t *testing.T) {
	car, owner := fullTestCar(t)
	owner.FirstName = ""
	resp := car.ToResponse(nil, nil, owner, false)
	resp.RedactForPublic(owner.FirstName, redactTestSecret)
	if resp.Owner.Name != "" {
		t.Errorf("guest owner name = %q, want empty (client renders a generic label)", resp.Owner.Name)
	}
	if strings.Contains(resp.Owner.Name, "Smithers") {
		t.Error("surname must never leak through the empty-first-name edge")
	}
}

// TestRedactForPublic_NoCoordinates: cars without coordinates redact cleanly
// (no panic, no invented location).
func TestRedactForPublic_NoCoordinates(t *testing.T) {
	car, owner := fullTestCar(t)
	car.Latitude = sql.NullFloat64{}
	car.Longitude = sql.NullFloat64{}
	resp := car.ToResponse(nil, nil, owner, false)
	resp.RedactForPublic(owner.FirstName, redactTestSecret)
	if resp.Location.Latitude != nil || resp.Location.Longitude != nil {
		t.Error("a car without coordinates must not gain any")
	}
	if resp.Location.Precision != LocationPrecisionApproximate {
		t.Error("anonymous responses declare approximate precision regardless")
	}
}
