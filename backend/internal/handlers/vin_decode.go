package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// vinDecodeUpstream is the NHTSA vPIC DecodeVinValues endpoint. Free, no key
// required, no documented rate limit. Returns the same shape regardless of
// whether the VIN is valid — validity is signaled by per-field emptiness and
// the `ErrorCode` field on the single Result row.
//
// Kept as a package var (not a const) so tests can point it at an httptest
// server without redefining the handler.
var vinDecodeUpstream = "https://vpic.nhtsa.dot.gov/api/vehicles/DecodeVinValues"

// vinDecodeTimeout caps how long we'll wait on NHTSA before giving up. Their
// p95 is well under a second; if we don't hear back in 8s something is wrong
// and we should fail the request rather than holding the iOS client.
const vinDecodeTimeout = 8 * time.Second

// VINDecodeHandler exposes the NHTSA vPIC decoder behind our own API surface
// so iOS clients don't need to know about NHTSA's quirks (140-field response,
// HTTP-200-on-bad-VIN semantics, enum strings that don't match ours). Lives
// next to CarHandler but split into its own file so the upstream call,
// normalization, and enum mapping stay readable.
type VINDecodeHandler struct {
	client *http.Client
	logger *slog.Logger
	// existsByVIN reports whether a non-archived listing already holds this
	// VIN (CarRepository.ExistsByVIN in production — the SAME definition of
	// "in use" the create/update preflights rely on). Injected as a func so
	// tests can stub it without a DB. May be nil (availability omitted).
	existsByVIN func(ctx context.Context, vin string) (bool, error)
}

// NewVINDecodeHandler builds the decode handler. existsByVIN powers the
// early "VIN already listed" signal on the wizard's Search step (QA pt-12);
// pass nil to disable availability checking (response field omitted).
func NewVINDecodeHandler(logger *slog.Logger, existsByVIN func(ctx context.Context, vin string) (bool, error)) *VINDecodeHandler {
	return &VINDecodeHandler{
		client:      &http.Client{Timeout: vinDecodeTimeout},
		logger:      logger,
		existsByVIN: existsByVIN,
	}
}

// VINDecodeResponse is the normalized payload iOS consumes. Optional fields
// are intentionally pointers / empty strings so the client can decide
// per-field whether to autofill — NHTSA frequently leaves Model or BodyClass
// empty even on otherwise-valid VINs, and we want to surface that as
// "I don't know" rather than clobbering whatever the user typed.
type VINDecodeResponse struct {
	VIN          string             `json:"vin"`
	Make         string             `json:"make,omitempty"`
	Model        string             `json:"model,omitempty"`
	Year         *int               `json:"year,omitempty"`
	BodyType     models.CarBodyType `json:"body_type,omitempty"`
	FuelType     models.FuelType    `json:"fuel_type,omitempty"`
	Manufacturer string             `json:"manufacturer,omitempty"`
	VehicleType  string             `json:"vehicle_type,omitempty"`
	// Warning carries the NHTSA error text when the decode came back with a
	// non-zero ErrorCode. Empty when ErrorCode == "0". iOS surfaces this as
	// a subtle hint without blocking the form — the user can still edit.
	Warning string `json:"warning,omitempty"`
	// Available is the early "is this VIN free to list?" signal (QA pt-12):
	// false when a non-archived listing already holds this VIN, true when
	// it doesn't. OMITTED (nil) when the check could not run — clients must
	// treat absent as unknown, never as unavailable. Advisory only (TOCTOU):
	// the CreateCar preflight + partial unique index remain authoritative.
	Available *bool `json:"available,omitempty"`
}

// vpicResponse is just enough of NHTSA's payload for us to extract what we
// need. NHTSA returns ~140 fields per Result row; ignoring the rest with
// json.Decoder is cheap and keeps our struct from rotting whenever they add
// columns.
type vpicResponse struct {
	Results []struct {
		Make            string `json:"Make"`
		Model           string `json:"Model"`
		ModelYear       string `json:"ModelYear"`
		BodyClass       string `json:"BodyClass"`
		FuelTypePrimary string `json:"FuelTypePrimary"`
		Manufacturer    string `json:"Manufacturer"`
		VehicleType     string `json:"VehicleType"`
		ErrorCode       string `json:"ErrorCode"`
		ErrorText       string `json:"ErrorText"`
	} `json:"Results"`
}

