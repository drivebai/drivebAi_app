package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

type CarHandler struct {
	carRepo   *repository.CarRepository
	photoRepo *repository.CarPhotoRepository
	docRepo   *repository.CarDocumentRepository
	userRepo  *repository.UserRepository
	uploadDir string
	// urlSigner signs car-document URLs (insurance, registration). Car
	// PHOTO URLs are not signed — they're publicly readable for Discovery.
	urlSigner          *PrivateURLSigner
	minWeeklyRentPrice float64
	autoApproveCars    bool
}

func NewCarHandler(
	carRepo *repository.CarRepository,
	photoRepo *repository.CarPhotoRepository,
	docRepo *repository.CarDocumentRepository,
	userRepo *repository.UserRepository,
	uploadDir string,
	urlSigner *PrivateURLSigner,
	minWeeklyRentPrice float64,
	autoApproveCars bool,
) *CarHandler {
	return &CarHandler{
		carRepo:            carRepo,
		photoRepo:          photoRepo,
		docRepo:            docRepo,
		userRepo:           userRepo,
		uploadDir:          uploadDir,
		urlSigner:          urlSigner,
		minWeeklyRentPrice: minWeeklyRentPrice,
		autoApproveCars:    autoApproveCars,
	}
}

// ListCars returns all cars for the authenticated owner
func (h *CarHandler) ListCars(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	cars, rentals, err := h.carRepo.GetByOwnerIDWithActiveRental(ctx, userID)
	if err != nil {
		slog.Error("failed to get cars", "error", err, "error_type", fmt.Sprintf("%T", err), "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Get owner info once
	owner, err := h.userRepo.GetByID(ctx, userID)
	if err != nil {
		slog.Error("failed to get owner", "error", err, "user_id", userID)
	}

	// Build response with photos and documents for each car
	var responses []*models.CarResponse
	for i, car := range cars {
		photos, _ := h.photoRepo.GetByCarID(ctx, car.ID)
		documents, _ := h.docRepo.GetByCarID(ctx, car.ID)
		// ListCars is owner-scoped (SELECT WHERE owner_id = $1) so the caller
		// is the owner of every row — VIN is safe to include.
		resp := car.ToResponse(photos, documents, owner, true)

		// Attach active_rental sub-object when the LEFT JOIN found a lease
		// currently occupying this car (paid + picked up + not yet returned).
		// planned_end_at and current_earned_cents are DERIVED here — the
		// canonical facts are pickup_confirmed_at + weeks.
		if i < len(rentals) && rentals[i] != nil {
			resp.ActiveRental = buildActiveRentalSummary(rentals[i])
		}
		responses = append(responses, resp)
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"cars": responses,
	})
}

// ownerCarResponse rebuilds the full owner-facing CarResponse for a car:
// photos, documents, owner info, AND the active_rental sub-object. Every
// owner-scoped WRITE endpoint (update / pause / location) responds through
// this so the client's canonical store copy never loses activeRental on an
// autosave round-trip — the QA pt-3 status parity applies to mutations,
// not just ListCars/GetCar reads.
func (h *CarHandler) ownerCarResponse(ctx context.Context, carID, ownerID uuid.UUID) *models.CarResponse {
	car, rental, err := h.carRepo.GetByIDWithActiveRental(ctx, carID)
	if err != nil || car == nil {
		slog.Error("ownerCarResponse refetch failed", "error", err, "car_id", carID)
		return nil
	}
	photos, _ := h.photoRepo.GetByCarID(ctx, car.ID)
	documents, _ := h.docRepo.GetByCarID(ctx, car.ID)
	owner, _ := h.userRepo.GetByID(ctx, ownerID)
	resp := car.ToResponse(photos, documents, owner, true)
	if rental != nil {
		resp.ActiveRental = buildActiveRentalSummary(rental)
	}
	return resp
}

// buildActiveRentalSummary derives the owner-card "active rental" snapshot
// from the repository row. planned_end_at is pickup_confirmed_at + weeks*7d.
// current_earned_cents pro-rates based on how many full weeks of the rental
// have elapsed at request time (capped at the total contracted weeks).
func buildActiveRentalSummary(row *repository.OwnerCarActiveRental) *models.ActiveRentalSummary {
	plannedEnd := row.PickupConfirmedAt.AddDate(0, 0, row.Weeks*7)

	elapsedWeeks := int(time.Since(row.PickupConfirmedAt).Hours() / (24 * 7))
	if elapsedWeeks < 0 {
		elapsedWeeks = 0
	}
	billableWeeks := elapsedWeeks
	if billableWeeks > row.Weeks {
		billableWeeks = row.Weeks
	}

	return &models.ActiveRentalSummary{
		LeaseRequestID:     row.LeaseRequestID,
		DriverID:           row.DriverID,
		DriverName:         row.DriverName,
		Weeks:              row.Weeks,
		WeeklyPriceCents:   row.EffectiveWeeklyPriceCents,
		PickupConfirmedAt:  models.RFC3339Time(row.PickupConfirmedAt),
		PlannedEndAt:       models.RFC3339Time(plannedEnd),
		CurrentEarnedCents: row.EffectiveWeeklyPriceCents * int64(billableWeeks),
		ChatID:             row.ChatID,
	}
}

// GetCar returns a specific car by ID
func (h *CarHandler) GetCar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Detail/list status parity (QA pt-3 / D9): the same join the My Cars
	// list uses, scoped to one car, so refreshing a single car can never
	// clobber the rented state the list showed.
	car, rental, err := h.carRepo.GetByIDWithActiveRental(ctx, carID)
	if err != nil {
		slog.Error("failed to get car", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	if car == nil || car.IsArchived() {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}

	// Verify ownership
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// Get photos, documents, and owner info
	photos, _ := h.photoRepo.GetByCarID(ctx, car.ID)
	documents, _ := h.docRepo.GetByCarID(ctx, car.ID)
	owner, _ := h.userRepo.GetByID(ctx, userID)

	// GetCar 403s above unless the caller is the owner — VIN is safe here.
	resp := car.ToResponse(photos, documents, owner, true)
	// active_rental carries driver name/earnings; the ownership 403 above
	// already guarantees the requester is the owner, so it is safe to attach.
	if rental != nil {
		resp.ActiveRental = buildActiveRentalSummary(rental)
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// CreateCar creates a new car listing
func (h *CarHandler) CreateCar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var req models.CreateCarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	// Validate required fields
	if req.Make == "" || req.Model == "" || req.Year == 0 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Make, model, and year are required"))
		return
	}

	// Generate title if not provided
	title := req.Title
	if title == "" {
		title = fmt.Sprintf("%d %s %s", req.Year, req.Make, req.Model)
	}

	// Create car model
	now := time.Now()
	car := &models.Car{
		ID:          uuid.New(),
		OwnerID:     userID,
		Title:       title,
		Make:        req.Make,
		Model:       req.Model,
		Year:        req.Year,
		BodyType:    req.BodyType,
		FuelType:    req.FuelType,
		Mileage:     req.Mileage,
		IsForRent:   req.IsForRent,
		IsForSale:   req.IsForSale,
		Currency:    "USD",
		Status:      models.CarStatusPending,
		IsPaused:    false,
		RentedWeeks: 0,
		TotalEarned: 0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Set defaults
	if car.BodyType == "" {
		car.BodyType = models.BodyTypeSedan
	}
	if car.FuelType == "" {
		car.FuelType = models.FuelTypeGas
	}

	// Handle optional fields
	if req.Description != nil {
		car.Description = sql.NullString{String: *req.Description, Valid: true}
	}
	// VIN is REQUIRED for every NEW listing (rent + sale) per product
	// decision. Normalize (trim + upper) then enforce the SAE 17-char shape.
	// Grandfathering applies only to legacy rows via UpdateCar — a fresh
	// create always demands a valid VIN.
	vin := ""
	if req.VIN != nil {
		vin = normalizeVIN(*req.VIN)
	}
	if !isValidVIN(vin) {
		httputil.WriteError(w, http.StatusBadRequest,
			models.NewAPIError(models.ErrCodeInvalidVIN,
				"A valid 17-character VIN is required"))
		return
	}
	car.VIN = sql.NullString{String: vin, Valid: true}
	// Pre-flight VIN-uniqueness check. The partial unique index
	// `cars_vin_unique_lower_idx` is the source of truth (and catches the race
	// below) — this lookup just lets us return a clean 409 in the common case
	// instead of an opaque 500. Empty / NULL VINs are exempt.
	if car.VIN.Valid && car.VIN.String != "" {
		exists, err := h.carRepo.ExistsByVIN(ctx, car.VIN.String)
		if err != nil {
			slog.Error("vin existence check failed", "error", err, "user_id", userID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if exists {
			writeVINConflict(w)
			return
		}
	}
	if req.Address != nil {
		car.Address = sql.NullString{String: *req.Address, Valid: true}
	}
	if req.Neighborhood != nil {
		car.Neighborhood = sql.NullString{String: *req.Neighborhood, Valid: true}
	}
	if req.Latitude != nil {
		car.Latitude = sql.NullFloat64{Float64: *req.Latitude, Valid: true}
	}
	if req.Longitude != nil {
		car.Longitude = sql.NullFloat64{Float64: *req.Longitude, Valid: true}
	}
	if req.Area != nil {
		car.Area = sql.NullString{String: *req.Area, Valid: true}
	}
	if req.Street != nil {
		car.Street = sql.NullString{String: *req.Street, Valid: true}
	}
	if req.Block != nil {
		car.Block = sql.NullString{String: *req.Block, Valid: true}
	}
	if req.Zip != nil {
		car.Zip = sql.NullString{String: *req.Zip, Valid: true}
	}
	if req.WeeklyRentPrice != nil {
		car.WeeklyRentPrice = sql.NullFloat64{Float64: *req.WeeklyRentPrice, Valid: true}
	}
	if req.SalePrice != nil {
		car.SalePrice = sql.NullFloat64{Float64: *req.SalePrice, Valid: true}
	}
	if req.MinYearsLicensed != nil {
		car.MinYearsLicensed = *req.MinYearsLicensed
	} else {
		car.MinYearsLicensed = 2
	}
	// Deposits are removed (QA pt-7 / D8): client-sent values are ignored
	// and every car stores 0. The column + JSON key survive because shipped
	// iOS builds decode deposit_amount as a non-optional Double.
	car.DepositAmount = 0
	if req.InsuranceCoverage != nil {
		car.InsuranceCoverage = *req.InsuranceCoverage
	} else {
		car.InsuranceCoverage = models.InsuranceFullCoverage
	}

	// Validate pricing
	if car.IsForRent && car.WeeklyRentPrice.Valid && car.WeeklyRentPrice.Float64 < h.minWeeklyRentPrice {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError(
			fmt.Sprintf("Weekly rent price must be at least %.0f", h.minWeeklyRentPrice),
		))
		return
	}
	// A for-sale car must carry a positive price at every write entry point.
	// UpdateCar's sale-readiness gate enforces this; without the same check
	// here a listing could be *created* at 0 or a negative price — a state the
	// update path refuses. The title-document half of that gate cannot apply at
	// create time, because documents are uploaded after the car row exists.
	if salePriceMissingOrNonPositive(car) {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError(
			"Sale price must be greater than 0 when a car is listed for sale",
		))
		return
	}

	// Save to database
	if err := h.carRepo.Create(ctx, car); err != nil {
		// Race-condition fallback: two concurrent inserts can both pass the
		// pre-flight ExistsByVIN check and only one will land. The partial
		// unique index `cars_vin_unique_lower_idx` rejects the loser with
		// Postgres SQLSTATE 23505; surface the same 409 we return above.
		if isVINUniqueViolation(err) {
			writeVINConflict(w)
			return
		}
		slog.Error("failed to create car", "error", err, "error_type", fmt.Sprintf("%T", err), "user_id", userID, "car_id", car.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Auto-approve in test/staging environments (AUTO_APPROVE_CARS=true).
	// Discover filtering logic is unchanged; this only sets the initial approval state.
	if h.autoApproveCars {
		if err := h.carRepo.SetApproved(ctx, car.ID, true); err != nil {
			slog.Warn("auto-approve failed", "car_id", car.ID, "error", err)
		} else {
			// Reflect the approved state in the create response so the client
			// doesn't show a spurious "Awaiting approval" badge in dev/staging.
			car.IsApproved = true
		}
	}

	// Get owner info for response
	owner, _ := h.userRepo.GetByID(ctx, userID)

	slog.Info("car created", "car_id", car.ID, "user_id", userID, "auto_approved", h.autoApproveCars)
	// Caller just created this car — they are the owner.
	httputil.WriteJSON(w, http.StatusCreated, car.ToResponse(nil, nil, owner, true))
}

// UpdateCar updates an existing car listing
func (h *CarHandler) UpdateCar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Get existing car
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil || car.IsArchived() {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}

	// Verify ownership
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// A sold car is terminal — reject every edit with 409 CAR_SOLD.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	var req models.UpdateCarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	// VIN rules (product decision): a valid 17-char VIN is required for all
	// listings, but legacy rows created before this rule may carry a
	// NULL/empty VIN and must NOT be force-migrated. So validate ONLY when the
	// owner is actually supplying a VIN, and never let an owner CLEAR a VIN
	// that is already on file. `car.VIN` here is still the pre-update value.
	if req.VIN != nil {
		newVIN := normalizeVIN(*req.VIN)
		hadVIN := car.VIN.Valid && car.VIN.String != ""
		if newVIN == "" {
			// Clearing: a no-op for a legacy VIN-less row (allowed), but
			// blocked when a VIN is already on file — a live listing can't
			// drop below the VIN requirement.
			if hadVIN {
				httputil.WriteError(w, http.StatusBadRequest,
					models.NewAPIError(models.ErrCodeInvalidVIN,
						"A valid 17-character VIN is required"))
				return
			}
		} else if !isValidVIN(newVIN) {
			httputil.WriteError(w, http.StatusBadRequest,
				models.NewAPIError(models.ErrCodeInvalidVIN,
					"A valid 17-character VIN is required"))
			return
		}
	}

	// Snapshot the pre-update sale flag so the sale-readiness gate below
	// fires only on the off→on TRANSITION, not on every steady-state PATCH.
	wasForSale := car.IsForSale

	// Apply updates. Status / is_paused / deposit_amount in the payload are
	// deliberately IGNORED — see applyCarUpdateRequest.
	applyCarUpdateRequest(car, &req)

	// Validate pricing
	if car.IsForRent && car.WeeklyRentPrice.Valid && car.WeeklyRentPrice.Float64 < h.minWeeklyRentPrice {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError(
			fmt.Sprintf("Weekly rent price must be at least %.0f", h.minWeeklyRentPrice),
		))
		return
	}

	// Sale-readiness validation: enabling For Sale requires a positive sale
	// price. Per decision C the 'title' document is NO LONGER required here —
	// that requirement moved to the Bill-of-Sale Accept gate — so this is now
	// purely the price check. The client mirrors it as a checklist;
	// details.missing carries machine-readable reasons.
	//
	// The gate still fires only on the off→on TRANSITION (or when the sale
	// price itself is being changed on an already-for-sale car) so an
	// unrelated autosave PATCH (description, mileage, …) never 422s.
	saleTransition := car.IsForSale && !wasForSale
	salePriceTouched := car.IsForSale && req.SalePrice != nil
	if saleTransition || salePriceTouched {
		// No document lookup needed anymore — the only remaining requirement
		// is the sale price (decision C dropped the title-document gate here).
		if missing := saleRequirementsMissing(car, nil); len(missing) > 0 {
			httputil.WriteError(w, http.StatusBadRequest,
				models.NewAPIError(models.ErrCodeSaleRequirementsNotMet,
					"This car can't be listed for sale yet").
					WithDetails(map[string]interface{}{"missing": missing}))
			return
		}
	}

	// Pre-flight VIN uniqueness check, excluding this car so a no-op VIN
	// round-trip on PATCH doesn't false-positive. Mirrors the CreateCar 409
	// contract so the iOS client can render the same "VIN already in use"
	// inline error.
	if req.VIN != nil && car.VIN.Valid && car.VIN.String != "" {
		exists, vinErr := h.carRepo.ExistsByVINExcludingID(ctx, car.VIN.String, car.ID)
		if vinErr != nil {
			slog.Error("ExistsByVINExcludingID failed", "error", vinErr, "car_id", car.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if exists {
			writeVINConflict(w)
			return
		}
	}

	// Save to database
	if err := h.carRepo.Update(ctx, car); err != nil {
		// Race fallback: a concurrent insert could have claimed the same VIN
		// between our pre-flight check and this UPDATE. The partial unique
		// index will raise 23505 — surface it as the same 409 the create
		// path uses.
		if isVINUniqueViolation(err) {
			writeVINConflict(w)
			return
		}
		slog.Error("failed to update car", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car updated", "car_id", car.ID, "user_id", userID)
	// UpdateCar 403s above unless the caller is the owner. Respond via the
	// active-rental-aware builder so an autosave PATCH can't strip the
	// Rented state from the client's store copy.
	if resp := h.ownerCarResponse(ctx, car.ID, userID); resp != nil {
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}
	httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
}

// applyCarUpdateRequest copies the non-nil fields of an UpdateCarRequest
// onto the car. Pure — no I/O — so the ignore rules below are unit-testable.
//
// Deliberately IGNORED payload fields:
//   - status / is_paused (QA pt-9 / D2): pause flows ONLY through
//     POST /cars/{carId}/pause, so a full-car autosave PATCH can never
//     clobber a rented status or silently unpause a listing;
//   - deposit_amount (QA pt-7 / D8): deposits are removed; the field is
//     accepted on the wire for old builds but never persisted (stays 0).
func applyCarUpdateRequest(car *models.Car, req *models.UpdateCarRequest) {
	if req.Title != nil {
		car.Title = *req.Title
	}
	if req.Description != nil {
		car.Description = sql.NullString{String: *req.Description, Valid: true}
	}
	if req.VIN != nil {
		vin := normalizeVIN(*req.VIN)
		if vin == "" {
			car.VIN = sql.NullString{}
		} else {
			car.VIN = sql.NullString{String: vin, Valid: true}
		}
	}
	if req.Make != nil {
		car.Make = *req.Make
	}
	if req.Model != nil {
		car.Model = *req.Model
	}
	if req.Year != nil {
		car.Year = *req.Year
	}
	if req.BodyType != nil {
		car.BodyType = *req.BodyType
	}
	if req.FuelType != nil {
		car.FuelType = *req.FuelType
	}
	if req.Mileage != nil {
		car.Mileage = *req.Mileage
	}
	if req.Address != nil {
		car.Address = sql.NullString{String: *req.Address, Valid: true}
	}
	if req.Neighborhood != nil {
		car.Neighborhood = sql.NullString{String: *req.Neighborhood, Valid: true}
	}
	if req.Latitude != nil {
		car.Latitude = sql.NullFloat64{Float64: *req.Latitude, Valid: true}
	}
	if req.Longitude != nil {
		car.Longitude = sql.NullFloat64{Float64: *req.Longitude, Valid: true}
	}
	if req.Area != nil {
		car.Area = sql.NullString{String: *req.Area, Valid: true}
	}
	if req.Street != nil {
		car.Street = sql.NullString{String: *req.Street, Valid: true}
	}
	if req.Block != nil {
		car.Block = sql.NullString{String: *req.Block, Valid: true}
	}
	if req.Zip != nil {
		car.Zip = sql.NullString{String: *req.Zip, Valid: true}
	}
	if req.IsForRent != nil {
		car.IsForRent = *req.IsForRent
	}
	if req.WeeklyRentPrice != nil {
		car.WeeklyRentPrice = sql.NullFloat64{Float64: *req.WeeklyRentPrice, Valid: true}
	}
	if req.IsForSale != nil {
		car.IsForSale = *req.IsForSale
	}
	if req.SalePrice != nil {
		car.SalePrice = sql.NullFloat64{Float64: *req.SalePrice, Valid: true}
	}
	if req.MinYearsLicensed != nil {
		car.MinYearsLicensed = *req.MinYearsLicensed
	}
	if req.InsuranceCoverage != nil {
		car.InsuranceCoverage = *req.InsuranceCoverage
	}
	// req.Status, req.IsPaused, req.DepositAmount: intentionally not applied.
}

// Machine-readable reason in SALE_REQUIREMENTS_NOT_MET details.missing.
const (
	saleMissingPriceMin = "sale_price_min"
)

// validPhotoSlots is the accepted set for POST /cars/{id}/photos. Kept in
// lockstep with the car_photos_slot_type_check CHECK constraint (migration
// 000032): the original five slots plus the three guided-capture additions.
var validPhotoSlots = map[models.PhotoSlotType]bool{
	models.PhotoSlotCoverFront:  true,
	models.PhotoSlotRight:       true,
	models.PhotoSlotLeft:        true,
	models.PhotoSlotBack:        true,
	models.PhotoSlotDashboard:   true,
	models.PhotoSlotFrontLeft34: true,
	models.PhotoSlotRearRight34: true,
	models.PhotoSlotInterior:    true,
}

// validCarDocumentTypes is the accepted set for POST /cars/{id}/documents.
// Kept in lockstep with car_documents_document_type_check (migration
// 000032); 'title' is required to enable for-sale (D4).
var validCarDocumentTypes = map[models.CarDocumentType]bool{
	models.CarDocInspection:   true,
	models.CarDocRegistration: true,
	models.CarDocPermit:       true,
	models.CarDocInsurance:    true,
	models.CarDocTitle:        true,
}

// isCarSold reports whether the listing is in the terminal 'sold' state. A
// sold car is inert: every owner-write endpoint (UpdateCar, PauseCar,
// UploadCarPhoto, UploadCarDocument, UpdateCarLocation, DeleteCarDocument)
// rejects mutations with 409 CAR_SOLD so a completed sale can never be
// edited out from under the buyer, and pause/resume can never move a car
// back off 'sold'. Pure — unit-testable without a repository.
func isCarSold(car *models.Car) bool {
	return car.Status == models.CarStatusSold
}

// writeCarSold writes the shared 409 CAR_SOLD envelope. Reuses the existing
// machine code models.ErrCodeCarSold with the edit-specific message.
func writeCarSold(w http.ResponseWriter) {
	httputil.WriteError(w, http.StatusConflict,
		models.NewAPIError(models.ErrCodeCarSold,
			"This vehicle has been sold and can no longer be edited."))
}

// pauseConflictError returns the 409 payload when the car can't be
// paused/unpaused because a rental is in flight (D2), nil otherwise.
// Pure — unit-testable without a repository.
func pauseConflictError(car *models.Car) *models.APIError {
	if car.Status == models.CarStatusRented {
		return models.NewAPIError(models.ErrCodeCarCurrentlyRented,
			"You can't pause a car during an active rental")
	}
	return nil
}

// salePriceMissingOrNonPositive is the single sale-price rule, shared by both
// write entry points (CreateCar's pre-insert guard and UpdateCar's readiness
// gate) so the two can never drift apart. There is no minimum amount: any
// strictly positive price is a valid listing. The NULL-guard is load-bearing —
// it is the only thing rejecting a for-sale listing with no price at all.
//
// Callers that already know the car is for sale still get the right answer:
// the IsForSale term short-circuits for rent-only cars.
func salePriceMissingOrNonPositive(car *models.Car) bool {
	if !car.IsForSale {
		return false
	}
	return !car.SalePrice.Valid || car.SalePrice.Float64 <= 0
}

// saleRequirementsMissing returns the unmet sale-readiness requirements for
// a car that would end up listed for sale. Per decision C the title document
// is NO LONGER required to enable For Sale (that requirement moved to the
// Bill-of-Sale Accept gate), so the only remaining check is a positive sale
// price. `documents` is retained in the signature for call-site stability.
// Empty slice = ready.
func saleRequirementsMissing(car *models.Car, documents []models.CarDocument) []string {
	missing := []string{}
	// This gate only ever runs for a car that is (becoming) for sale, so ask
	// the shared rule directly rather than re-deriving it.
	if !car.SalePrice.Valid || car.SalePrice.Float64 <= 0 {
		missing = append(missing, saleMissingPriceMin)
	}
	return missing
}

// DeleteCar soft-archives a car listing (QA pt-9 / D3).
//
// Hard DELETE is gone: it either CASCADE-destroyed chats/messages/leases/
// payments (migration 000007 CASCADE) — even mid-active-rental — or 500'd
// on the purchase/accident RESTRICT FKs, after already removing photo
// files from disk. Instead we:
//  1. return 409 CAR_HAS_ACTIVE_OBLIGATIONS while any live commitment
//     exists (active lease, open return, open handover, live purchase);
//  2. otherwise SET archived_at = now(). No rows and NO FILES are deleted —
//     history in chats/leases still references those images.
func (h *CarHandler) DeleteCar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Get existing car
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}

	// Verify ownership
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// Already archived → idempotent success (double-tap / retry safe).
	if car.IsArchived() {
		httputil.WriteSuccess(w, http.StatusOK, "Car deleted successfully", nil)
		return
	}

	// A sold car is already inert: the sale-completion flow auto-archives it
	// (W1-B), so there is nothing left to archive and no live obligations to
	// guard. Treat delete as an idempotent no-op success rather than a 409 —
	// this is the safe option (the listing is effectively gone) and avoids
	// erroring on a delete of something the user can no longer see anyway.
	if isCarSold(car) {
		httputil.WriteSuccess(w, http.StatusOK, "Car deleted successfully", nil)
		return
	}

	// Block while live commitments reference this car.
	obligations, err := h.carRepo.GetActiveObligations(ctx, carID)
	if err != nil {
		slog.Error("failed to check car obligations", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if len(obligations) > 0 {
		httputil.WriteError(w, http.StatusConflict,
			models.NewAPIError(models.ErrCodeCarHasActiveObligations,
				"This car has an active rental, return, handover or purchase in progress and can't be deleted yet").
				WithDetails(map[string]interface{}{"obligations": obligations}))
		return
	}

	if err := h.carRepo.ArchiveCar(ctx, carID); err != nil {
		slog.Error("failed to archive car", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car archived", "car_id", carID, "user_id", userID)
	httputil.WriteSuccess(w, http.StatusOK, "Car deleted successfully", nil)
}

// PauseCar toggles the paused state of a car
func (h *CarHandler) PauseCar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Get existing car
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil || car.IsArchived() {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}

	// Verify ownership
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// A sold car is terminal: pause/resume must never move it back off
	// 'sold' to available. Reject before touching status.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	// D2: pausing a rented car would hide an active rental and — on
	// unpause — re-list a car another driver physically holds. Since
	// rented cars can never become paused, unpausing safely restores
	// 'available'.
	if apiErr := pauseConflictError(car); apiErr != nil {
		httputil.WriteError(w, http.StatusConflict, apiErr)
		return
	}

	// Toggle pause state
	newIsPaused := !car.IsPaused
	newStatus := models.CarStatusAvailable
	if newIsPaused {
		newStatus = models.CarStatusPaused
	}

	if err := h.carRepo.UpdateStatus(ctx, carID, newStatus, newIsPaused); err != nil {
		slog.Error("failed to pause car", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car paused toggled", "car_id", carID, "is_paused", newIsPaused)
	// PauseCar 403s above unless the caller is the owner. Active-rental-
	// aware response — see ownerCarResponse.
	if resp := h.ownerCarResponse(ctx, carID, userID); resp != nil {
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}
	httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
}

// ListCarPhotos returns all photos for a car
func (h *CarHandler) ListCarPhotos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Verify car exists and ownership
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	photos, err := h.photoRepo.GetByCarID(ctx, carID)
	if err != nil {
		slog.Error("failed to get car photos", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	var photoResponses []models.CarPhotoResponse
	for _, p := range photos {
		photoResponses = append(photoResponses, models.CarPhotoResponse{
			ID:        p.ID,
			SlotType:  p.SlotType,
			FileURL:   p.FileURL,
			FileSize:  p.FileSize,
			CreatedAt: models.RFC3339Time(p.CreatedAt),
			UpdatedAt: models.RFC3339Time(p.UpdatedAt),
		})
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"photos": photoResponses,
	})
}

// UploadCarPhoto uploads a photo for a specific slot
func (h *CarHandler) UploadCarPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Verify car exists and ownership
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// A sold car is terminal — no further photo edits.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	// Parse multipart form (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Failed to parse form data"))
		return
	}

	// Get slot type
	slotTypeStr := r.FormValue("slot_type")
	if slotTypeStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("slot_type is required"))
		return
	}

	slotType := models.PhotoSlotType(slotTypeStr)
	if !validPhotoSlots[slotType] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid slot_type"))
		return
	}

	// Get file
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	// Validate mime type
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		// Try to detect from file
		buffer := make([]byte, 512)
		file.Read(buffer)
		contentType = http.DetectContentType(buffer)
		file.Seek(0, 0)
	}

	validTypes := map[string]string{
		"image/jpeg": ".jpg",
		"image/jpg":  ".jpg",
		"image/png":  ".png",
	}
	ext, valid := validTypes[contentType]
	if !valid {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Only JPEG and PNG images are allowed"))
		return
	}

	// Delete existing photo for this slot if exists
	existingPhoto, _ := h.photoRepo.GetByCarIDAndSlot(ctx, carID, slotType)
	if existingPhoto != nil {
		os.Remove(existingPhoto.FilePath)
	}

	// Create directory for car photos
	carDir := filepath.Join(h.uploadDir, "cars", carID.String())
	if err := os.MkdirAll(carDir, 0755); err != nil {
		slog.Error("failed to create car directory", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Generate unique filename
	photoID := uuid.New()
	filename := fmt.Sprintf("%s_%s%s", slotTypeStr, photoID.String(), ext)
	filePath := filepath.Join(carDir, filename)

	// Save file to disk
	dst, err := os.Create(filePath)
	if err != nil {
		slog.Error("failed to create file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	defer dst.Close()

	fileSize, err := io.Copy(dst, file)
	if err != nil {
		slog.Error("failed to write file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Create photo URL
	fileURL := fmt.Sprintf("/uploads/cars/%s/%s", carID.String(), filename)

	// Create or update photo record
	now := time.Now()
	photo := &models.CarPhoto{
		ID:        photoID,
		CarID:     carID,
		SlotType:  slotType,
		FilePath:  filePath,
		FileURL:   fileURL,
		FileSize:  int(fileSize),
		MimeType:  contentType,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.photoRepo.Upsert(ctx, photo); err != nil {
		slog.Error("failed to save photo record", "error", err)
		os.Remove(filePath) // Clean up file
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// NOTE: uploading a cover photo no longer publishes the listing. Admin
	// approval (ApproveCar → is_approved false→true, which also flips status
	// pending→available) is now the SINGLE publish gate. Uploading a cover
	// photo persists the photo only and never changes status.

	slog.Info("car photo uploaded", "car_id", carID, "slot_type", slotType, "photo_id", photoID)
	httputil.WriteJSON(w, http.StatusOK, models.CarPhotoResponse{
		ID:        photo.ID,
		SlotType:  photo.SlotType,
		FileURL:   photo.FileURL,
		FileSize:  photo.FileSize,
		CreatedAt: models.RFC3339Time(photo.CreatedAt),
		UpdatedAt: models.RFC3339Time(photo.UpdatedAt),
	})
}

// DeleteCarPhoto deletes a car photo
func (h *CarHandler) DeleteCarPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	photoIDStr := chi.URLParam(r, "photoId")
	photoID, err := uuid.Parse(photoIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid photo ID"))
		return
	}

	// Verify car exists and ownership
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}
	// A sold car is frozen: the ex-owner must not be able to tamper with the
	// completed sale's photo record (those photos still surface in the buyer +
	// admin purchase-detail views). Same guard as the other owner-write paths.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	// Get photo
	photo, err := h.photoRepo.GetByID(ctx, photoID)
	if err != nil || photo == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Photo not found"))
		return
	}

	// Verify photo belongs to this car
	if photo.CarID != carID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Photo does not belong to this car"))
		return
	}

	// Delete file from disk
	os.Remove(photo.FilePath)

	// Delete from database
	if err := h.photoRepo.Delete(ctx, photoID); err != nil {
		slog.Error("failed to delete photo", "error", err, "photo_id", photoID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car photo deleted", "car_id", carID, "photo_id", photoID)
	httputil.WriteSuccess(w, http.StatusOK, "Photo deleted successfully", nil)
}

// ListCarDocuments returns all documents for a car
func (h *CarHandler) ListCarDocuments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Verify car exists and ownership
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	documents, err := h.docRepo.GetByCarID(ctx, carID)
	if err != nil {
		slog.Error("failed to get car documents", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	var docResponses []models.CarDocumentResponse
	for _, d := range documents {
		docResponses = append(docResponses, models.CarDocumentResponse{
			ID:           d.ID,
			DocumentType: d.DocumentType,
			FileName:     d.FileName,
			// Sign per response — DB stores raw `/uploads/cars/.../documents/...`.
			FileURL:   h.urlSigner.Sign(d.FileURL),
			FileSize:  d.FileSize,
			CreatedAt: models.RFC3339Time(d.CreatedAt),
			UpdatedAt: models.RFC3339Time(d.UpdatedAt),
		})
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"documents": docResponses,
	})
}

// UploadCarDocument uploads a document for a car
func (h *CarHandler) UploadCarDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Verify car exists and ownership
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// A sold car is terminal — no further document edits.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	// Parse multipart form (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Failed to parse form data"))
		return
	}

	// Get document type
	docTypeStr := r.FormValue("document_type")
	if docTypeStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("document_type is required"))
		return
	}

	docType := models.CarDocumentType(docTypeStr)
	if !validCarDocumentTypes[docType] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid document_type"))
		return
	}

	// Get file
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	// Validate mime type
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		buffer := make([]byte, 512)
		file.Read(buffer)
		contentType = http.DetectContentType(buffer)
		file.Seek(0, 0)
	}

	validMimeTypes := map[string]string{
		"image/jpeg":      ".jpg",
		"image/jpg":       ".jpg",
		"image/png":       ".png",
		"application/pdf": ".pdf",
	}
	ext, valid := validMimeTypes[contentType]
	if !valid {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Only JPEG, PNG, and PDF files are allowed"))
		return
	}

	// Create directory for car documents
	carDir := filepath.Join(h.uploadDir, "cars", carID.String(), "documents")
	if err := os.MkdirAll(carDir, 0755); err != nil {
		slog.Error("failed to create car directory", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Generate unique filename
	docID := uuid.New()
	originalName := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	filename := fmt.Sprintf("%s_%s_%s%s", docTypeStr, originalName, docID.String()[:8], ext)
	filePath := filepath.Join(carDir, filename)

	// Save file to disk
	dst, err := os.Create(filePath)
	if err != nil {
		slog.Error("failed to create file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	defer dst.Close()

	fileSize, err := io.Copy(dst, file)
	if err != nil {
		slog.Error("failed to write file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Create document URL
	fileURL := fmt.Sprintf("/uploads/cars/%s/documents/%s", carID.String(), filename)

	// Create document record
	now := time.Now()
	doc := &models.CarDocument{
		ID:           docID,
		CarID:        carID,
		DocumentType: docType,
		FileName:     header.Filename,
		FilePath:     filePath,
		FileURL:      fileURL,
		FileSize:     int(fileSize),
		MimeType:     contentType,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := h.docRepo.Create(ctx, doc); err != nil {
		slog.Error("failed to save document record", "error", err)
		os.Remove(filePath)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car document uploaded", "car_id", carID, "doc_type", docType, "doc_id", docID)
	httputil.WriteJSON(w, http.StatusOK, models.CarDocumentResponse{
		ID:           doc.ID,
		DocumentType: doc.DocumentType,
		FileName:     doc.FileName,
		FileURL:      h.urlSigner.Sign(doc.FileURL),
		FileSize:     doc.FileSize,
		CreatedAt:    models.RFC3339Time(doc.CreatedAt),
		UpdatedAt:    models.RFC3339Time(doc.UpdatedAt),
	})
}

// ListAvailableListings returns all available cars for drivers to browse (public endpoint)
func (h *CarHandler) ListAvailableListings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get query parameters for filtering
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "available"
	}

	search := r.URL.Query().Get("search")

	cars, err := h.carRepo.GetAvailableListings(ctx, status, search)
	if err != nil {
		slog.Error("failed to get available listings", "error", err, "error_type", fmt.Sprintf("%T", err), "status", status, "search", search)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Build response with photos and owner info for each car.
	//
	// This is the PUBLIC discovery surface — every authenticated driver can
	// hit it. VINs must be omitted: a (VIN, make, model) tuple is enough to
	// pull title/accident history or file fraudulent insurance claims, and
	// nothing about Discovery's UX needs the VIN. Owners still see their own
	// VINs via /cars and /cars/{id}, which gate on ownership.
	var responses []*models.CarResponse
	for _, car := range cars {
		photos, _ := h.photoRepo.GetByCarID(ctx, car.ID)
		owner, _ := h.userRepo.GetByID(ctx, car.OwnerID)
		responses = append(responses, car.ToResponse(photos, nil, owner, false))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"listings": responses,
		"count":    len(responses),
	})
}

// UpdateCarLocation updates only the location of a car (owner-only)
func (h *CarHandler) UpdateCarLocation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	// Get existing car
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}

	// Verify ownership
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// A sold car is terminal — no further location edits.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	var req models.UpdateCarLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	// Validate lat/lng
	if req.Latitude == nil || req.Longitude == nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("latitude and longitude are required"))
		return
	}
	if *req.Latitude < -90 || *req.Latitude > 90 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("latitude must be between -90 and 90"))
		return
	}
	if *req.Longitude < -180 || *req.Longitude > 180 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("longitude must be between -180 and 180"))
		return
	}

	area := ""
	if req.Area != nil {
		area = *req.Area
	}
	street := ""
	if req.Street != nil {
		street = *req.Street
	}
	block := ""
	if req.Block != nil {
		block = *req.Block
	}
	zip := ""
	if req.Zip != nil {
		zip = *req.Zip
	}

	slog.Info("updating car location", "car_id", carID, "lat", *req.Latitude, "lng", *req.Longitude, "area", area, "street", street)

	if err := h.carRepo.UpdateLocation(ctx, carID, *req.Latitude, *req.Longitude, area, street, block, zip); err != nil {
		slog.Error("failed to update car location", "error", err, "car_id", carID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car location updated", "car_id", carID, "user_id", userID)
	// UpdateCarLocation 403s above unless the caller is the owner. Active-
	// rental-aware response — see ownerCarResponse.
	if resp := h.ownerCarResponse(ctx, carID, userID); resp != nil {
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}
	httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
}

// DeleteCarDocument deletes a car document
func (h *CarHandler) DeleteCarDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := httputil.GetUserID(ctx)
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	carIDStr := chi.URLParam(r, "carId")
	carID, err := uuid.Parse(carIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car ID"))
		return
	}

	docIDStr := chi.URLParam(r, "docId")
	docID, err := uuid.Parse(docIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid document ID"))
		return
	}

	// Verify car exists and ownership
	car, err := h.carRepo.GetByID(ctx, carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "You do not own this car"))
		return
	}

	// A sold car is terminal — no further document edits.
	if isCarSold(car) {
		writeCarSold(w)
		return
	}

	// Get document
	doc, err := h.docRepo.GetByID(ctx, docID)
	if err != nil || doc == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Document not found"))
		return
	}

	// Verify document belongs to this car
	if doc.CarID != carID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Document does not belong to this car"))
		return
	}

	// Delete file from disk
	os.Remove(doc.FilePath)

	// Delete from database
	if err := h.docRepo.Delete(ctx, docID); err != nil {
		slog.Error("failed to delete document", "error", err, "doc_id", docID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	slog.Info("car document deleted", "car_id", carID, "doc_id", docID)
	httputil.WriteSuccess(w, http.StatusOK, "Document deleted successfully", nil)
}

// writeVINConflict returns the exact 409 body the iOS client expects when a
// VIN is already in use. The shape (`error` as a string, plus `message`)
// intentionally differs from httputil.WriteError's nested APIError envelope —
// that's why we build the response inline instead of going through
// httputil.WriteError.
func writeVINConflict(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   "vin_already_in_use",
		"message": "VIN already in use",
	})
}

// isVINUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) raised by the VIN index. We string-match here
// to stay consistent with how lease_request_repository handles 23505 — no
// new dependency on pgconn — and we require the index name so we don't
// mis-attribute violations from other unique constraints on the cars table.
func isVINUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "23505") && !strings.Contains(msg, "duplicate key") {
		return false
	}
	return strings.Contains(msg, "cars_vin_unique_lower_idx")
}
