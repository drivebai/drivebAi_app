package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// ── Sold-car terminal guard (item 3) ─────────────────────────────────────────

// TestIsCarSold: the predicate is true ONLY for the terminal 'sold' state.
// Every owner-write path (UpdateCar/PauseCar/UploadCarPhoto/UploadCarDocument/
// DeleteCarPhoto/UpdateCarLocation/DeleteCarDocument) keys on this to reject
// with CAR_SOLD, so a false negative here would let a completed sale be edited.
func TestIsCarSold(t *testing.T) {
	if !isCarSold(&models.Car{Status: models.CarStatusSold}) {
		t.Fatal("a sold car must be reported sold")
	}
	for _, s := range []models.CarListingStatus{
		models.CarStatusAvailable,
		models.CarStatusRented,
		models.CarStatusPending,
		models.CarStatusPaused,
	} {
		if isCarSold(&models.Car{Status: s}) {
			t.Errorf("status %q must NOT be reported sold", s)
		}
	}
}

// TestWriteCarSold: the shared 409 envelope carries the CAR_SOLD machine code
// and the edit-specific message, so every sold-guarded handler returns an
// identical, client-recognisable payload.
func TestWriteCarSold(t *testing.T) {
	rr := httptest.NewRecorder()
	writeCarSold(rr)
	if rr.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, models.ErrCodeCarSold) {
		t.Errorf("body should carry code %q; got %s", models.ErrCodeCarSold, body)
	}
	if !strings.Contains(body, "sold") {
		t.Errorf("body should explain the car was sold; got %s", body)
	}
}