// DecodeVIN handles GET /api/v1/cars/vin-decode/{vin}. Validates the VIN
// shape, calls NHTSA, and returns a normalized payload with enum-mapped
// body/fuel types so iOS can autofill the create-listing form. Wrapped in
// our own surface (instead of letting iOS call NHTSA directly) so we own
// the timeout, error shape, and enum mapping in one place.
func (h *VINDecodeHandler) DecodeVIN(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "vin")
	vin := normalizeVIN(raw)
	if !isValidVIN(vin) {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError(
			"VIN must be 17 alphanumeric characters and exclude I, O, and Q",
		))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), vinDecodeTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/%s?format=json", vinDecodeUpstream, vin)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.logger.Error("vin decode: build request", "error", err, "vin", vin)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		h.logger.Error("vin decode: upstream failed", "error", err, "vin", vin)
		// Surface as 502 so iOS can show a transient error and let the user
		// retry — the VIN itself is fine, we just couldn't reach NHTSA.
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError(
			"VIN_DECODE_UPSTREAM", "Couldn't reach the VIN decoder. Please try again.",
		))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.logger.Warn("vin decode: upstream non-200", "status", resp.StatusCode, "vin", vin)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError(
			"VIN_DECODE_UPSTREAM", "VIN decoder returned an unexpected response.",
		))
		return
	}

	// Cap the read so a malformed upstream can't exhaust memory. NHTSA's real
	// payload is ~6KB; 256KB is comfortable headroom.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		h.logger.Error("vin decode: read body", "error", err, "vin", vin)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError(
			"VIN_DECODE_UPSTREAM", "VIN decoder returned an unreadable response.",
		))
		return
	}

	var parsed vpicResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		h.logger.Error("vin decode: parse upstream", "error", err, "vin", vin)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError(
			"VIN_DECODE_UPSTREAM", "VIN decoder returned an unparseable response.",
		))
		return
	}
	if len(parsed.Results) == 0 {
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError(
			"VIN_DECODE_UPSTREAM", "VIN decoder returned no results.",
		))
		return
	}

	r0 := parsed.Results[0]
	out := VINDecodeResponse{
		VIN:          vin,
		Make:         titleCase(r0.Make),
		Model:        strings.TrimSpace(r0.Model),
		Manufacturer: strings.TrimSpace(r0.Manufacturer),
		VehicleType:  strings.TrimSpace(r0.VehicleType),
	}
	if y, err := strconv.Atoi(strings.TrimSpace(r0.ModelYear)); err == nil && y > 1900 && y < 2100 {
		out.Year = &y
	}
	if mapped, ok := mapBodyClass(r0.BodyClass); ok {
		out.BodyType = mapped
	}
	if mapped, ok := mapFuelType(r0.FuelTypePrimary); ok {
		out.FuelType = mapped
	}

	// ErrorCode "0" is the clean-decode signal. Anything else is a
	// degraded-but-still-useful response (most often "8 - No detailed data
	// available currently" for older VINs). Forward as a warning so the
	// client can show a hint while still autofilling whatever fields we
	// did get.
	errCode := strings.TrimSpace(r0.ErrorCode)
	if errCode != "" && errCode != "0" {
		out.Warning = strings.TrimSpace(r0.ErrorText)
	}

	// Reject decodes that gave us literally nothing useful — usually a
	// malformed VIN that passed our shape check but is unknown to NHTSA.
	// Better to tell the user "we couldn't find this VIN" than to dump an
	// empty autofill that does nothing.
	if out.Make == "" && out.Model == "" && out.Year == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError(
			"VIN_NOT_FOUND", "We couldn't find any details for this VIN.",
		))
		return
	}

	// Availability (QA pt-12): one definition of "in use" — the same
	// ExistsByVIN the create/update preflights call. Graceful degradation:
	// a DB error must never fail a good NHTSA decode, so we log and omit
	// the field (client treats absent as unknown).
	if h.existsByVIN != nil {
		if exists, err := h.existsByVIN(r.Context(), vin); err != nil {
			h.logger.Error("vin decode: availability check failed", "error", err, "vin", vin)
		} else {
			available := !exists
			out.Available = &available
		}
	}

	httputil.WriteJSON(w, http.StatusOK, out)
}

