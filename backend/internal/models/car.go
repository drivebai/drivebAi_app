package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// CarListingStatus represents the status of a car listing
type CarListingStatus string

const (
	CarStatusAvailable CarListingStatus = "available"
	CarStatusRented    CarListingStatus = "rented"
	CarStatusPending   CarListingStatus = "pending"
	CarStatusPaused    CarListingStatus = "paused"
	// CarStatusSold is a terminal state set the moment a purchase moves
	// to `completed` or `rejected_upheld` — the car has been sold and is
	// no longer eligible for either rental or purchase. Discovery filters
	// exclude these rows (see car_repository.GetAvailableListings).
	CarStatusSold CarListingStatus = "sold"
)

// CarBodyType represents the body type of a car
type CarBodyType string

const (
	BodyTypeSedan       CarBodyType = "sedan"
	BodyTypeSUV         CarBodyType = "suv"
	BodyTypeCoupe       CarBodyType = "coupe"
	BodyTypeHatchback   CarBodyType = "hatchback"
	BodyTypeTruck       CarBodyType = "truck"
	BodyTypeVan         CarBodyType = "van"
	BodyTypeConvertible CarBodyType = "convertible"
	BodyTypeWagon       CarBodyType = "wagon"
)

// FuelType represents the fuel type of a car
type FuelType string

const (
	FuelTypeGas          FuelType = "gas"
	FuelTypeDiesel       FuelType = "diesel"
	FuelTypeElectric     FuelType = "electric"
	FuelTypeHybrid       FuelType = "hybrid"
	FuelTypePlugInHybrid FuelType = "plug_in_hybrid"
)

// InsuranceCoverage represents insurance coverage requirements
type InsuranceCoverage string

const (
	InsuranceLiabilityOnly InsuranceCoverage = "liability_only"
	InsuranceFullCoverage  InsuranceCoverage = "full_coverage"
)

// PhotoSlotType represents the type of photo slot
type PhotoSlotType string

const (
	PhotoSlotCoverFront PhotoSlotType = "cover_front"
	PhotoSlotRight      PhotoSlotType = "right"
	PhotoSlotLeft       PhotoSlotType = "left"
	PhotoSlotBack       PhotoSlotType = "back"
	PhotoSlotDashboard  PhotoSlotType = "dashboard"
	// Guided-capture slots added by migration 000032. Raw values are the
	// canonical storage strings; the original five stay unchanged so
	// cover_front auto-publish and every cover-photo subquery keep working
	// with zero row rewrites.
	PhotoSlotFrontLeft34 PhotoSlotType = "front_left_34"
	PhotoSlotRearRight34 PhotoSlotType = "rear_right_34"
	PhotoSlotInterior    PhotoSlotType = "interior"
)

// CarDocumentType represents the type of car document
type CarDocumentType string

const (
	CarDocInspection   CarDocumentType = "inspection"
	CarDocRegistration CarDocumentType = "registration"
	CarDocPermit       CarDocumentType = "permit"
	CarDocInsurance    CarDocumentType = "insurance"
	// CarDocTitle (migration 000032) is required before a listing can be
	// enabled for sale (D4) and before admin approval of a for-sale car (D5).
	CarDocTitle CarDocumentType = "title"
)

// ErrCodeInvalidVIN is returned (400) when CreateCar/UpdateCar is given a
// missing or malformed VIN. A valid 17-char VIN is required for ALL new
// listings (rent + sale); legacy NULL/empty-VIN rows are grandfathered and
// only re-validated when the owner actually supplies a VIN.
const ErrCodeInvalidVIN = "INVALID_VIN"

// RequiredCarDocumentTypes returns the document types a car must have on
// file before it can be approved (admin ApproveCar): registration,
// inspection and insurance. Title is intentionally NOT required here
// (decision C) — the title requirement moved off the listing/approval stage
// and is enforced later at the Bill-of-Sale Accept gate. The isForSale
// parameter is retained so the many call sites don't churn, but no longer
// changes the result. Single definition shared by the admin approve guard,
// the admin list/detail "missing docs" badge, and UpdateCar's
// sale-readiness check.
func RequiredCarDocumentTypes(isForSale bool) []CarDocumentType {
	return []CarDocumentType{CarDocRegistration, CarDocInspection, CarDocInsurance}
}