// TestSoldGuard_WiredIntoAllOwnerWritePaths proves every owner-write handler
// rejects a sold car with the shared CAR_SOLD envelope. The guard sits after a
// DB read (GetByID), so the runtime path needs a database; this locks the
// wiring — a new owner-write endpoint that forgets the guard, or a removed
// guard, fails here.
func TestSoldGuard_WiredIntoAllOwnerWritePaths(t *testing.T) {
	src, err := os.ReadFile("car.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(src)
	for _, fn := range []string{
		"func (h *CarHandler) UpdateCar(",
		"func (h *CarHandler) PauseCar(",
		"func (h *CarHandler) UploadCarPhoto(",
		"func (h *CarHandler) UploadCarDocument(",
		"func (h *CarHandler) DeleteCarPhoto(",
		"func (h *CarHandler) UpdateCarLocation(",
		"func (h *CarHandler) DeleteCarDocument(",
	} {
		body := extractFunc(t, s, fn)
		if !strings.Contains(body, "isCarSold(car)") {
			t.Errorf("%s must check isCarSold(car)", fn)
		}
		if !strings.Contains(body, "writeCarSold(w)") {
			t.Errorf("%s must reject a sold car via writeCarSold(w)", fn)
		}
	}
}

// ── VIN normalization + validity (item 2) ────────────────────────────────────

// TestNormalizeVIN_CaseAndSpaceCollide: the value we persist and the value we
// compare against are byte-for-byte identical regardless of how the user typed
// it — so "1hgcm82633a004352 " and "1HGCM82633A004352" collide and cannot
// coexist as two live listings.
func TestNormalizeVIN_CaseAndSpaceCollide(t *testing.T) {
	a := normalizeVIN("  1hgcm82633a004352 ")
	b := normalizeVIN("1HGCM82633A004352")
	if a != b {
		t.Fatalf("case/space variants must normalize equal: %q vs %q", a, b)
	}
	if a != "1HGCM82633A004352" {
		t.Fatalf("normalizeVIN must trim + upper-case, got %q", a)
	}
	// Both normalize to a well-formed VIN.
	if !isValidVIN(a) || !isValidVIN(b) {
		t.Fatalf("normalized collision value must be a valid VIN")
	}
}

// TestIsValidVIN pins the SAE 17-char shape: alphanumeric excluding I/O/Q, and
// upper-case only (callers normalize first).
func TestIsValidVIN(t *testing.T) {
	valid := []string{
		"1HGCM82633A004352",
		"JH4KA7561PC008269",
		"11111111111111111",
	}
	for _, v := range valid {
		if !isValidVIN(v) {
			t.Errorf("%q should be a valid VIN", v)
		}
	}
	invalid := map[string]string{
		"":                   "empty",
		"1HGCM82633A00435":   "16 chars (too short)",
		"1HGCM82633A0043522": "18 chars (too long)",
		"1hgcm82633a004352":  "lower-case (not normalized)",
		"1HGCM82633A0043I2":  "contains I",
		"1HGCM82633A0043O2":  "contains O",
		"1HGCM82633A0043Q2":  "contains Q",
		"1HGCM82633A0043-2":  "contains a hyphen",
	}
	for v, why := range invalid {
		if isValidVIN(v) {
			t.Errorf("%q should be invalid (%s)", v, why)
		}
	}
}

// TestCreateCar_RequiresValidVIN: a NEW listing with a missing or malformed VIN
// is rejected with 400 INVALID_VIN — and the rejection happens before any
// repository access (so it runs with a zero-value handler).
func TestCreateCar_RequiresValidVIN(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing vin", `{"make":"Toyota","model":"Corolla","year":2019}`},
		{"blank vin", `{"make":"Toyota","model":"Corolla","year":2019,"vin":"   "}`},
		{"short vin", `{"make":"Toyota","model":"Corolla","year":2019,"vin":"SHORT"}`},
		{"vin with I/O/Q", `{"make":"Toyota","model":"Corolla","year":2019,"vin":"1HGCM82633A0043I2"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &CarHandler{} // VIN gate returns before touching carRepo
			req := httptest.NewRequest(http.MethodPost, "/api/v1/cars", strings.NewReader(tc.body))
			ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			h.CreateCar(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), models.ErrCodeInvalidVIN) {
				t.Errorf("body should carry INVALID_VIN; got %s", rr.Body.String())
			}
		})
	}
}

// ── Sale-readiness accepts a $500 for-sale listing (item 2/4) ────────────────

// TestSalePriceGate_AcceptsFiveHundred re-affirms the relaxed floor at the
// exact predicate CreateCar + UpdateCar share: $500 (and any positive amount)
// is sale-ready, and the title document is NOT part of this gate (decision C).
func TestSalePriceGate_AcceptsFiveHundred(t *testing.T) {
	fiveHundred := &models.Car{IsForSale: true}
	fiveHundred.SalePrice.Valid = true
	fiveHundred.SalePrice.Float64 = 500
	if salePriceMissingOrNonPositive(fiveHundred) {
		t.Fatal("$500 for-sale listing must pass the price gate")
	}
	// No documents supplied — still sale-ready because title moved to the BoS
	// Accept gate.
	if missing := saleRequirementsMissing(fiveHundred, nil); len(missing) != 0 {
		t.Fatalf("$500 with no docs must be sale-ready, missing = %v", missing)
	}
}

// ── Error-code → HTTP status mapping for the new codes (item 4/8) ─────────────

// TestStatusForErr_CarSaleReleaseCodes locks the transport contract for the
// Bill-of-Sale gate codes so a new sentinel can't silently fall through to 500.
func TestStatusForErr_CarSaleReleaseCodes(t *testing.T) {
	cases := map[*models.APIError]int{
		models.ErrTitleRequired:                 http.StatusConflict,
		models.ErrInspectionChecklistIncomplete: http.StatusBadRequest,
		models.ErrTitleConditionRequired:        http.StatusBadRequest,
		models.ErrInvalidTitleCondition:         http.StatusBadRequest,
		models.ErrTitleConditionOtherRequired:   http.StatusBadRequest,
		models.ErrSellerAddressRequired:         http.StatusBadRequest,
		models.ErrBuyerAddressRequired:          http.StatusBadRequest,
	}
	for e, want := range cases {
		if got := statusForPurchaseErr(e); got != want {
			t.Errorf("%s: got status %d, want %d", e.Code, got, want)
		}
	}
}

// ── InspectAccept validate-before-capture ordering (item 4, SAFETY CRITICAL) ─

// TestInspectAccept_ValidatesBeforeCapture asserts the source-level ordering
// contract that this unit suite cannot exercise end-to-end (InspectAccept's
// first action is a DB read): the title-on-file gate, the checklist-complete
// gate, the title-condition gate, and the checklist PERSIST all appear BEFORE
// the InspectionAccept status flip and BEFORE capturePayment. If a refactor
// ever reordered a validation past capture, this fails.
func TestInspectAccept_ValidatesBeforeCapture(t *testing.T) {
	src, err := os.ReadFile("purchase_request.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := extractFunc(t, string(src), "func (h *PurchaseRequestHandler) InspectAccept(")

	titleGate := mustIndex(t, body, "ErrTitleRequired")
	checklistGate := mustIndex(t, body, "ErrInspectionChecklistIncomplete")
	titleCondGate := mustIndex(t, body, "ErrTitleConditionRequired")
	persist := mustIndex(t, body, "SaveInspectionChecklist")
	flip := mustIndex(t, body, "InspectionAccept(")
	capture := mustIndex(t, body, "capturePayment(")

	// (11) title-on-file gate precedes everything downstream.
	if !(titleGate < checklistGate && titleGate < persist && titleGate < flip && titleGate < capture) {
		t.Errorf("title-on-file gate must precede checklist/persist/flip/capture")
	}
	// (22) checklist + title-condition gates precede persist, flip, capture.
	for _, g := range []struct {
		name string
		at   int
	}{{"checklist-complete", checklistGate}, {"title-condition", titleCondGate}} {
		if !(g.at < persist && g.at < flip && g.at < capture) {
			t.Errorf("%s gate must precede persist/flip/capture", g.name)
		}
	}
	// Persist the checklist before flipping status and before capturing money.
	if !(persist < flip && flip < capture) {
		t.Errorf("order must be: persist(%d) < flip(%d) < capture(%d)", persist, flip, capture)
	}
}

// ── Publish-gate: uploading a cover photo no longer auto-publishes ───────────

// TestUploadCoverPhoto_NoLongerAutoPublishes is a source-invariant guard:
// admin approval is now the SINGLE publish gate, so UploadCarPhoto must not
// call UpdateStatus(...CarStatusAvailable...) on the cover-photo slot. (The
// handler path itself needs a DB, so this locks the removal structurally.)
func TestUploadCoverPhoto_NoLongerAutoPublishes(t *testing.T) {
	src, err := os.ReadFile("car.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := extractFunc(t, string(src), "func (h *CarHandler) UploadCarPhoto(")
	if regexp.MustCompile(`UpdateStatus\([^)]*CarStatusAvailable`).MatchString(body) {
		t.Error("UploadCarPhoto must not auto-publish (admin approval is the single publish gate)")
	}
}

// ── source-scan helpers ──────────────────────────────────────────────────────

// extractFunc returns the body of the function whose signature starts at
// header, from the header up to the next top-level `\nfunc ` declaration.
func extractFunc(t *testing.T, src, header string) string {
	t.Helper()
	start := strings.Index(src, header)
	if start < 0 {
		t.Fatalf("function %q not found in source", header)
	}
	rest := src[start+len(header):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return rest[:next]
	}
	return rest
}

// mustIndex returns the index of sub within body, failing if absent.
func mustIndex(t *testing.T, body, sub string) int {
	t.Helper()
	i := strings.Index(body, sub)
	if i < 0 {
		t.Fatalf("expected %q in function body", sub)
	}
	return i
}