// normalizeVIN strips whitespace and upper-cases so the value we send to
// NHTSA and the value we persist are byte-for-byte the same regardless of
// how the user typed it.
func normalizeVIN(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// isValidVIN enforces the SAE J853 17-character VIN shape: alphanumeric
// excluding I, O, Q (those are excluded specifically to avoid confusion
// with 1 and 0). We do this client-side AND server-side so we don't waste
// upstream calls on values that can't be VINs.
func isValidVIN(vin string) bool {
	if len(vin) != 17 {
		return false
	}
	for _, r := range vin {
		switch {
		case r >= '0' && r <= '9':
			continue
		case r >= 'A' && r <= 'Z':
			if r == 'I' || r == 'O' || r == 'Q' {
				return false
			}
			continue
		default:
			return false
		}
	}
	return true
}

// titleCase converts NHTSA's all-caps strings ("HONDA") to the
// presentation case our iOS app expects ("Honda"). Single-word strings
// only — multi-word makes ("ALFA ROMEO") become "Alfa Romeo".
func titleCase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	parts := strings.Fields(strings.ToLower(s))
	for i, p := range parts {
		if p == "" {
			continue
		}
		r := []rune(p)
		r[0] = []rune(strings.ToUpper(string(r[0])))[0]
		parts[i] = string(r)
	}
	return strings.Join(parts, " ")
}

// mapBodyClass translates NHTSA's BodyClass strings to our enum. NHTSA uses
// a long-form descriptive vocabulary; we substring-match on the most
// recognizable token so subtle variations ("Sedan/Saloon" vs "Sedan") all
// land on the same iOS enum case. Unknown classes return ok=false, which
// the handler treats as "leave the iOS picker untouched".
func mapBodyClass(class string) (models.CarBodyType, bool) {
	c := strings.ToLower(class)
	switch {
	case strings.Contains(c, "sedan"), strings.Contains(c, "saloon"):
		return models.BodyTypeSedan, true
	case strings.Contains(c, "sport utility"), strings.Contains(c, "suv"):
		return models.BodyTypeSUV, true
	case strings.Contains(c, "coupe"):
		return models.BodyTypeCoupe, true
	case strings.Contains(c, "hatchback"), strings.Contains(c, "liftback"), strings.Contains(c, "notchback"):
		return models.BodyTypeHatchback, true
	case strings.Contains(c, "pickup"), strings.Contains(c, "truck"):
		return models.BodyTypeTruck, true
	case strings.Contains(c, "van"), strings.Contains(c, "minivan"):
		return models.BodyTypeVan, true
	case strings.Contains(c, "convertible"), strings.Contains(c, "cabriolet"), strings.Contains(c, "roadster"):
		return models.BodyTypeConvertible, true
	case strings.Contains(c, "wagon"):
		return models.BodyTypeWagon, true
	}
	return "", false
}

// mapFuelType translates NHTSA's FuelTypePrimary to our enum, with the same
// "unknown is OK" semantics as mapBodyClass.
func mapFuelType(fuel string) (models.FuelType, bool) {
	f := strings.ToLower(fuel)
	switch {
	case strings.Contains(f, "gasoline"), strings.Contains(f, "petrol"):
		return models.FuelTypeGas, true
	case strings.Contains(f, "diesel"):
		return models.FuelTypeDiesel, true
	case strings.Contains(f, "plug-in"), strings.Contains(f, "plug in"):
		// Match PHEV before plain "hybrid"/"electric" since the string
		// "Plug-In Hybrid Electric (PHEV)" contains both.
		return models.FuelTypePlugInHybrid, true
	case strings.Contains(f, "hybrid"):
		return models.FuelTypeHybrid, true
	case strings.Contains(f, "electric"), strings.Contains(f, "battery"):
		return models.FuelTypeElectric, true
	}
	return "", false
}