// MissingRequiredCarDocuments computes which required document types are
// absent from the given on-file set (raw document_type strings). Returns an
// empty (non-nil) slice when nothing is missing so JSON encodes `[]` rather
// than `null`.
func MissingRequiredCarDocuments(isForSale bool, onFile []string) []string {
	have := make(map[CarDocumentType]bool, len(onFile))
	for _, t := range onFile {
		have[CarDocumentType(t)] = true
	}
	missing := []string{}
	for _, t := range RequiredCarDocumentTypes(isForSale) {
		if !have[t] {
			missing = append(missing, string(t))
		}
	}
	return missing
}

// Car represents a car listing in the system
type Car struct {
	ID          uuid.UUID      `json:"id"`
	OwnerID     uuid.UUID      `json:"owner_id"`
	Title       string         `json:"title"`
	Description sql.NullString `json:"-"`

	// Specs
	VIN      sql.NullString `json:"-"`
	Make     string         `json:"make"`
	Model    string         `json:"model"`
	Year     int            `json:"year"`
	BodyType CarBodyType    `json:"body_type"`
	FuelType FuelType       `json:"fuel_type"`
	Mileage  int            `json:"mileage"`

	// Location
	Address      sql.NullString  `json:"-"`
	Neighborhood sql.NullString  `json:"-"`
	Latitude     sql.NullFloat64 `json:"-"`
	Longitude    sql.NullFloat64 `json:"-"`
	Area         sql.NullString  `json:"-"`
	Street       sql.NullString  `json:"-"`
	Block        sql.NullString  `json:"-"`
	Zip          sql.NullString  `json:"-"`

	// Pricing
	IsForRent       bool            `json:"is_for_rent"`
	WeeklyRentPrice sql.NullFloat64 `json:"-"`
	IsForSale       bool            `json:"is_for_sale"`
	SalePrice       sql.NullFloat64 `json:"-"`
	Currency        string          `json:"currency"`

	// Requirements
	MinYearsLicensed  int               `json:"min_years_licensed"`
	DepositAmount     float64           `json:"deposit_amount"`
	InsuranceCoverage InsuranceCoverage `json:"insurance_coverage"`

	// Status
	Status   CarListingStatus `json:"status"`
	IsPaused bool             `json:"is_paused"`

	// IsApproved is the admin moderation gate (migration 000015). New
	// listings default FALSE and go live only after an admin approves;
	// grandfathered pre-000015 rows were backfilled to TRUE. Not emitted on
	// the raw Car JSON — surfaced to clients via CarResponse.IsApproved.
	IsApproved bool `json:"-"`

	// Stats
	RentedWeeks int     `json:"rented_weeks"`
	TotalEarned float64 `json:"total_earned"`

	// Soft-archive marker (migration 000032). Non-NULL means the owner
	// "deleted" the listing: it is excluded from Discover, owner lists and
	// VIN uniqueness, but the row (and its files) survive so historical
	// chats/leases/payments keep resolving.
	ArchivedAt sql.NullTime `json:"-"`

	// Timestamps
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsArchived reports whether the listing has been soft-archived.
func (c *Car) IsArchived() bool {
	return c.ArchivedAt.Valid
}

// CarPhoto represents a photo of a car
type CarPhoto struct {
	ID        uuid.UUID     `json:"id"`
	CarID     uuid.UUID     `json:"car_id"`
	SlotType  PhotoSlotType `json:"slot_type"`
	FilePath  string        `json:"-"`
	FileURL   string        `json:"file_url"`
	FileSize  int           `json:"file_size"`
	MimeType  string        `json:"mime_type"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// CarDocument represents a document associated with a car
type CarDocument struct {
	ID           uuid.UUID       `json:"id"`
	CarID        uuid.UUID       `json:"car_id"`
	DocumentType CarDocumentType `json:"document_type"`
	FileName     string          `json:"file_name"`
	FilePath     string          `json:"-"`
	FileURL      string          `json:"file_url"`
	FileSize     int             `json:"file_size"`
	MimeType     string          `json:"mime_type"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ActiveRentalSummary is a per-car snapshot of the lease currently occupying
// the car (paid + pickup_confirmed + not yet returned). Attached to CarResponse
// so the owner's My Cars grid can show "Rented to Jamie R. · 4 weeks · $180/wk"
// without a second round-trip to /lease-requests.
//
// All money fields are cents to stay consistent with the rest of the payment
// pipeline (payments.amount, PaymentSummary.Amount). PlannedEndAt and
// CurrentEarnedCents are DERIVED — computed in the handler from
// pickup_confirmed_at + weeks — and are NOT stored on any table.
type ActiveRentalSummary struct {
	LeaseRequestID     uuid.UUID   `json:"lease_request_id"`
	DriverID           uuid.UUID   `json:"driver_id"`
	DriverName         string      `json:"driver_name"`
	Weeks              int         `json:"weeks"`
	WeeklyPriceCents   int64       `json:"weekly_price_cents"`
	PickupConfirmedAt  RFC3339Time `json:"pickup_confirmed_at"`
	PlannedEndAt       RFC3339Time `json:"planned_end_at"`
	CurrentEarnedCents int64       `json:"current_earned_cents"`
	// ChatID is the driver↔owner chat for this rental, when one exists
	// (deterministic via uq_chats_car_driver_owner). Lets the owner's My
	// Cars row deep-link straight into the conversation. Omitted when no
	// chat has been created for the pair.
	ChatID *uuid.UUID `json:"chat_id,omitempty"`
}

// CarResponse is the API response format for a car
type CarResponse struct {
	ID          uuid.UUID `json:"id"`
	OwnerID     uuid.UUID `json:"owner_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`

	// Specs
	Specs CarSpecsResponse `json:"specs"`

	// Location
	Location CarLocationResponse `json:"location"`

	// Pricing
	IsForRent       bool     `json:"is_for_rent"`
	WeeklyRentPrice *float64 `json:"weekly_rent_price,omitempty"`
	IsForSale       bool     `json:"is_for_sale"`
	SalePrice       *float64 `json:"sale_price,omitempty"`
	Currency        string   `json:"currency"`

	// Requirements
	Requirements CarRequirementsResponse `json:"requirements"`

	// Status
	Status   CarListingStatus `json:"status"`
	IsPaused bool             `json:"is_paused"`
	// IsApproved lets the client render an "Awaiting approval" state: false
	// means the listing is still in the admin moderation queue and is not
	// yet visible in Discover; true means approved (and, when status is
	// available and not paused, live).
	IsApproved bool `json:"is_approved"`

	// Stats
	RentedWeeks int     `json:"rented_weeks"`
	TotalEarned float64 `json:"total_earned"`

	// Photos and Documents
	Photos    []CarPhotoResponse    `json:"photos"`
	Documents []CarDocumentResponse `json:"documents"`

	// Owner info (for display)
	Owner *CarOwnerResponse `json:"owner,omitempty"`

	// ActiveRental is populated on the owner's own /cars listing when a lease
	// is currently active on this car (paid, picked up, not yet returned).
	// Omitted otherwise so drivers browsing Discovery never see rental data
	// from another user's transaction.
	ActiveRental *ActiveRentalSummary `json:"active_rental,omitempty"`

	// Timestamps
	CreatedAt RFC3339Time `json:"created_at"`
	UpdatedAt RFC3339Time `json:"updated_at"`
}

type CarSpecsResponse struct {
	VIN      *string     `json:"vin,omitempty"`
	Make     string      `json:"make"`
	Model    string      `json:"model"`
	Year     int         `json:"year"`
	BodyType CarBodyType `json:"body_type"`
	FuelType FuelType    `json:"fuel_type"`
	Mileage  int         `json:"mileage"`
}

type CarLocationResponse struct {
	Address      string   `json:"address"`
	Neighborhood string   `json:"neighborhood"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Area         string   `json:"area"`
	Street       string   `json:"street"`
	Block        string   `json:"block"`
	Zip          string   `json:"zip"`
}

type CarRequirementsResponse struct {
	MinYearsLicensed  int               `json:"min_years_licensed"`
	DepositAmount     float64           `json:"deposit_amount"`
	InsuranceCoverage InsuranceCoverage `json:"insurance_coverage"`
}

type CarPhotoResponse struct {
	ID        uuid.UUID     `json:"id"`
	SlotType  PhotoSlotType `json:"slot_type"`
	FileURL   string        `json:"file_url"`
	FileSize  int           `json:"file_size"`
	CreatedAt RFC3339Time   `json:"created_at"`
	UpdatedAt RFC3339Time   `json:"updated_at"`
}

type CarDocumentResponse struct {
	ID           uuid.UUID       `json:"id"`
	DocumentType CarDocumentType `json:"document_type"`
	FileName     string          `json:"file_name"`
	FileURL      string          `json:"file_url"`
	FileSize     int             `json:"file_size"`
	CreatedAt    RFC3339Time     `json:"created_at"`
	UpdatedAt    RFC3339Time     `json:"updated_at"`
}

type CarOwnerResponse struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	ProfilePhotoURL *string   `json:"profile_photo_url,omitempty"`
	// Rating and review count would come from a reviews table in the future
	Rating      float64 `json:"rating"`
	ReviewCount int     `json:"review_count"`
}

// ToResponse converts a Car model to CarResponse.
//
// includeVIN gates the VIN field on the response. VINs are sensitive — a
// VIN plus a make/model is enough to pull title history, file fraudulent
// insurance claims, or run identity-grade vehicle lookups — so we expose
// them ONLY when the viewer is the car's owner (or an admin path that
// constructs its own DTO). Public listings, drivers browsing Discovery,
// and chat surfaces must pass false and get nil under specs.vin.
//
// Phrased as a required parameter rather than an Options struct so that
// every call site has to make an explicit decision — if you don't know
// who the viewer is, the only safe default is false.
func (c *Car) ToResponse(photos []CarPhoto, documents []CarDocument, owner *User, includeVIN bool) *CarResponse {
	resp := &CarResponse{
		ID:          c.ID,
		OwnerID:     c.OwnerID,
		Title:       c.Title,
		Description: "",
		Specs: CarSpecsResponse{
			Make:     c.Make,
			Model:    c.Model,
			Year:     c.Year,
			BodyType: c.BodyType,
			FuelType: c.FuelType,
			Mileage:  c.Mileage,
		},
		Location: CarLocationResponse{
			Address:      "",
			Neighborhood: "",
		},
		IsForRent: c.IsForRent,
		IsForSale: c.IsForSale,
		Currency:  c.Currency,
		Requirements: CarRequirementsResponse{
			MinYearsLicensed:  c.MinYearsLicensed,
			DepositAmount:     c.DepositAmount,
			InsuranceCoverage: c.InsuranceCoverage,
		},
		Status:      c.Status,
		IsPaused:    c.IsPaused,
		IsApproved:  c.IsApproved,
		RentedWeeks: c.RentedWeeks,
		TotalEarned: c.TotalEarned,
		Photos:      make([]CarPhotoResponse, 0),
		Documents:   make([]CarDocumentResponse, 0),
		CreatedAt:   RFC3339Time(c.CreatedAt),
		UpdatedAt:   RFC3339Time(c.UpdatedAt),
	}

	// Handle nullable fields
	if c.Description.Valid {
		resp.Description = c.Description.String
	}
	if c.Address.Valid {
		resp.Location.Address = c.Address.String
	}
	if c.Neighborhood.Valid {
		resp.Location.Neighborhood = c.Neighborhood.String
	}
	if c.Latitude.Valid {
		lat := c.Latitude.Float64
		resp.Location.Latitude = &lat
	}
	if c.Longitude.Valid {
		lng := c.Longitude.Float64
		resp.Location.Longitude = &lng
	}
	if c.Area.Valid {
		resp.Location.Area = c.Area.String
	}
	if c.Street.Valid {
		resp.Location.Street = c.Street.String
	}
	if c.Block.Valid {
		resp.Location.Block = c.Block.String
	}
	if c.Zip.Valid {
		resp.Location.Zip = c.Zip.String
	}
	if c.WeeklyRentPrice.Valid {
		price := c.WeeklyRentPrice.Float64
		resp.WeeklyRentPrice = &price
	}
	if c.SalePrice.Valid {
		price := c.SalePrice.Float64
		resp.SalePrice = &price
	}
	if includeVIN && c.VIN.Valid && c.VIN.String != "" {
		vin := c.VIN.String
		resp.Specs.VIN = &vin
	}

	// Convert photos
	for _, p := range photos {
		resp.Photos = append(resp.Photos, CarPhotoResponse{
			ID:        p.ID,
			SlotType:  p.SlotType,
			FileURL:   p.FileURL,
			FileSize:  p.FileSize,
			CreatedAt: RFC3339Time(p.CreatedAt),
			UpdatedAt: RFC3339Time(p.UpdatedAt),
		})
	}

	// Convert documents
	for _, d := range documents {
		resp.Documents = append(resp.Documents, CarDocumentResponse{
			ID:           d.ID,
			DocumentType: d.DocumentType,
			FileName:     d.FileName,
			FileURL:      d.FileURL,
			FileSize:     d.FileSize,
			CreatedAt:    RFC3339Time(d.CreatedAt),
			UpdatedAt:    RFC3339Time(d.UpdatedAt),
		})
	}

	// Add owner info if provided
	if owner != nil {
		ownerName := owner.FirstName
		if owner.LastName != "" {
			ownerName += " " + owner.LastName
		}
		resp.Owner = &CarOwnerResponse{
			ID:              owner.ID,
			Name:            ownerName,
			ProfilePhotoURL: owner.ProfilePhotoURL,
			Rating:          5.0, // Default rating for now
			ReviewCount:     0,   // Default review count
		}
	}

	return resp
}

// CreateCarRequest is the request body for creating a car
type CreateCarRequest struct {
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`

	// Specs
	VIN      *string     `json:"vin,omitempty"`
	Make     string      `json:"make"`
	Model    string      `json:"model"`
	Year     int         `json:"year"`
	BodyType CarBodyType `json:"body_type"`
	FuelType FuelType    `json:"fuel_type"`
	Mileage  int         `json:"mileage"`

	// Location
	Address      *string  `json:"address,omitempty"`
	Neighborhood *string  `json:"neighborhood,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Area         *string  `json:"area,omitempty"`
	Street       *string  `json:"street,omitempty"`
	Block        *string  `json:"block,omitempty"`
	Zip          *string  `json:"zip,omitempty"`

	// Pricing
	IsForRent       bool     `json:"is_for_rent"`
	WeeklyRentPrice *float64 `json:"weekly_rent_price,omitempty"`
	IsForSale       bool     `json:"is_for_sale"`
	SalePrice       *float64 `json:"sale_price,omitempty"`

	// Requirements
	MinYearsLicensed *int `json:"min_years_licensed,omitempty"`
	// DepositAmount is DEPRECATED (QA round pt-7): accepted for old-build
	// wire compatibility but ignored — deposits never entered any payment
	// formula and are being removed. CreateCar always stores 0.
	DepositAmount     *float64           `json:"deposit_amount,omitempty"`
	InsuranceCoverage *InsuranceCoverage `json:"insurance_coverage,omitempty"`
}

// UpdateCarRequest is the request body for updating a car
type UpdateCarRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`

	// Specs
	VIN      *string      `json:"vin,omitempty"`
	Make     *string      `json:"make,omitempty"`
	Model    *string      `json:"model,omitempty"`
	Year     *int         `json:"year,omitempty"`
	BodyType *CarBodyType `json:"body_type,omitempty"`
	FuelType *FuelType    `json:"fuel_type,omitempty"`
	Mileage  *int         `json:"mileage,omitempty"`

	// Location
	Address      *string  `json:"address,omitempty"`
	Neighborhood *string  `json:"neighborhood,omitempty"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Area         *string  `json:"area,omitempty"`
	Street       *string  `json:"street,omitempty"`
	Block        *string  `json:"block,omitempty"`
	Zip          *string  `json:"zip,omitempty"`

	// Pricing
	IsForRent       *bool    `json:"is_for_rent,omitempty"`
	WeeklyRentPrice *float64 `json:"weekly_rent_price,omitempty"`
	IsForSale       *bool    `json:"is_for_sale,omitempty"`
	SalePrice       *float64 `json:"sale_price,omitempty"`

	// Requirements
	MinYearsLicensed *int `json:"min_years_licensed,omitempty"`
	// DepositAmount is DEPRECATED (QA round pt-7): accepted but ignored.
	DepositAmount     *float64           `json:"deposit_amount,omitempty"`
	InsuranceCoverage *InsuranceCoverage `json:"insurance_coverage,omitempty"`

	// Status / IsPaused are DEPRECATED (QA round pt-9, decision D2):
	// accepted for old-build wire compatibility but IGNORED by UpdateCar.
	// Pause state flows exclusively through POST /cars/{carId}/pause so a
	// full-car autosave PATCH can never clobber a rented/paused status.
	Status   *CarListingStatus `json:"status,omitempty"`
	IsPaused *bool             `json:"is_paused,omitempty"`
}

// UpdateCarLocationRequest is the request body for updating car location
type UpdateCarLocationRequest struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Area      *string  `json:"area,omitempty"`
	Street    *string  `json:"street,omitempty"`
	Block     *string  `json:"block,omitempty"`
	Zip       *string  `json:"zip,omitempty"`
}
