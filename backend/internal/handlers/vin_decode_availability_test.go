package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Tests for the early VIN-availability signal (QA pt-12): the decode
// endpoint reuses ExistsByVIN — injected as a func so these tests run
// without a DB — and must degrade gracefully: a DB error may never fail a
// good NHTSA decode; the `available` field is simply omitted.

const testVIN = "1HGCM82633A004352"

// newVPICStub stands in for the NHTSA vPIC upstream with a clean decode.
func newVPICStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"Results": [{
				"Make": "HONDA",
				"Model": "Accord",
				"ModelYear": "2003",
				"BodyClass": "Sedan/Saloon",
				"FuelTypePrimary": "Gasoline",
				"Manufacturer": "HONDA MFG",
				"VehicleType": "PASSENGER CAR",
				"ErrorCode": "0",
				"ErrorText": ""
			}]
		}`))
	}))
}

// decodeVINViaHandler runs one DecodeVIN request against the stubbed
// upstream with the given ExistsByVIN injection and returns status + the
// decoded JSON body.
func decodeVINViaHandler(t *testing.T, existsByVIN func(context.Context, string) (bool, error)) (int, map[string]interface{}) {
	t.Helper()

	stub := newVPICStub(t)
	defer stub.Close()

	oldUpstream := vinDecodeUpstream
	vinDecodeUpstream = stub.URL
	defer func() { vinDecodeUpstream = oldUpstream }()

	h := NewVINDecodeHandler(slog.New(slog.NewTextHandler(io.Discard, nil)), existsByVIN)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cars/vin-decode/"+testVIN, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("vin", testVIN)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.DecodeVIN(rr, req)

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v — body: %s", err, rr.Body.String())
	}
	return rr.Code, body
}

func TestVINDecode_Available_False_WhenVINInUse(t *testing.T) {
	var checkedVIN string
	status, body := decodeVINViaHandler(t, func(_ context.Context, vin string) (bool, error) {
		checkedVIN = vin
		return true, nil // a non-archived car already holds this VIN
	})

	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if checkedVIN != testVIN {
		t.Fatalf("ExistsByVIN got %q, want normalized %q", checkedVIN, testVIN)
	}
	avail, present := body["available"]
	if !present {
		t.Fatal("available field missing — want available:false")
	}
	if avail != false {
		t.Fatalf("want available:false, got %v", avail)
	}
	// The decode payload itself must be intact.
	if body["make"] != "Honda" {
		t.Fatalf("decode payload damaged: make = %v", body["make"])
	}
}

func TestVINDecode_Available_True_WhenVINFree(t *testing.T) {
	status, body := decodeVINViaHandler(t, func(context.Context, string) (bool, error) {
		return false, nil // no live listing holds this VIN (e.g. only archived)
	})

	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if avail, present := body["available"]; !present || avail != true {
		t.Fatalf("want available:true, got present=%v value=%v", present, avail)
	}
}

func TestVINDecode_AvailabilityOmitted_OnDBError(t *testing.T) {
	status, body := decodeVINViaHandler(t, func(context.Context, string) (bool, error) {
		return false, errors.New("connection refused")
	})

	// Graceful degradation: the good NHTSA decode still succeeds…
	if status != http.StatusOK {
		t.Fatalf("want 200 despite DB error, got %d", status)
	}
	// …and the field is OMITTED (never false) so the client treats it as
	// unknown rather than blocking the user.
	if _, present := body["available"]; present {
		t.Fatalf("available must be omitted on DB error, got %v", body["available"])
	}
	if body["make"] != "Honda" {
		t.Fatalf("decode payload damaged: make = %v", body["make"])
	}
}

func TestVINDecode_AvailabilityOmitted_WhenCheckerNotWired(t *testing.T) {
	status, body := decodeVINViaHandler(t, nil)

	if status != http.StatusOK {
		t.Fatalf("want 200, got %d", status)
	}
	if _, present := body["available"]; present {
		t.Fatal("available must be omitted when no checker is injected")
	}
}
