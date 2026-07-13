package models

import (
	"time"

	"github.com/google/uuid"
)

// ─── State machine ──────────────────────────────────────────────────────────

// PurchaseRequestStatus tracks the buy-the-car lifecycle. See DESIGN SPEC
// §1 for the state diagram. Terminal states are:
//
//	completed, rejected_upheld       → car flips to 'sold'
//	rejected_refunded                → hold released, no sale
//	declined, cancelled, expired,
//	expired_auth                      → offer never happened, no side-effects
type PurchaseRequestStatus string

const (
	PurchaseStatusRequested          PurchaseRequestStatus = "requested"
	PurchaseStatusAccepted           PurchaseRequestStatus = "accepted"
	PurchaseStatusDeclined           PurchaseRequestStatus = "declined"
	PurchaseStatusCancelled          PurchaseRequestStatus = "cancelled"
	PurchaseStatusBOSPendingSeller   PurchaseRequestStatus = "bos_pending_seller"
	PurchaseStatusBOSPendingBuyer    PurchaseRequestStatus = "bos_pending_buyer"
	PurchaseStatusBOSSigned          PurchaseRequestStatus = "bos_signed"
	PurchaseStatusPaymentAuthorized  PurchaseRequestStatus = "payment_authorized"
	PurchaseStatusHandoverScheduled  PurchaseRequestStatus = "handover_scheduled"
	PurchaseStatusAwaitingInspection PurchaseRequestStatus = "awaiting_inspection"
	PurchaseStatusInspectionAccepted PurchaseRequestStatus = "inspection_accepted"
	PurchaseStatusCompleted          PurchaseRequestStatus = "completed"
	PurchaseStatusInspectionRejected PurchaseRequestStatus = "inspection_rejected"
	PurchaseStatusRejectedRefunded   PurchaseRequestStatus = "rejected_refunded"
	PurchaseStatusRejectedUpheld     PurchaseRequestStatus = "rejected_upheld"
	PurchaseStatusExpired            PurchaseRequestStatus = "expired"
	PurchaseStatusExpiredAuth        PurchaseRequestStatus = "expired_auth"
)

// IsTerminal reports whether the status is a terminal state that will not
// transition further without an explicit new purchase_request.
func (s PurchaseRequestStatus) IsTerminal() bool {
	switch s {
	case PurchaseStatusCompleted,
		PurchaseStatusRejectedRefunded,
		PurchaseStatusRejectedUpheld,
		PurchaseStatusDeclined,
		PurchaseStatusCancelled,
		PurchaseStatusExpired,
		PurchaseStatusExpiredAuth:
		return true
	}
	return false
}

// IsActive reports whether the row still needs attention from at least one
// participant. Used by Today aggregation.
func (s PurchaseRequestStatus) IsActive() bool {
	return !s.IsTerminal() && s != PurchaseStatusInspectionAccepted
}

// PurchaseRejectionReason enumerates why the buyer rejected the vehicle.
type PurchaseRejectionReason string

const (
	PurchaseRejectionUndisclosedDamage PurchaseRejectionReason = "undisclosed_damage"
	PurchaseRejectionMechanicalIssues  PurchaseRejectionReason = "mechanical_issues"
	PurchaseRejectionTitleOrPaperwork  PurchaseRejectionReason = "title_or_paperwork"
	PurchaseRejectionVINMismatch       PurchaseRejectionReason = "vin_mismatch"
	PurchaseRejectionNotAsDescribed    PurchaseRejectionReason = "not_as_described"
	PurchaseRejectionNoShow            PurchaseRejectionReason = "no_show"
	PurchaseRejectionOther             PurchaseRejectionReason = "other"
)

// TitleCondition enumerates the seller-declared title brand recorded on the
// Bill of Sale. Authoritative single source of truth (DESIGN SPEC item 20):
// the buyer only acknowledges it during inspection — there is NO second
// title_condition column on the inspection table.
type TitleCondition string

const (
	TitleConditionClean               TitleCondition = "clean"
	TitleConditionLienRecorded        TitleCondition = "lien_recorded"
	TitleConditionSalvage             TitleCondition = "salvage"
	TitleConditionRebuilt             TitleCondition = "rebuilt"
	TitleConditionLemonBuyback        TitleCondition = "lemon_buyback"
	TitleConditionFlood               TitleCondition = "flood"
	TitleConditionManufacturerBuyback TitleCondition = "manufacturer_buyback"
	TitleConditionOther               TitleCondition = "other"
)

// IsValid reports whether the given title condition is a known brand.
func (t TitleCondition) IsValid() bool {
	switch t {
	case TitleConditionClean,
		TitleConditionLienRecorded,
		TitleConditionSalvage,
		TitleConditionRebuilt,
		TitleConditionLemonBuyback,
		TitleConditionFlood,
		TitleConditionManufacturerBuyback,
		TitleConditionOther:
		return true
	}
	return false
}

// IsValid reports whether the given reason string is a known category.
func (r PurchaseRejectionReason) IsValid() bool {
	switch r {
	case PurchaseRejectionUndisclosedDamage,
		PurchaseRejectionMechanicalIssues,
		PurchaseRejectionTitleOrPaperwork,
		PurchaseRejectionVINMismatch,
		PurchaseRejectionNotAsDescribed,
		PurchaseRejectionNoShow,
		PurchaseRejectionOther:
		return true
	}
	return false
}

// PurchaseRejectionStatus tracks admin adjudication state.
type PurchaseRejectionStatus string

const (
	PurchaseRejectionSubmitted   PurchaseRejectionStatus = "submitted"
	PurchaseRejectionUnderReview PurchaseRejectionStatus = "under_review"
	PurchaseRejectionAccepted    PurchaseRejectionStatus = "accepted"
	PurchaseRejectionUpheld      PurchaseRejectionStatus = "upheld"
	PurchaseRejectionWithdrawn   PurchaseRejectionStatus = "withdrawn"
)

// ─── Config defaults ────────────────────────────────────────────────────────

const (
	// PurchaseOfferTTL is how long a `requested` offer stays valid before
	// the lazy-expire scanner flips it to `expired`. DESIGN SPEC §12
	// assumption #9.
	PurchaseOfferTTL = 72 * time.Hour
	// PurchaseInspectionWindow is the buyer's window to accept/reject the
	// vehicle after keys handed over. DESIGN SPEC §12 assumption #8.
	PurchaseInspectionWindow = 48 * time.Hour
	// PurchaseAuthTTL mirrors the ~7-day Stripe manual-capture auth TTL.
	// After this point, we transition to `expired_auth` and the auth is
	// released automatically by Stripe. DESIGN SPEC §12 assumption #2.
	PurchaseAuthTTL = 7 * 24 * time.Hour
	// PurchaseRejectionMinEvidence is the smallest number of evidence files
	// required to submit a rejection.
	PurchaseRejectionMinEvidence = 1
	// PurchaseOfferMinCents is the minimum accepted offer: strictly positive (1 cent).
	PurchaseOfferMinCents int64 = 1
	// PurchaseExplanationMinLen / MaxLen mirror the DB CHECK constraint.
	PurchaseExplanationMinLen = 20
	PurchaseExplanationMaxLen = 2000
	// PurchaseRejectionEvidenceMaxFiles / MaxBytes match the accidents flow.
	PurchaseRejectionEvidenceMaxFiles = 20
	PurchaseRejectionEvidenceMaxBytes = 50 * 1024 * 1024
)

// DefaultBOSTerms is the standard as-is/where-is disclaimer written into
// every freshly-seeded Bill of Sale. Kept as a Go constant (not just the
// migration column default) so that Accept can INSERT it explicitly and
// the wizard's Review step always renders a value — no more silent `—`
// when the row happens to predate the migration default.
const DefaultBOSTerms = "Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing."

// ─── Domain models ──────────────────────────────────────────────────────────

// PurchaseRequest is the root row backing the buy-the-car state machine.
type PurchaseRequest struct {
	ID       uuid.UUID
	CarID    uuid.UUID
	SellerID uuid.UUID
	BuyerID  uuid.UUID
	ChatID   uuid.UUID

	OfferAmountCents int64
	Currency         string
	BuyerMessage     *string

	Status    PurchaseRequestStatus
	ExpiresAt time.Time
	// AuthExpiresAt is set the moment Stripe reports the intent as
	// `requires_capture`. Nil while pre-payment.
	AuthExpiresAt *time.Time

	HandoverLocation     *string
	HandoverLatitude     *float64
	HandoverLongitude    *float64
	HandoverScheduledAt  *time.Time
	KeysHandedOverAt     *time.Time
	InspectionDeadlineAt *time.Time
	InspectionAcceptedAt *time.Time
	CompletedAt          *time.Time

	PaymentIntentID     *string
	PaymentStatus       *PaymentStatus
	RefundStatus        *VehicleReturnRefundStatus
	RefundID            *string
	RefundedAt          *time.Time
	RefundFailureReason *string

	CancellationReason *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// PurchaseBillOfSale is the Vehicle Bill of Sale record for a purchase.
type PurchaseBillOfSale struct {
	ID                uuid.UUID
	PurchaseRequestID uuid.UUID

	VehicleYear  int
	VehicleMake  string
	VehicleModel string
	VIN          string

	SaleAmountCents int64
	Currency        string

	TermsConditions string

	SellerName         string
	SellerAddress      string
	SellerAddressLat   *float64
	SellerAddressLng   *float64
	SellerSignatureURL *string
	SellerSignedAt     *time.Time

	BuyerName         string
	BuyerAddress      string
	BuyerAddressLat   *float64
	BuyerAddressLng   *float64
	BuyerSignatureURL *string
	BuyerSignedAt     *time.Time

	// TitleCondition is the seller-declared title brand (single source of
	// truth). NULL until the seller sets it. TitleConditionOther carries the
	// free-text detail required when TitleCondition == 'other'.
	TitleCondition      *string
	TitleConditionOther *string

	FinalizedPDFURL *string
	FinalizedAt     *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SellerSigned / BuyerSigned are small convenience helpers.
func (b *PurchaseBillOfSale) SellerSigned() bool { return b.SellerSignedAt != nil }
func (b *PurchaseBillOfSale) BuyerSigned() bool  { return b.BuyerSignedAt != nil }
func (b *PurchaseBillOfSale) FullySigned() bool  { return b.SellerSigned() && b.BuyerSigned() }

// PurchaseInspectionChecklist is the buyer-completed, pre-capture safety
// checklist persisted at Accept. One row per purchase (UNIQUE
// purchase_request_id). A NULL row is tolerated for pre-migration in-flight
// purchases.
type PurchaseInspectionChecklist struct {
	ID                                         uuid.UUID
	PurchaseRequestID                          uuid.UUID
	VINMatches                                 bool
	OdometerReviewed                           bool
	ExteriorOK                                 bool
	InteriorOK                                 bool
	MechanicalTestDriveOK                      bool
	TitleReviewed                              bool
	KeysHandedOver                             bool
	BuyerUnderstandsAcceptanceCompletesPayment bool
	CreatedAt                                  time.Time
}

// PurchaseRejection is the buyer's rejection record + admin adjudication.
type PurchaseRejection struct {
	ID                uuid.UUID
	PurchaseRequestID uuid.UUID
	ReasonCategory    PurchaseRejectionReason
	Explanation       string
	Status            PurchaseRejectionStatus
	RefundStatus      *VehicleReturnRefundStatus
	AdminNote         *string
	ResolvedBy        *uuid.UUID
	ResolvedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// PurchaseRejectionEvidence is one file attached to a rejection.
type PurchaseRejectionEvidence struct {
	ID                  uuid.UUID
	PurchaseRejectionID uuid.UUID
	FileURL             string
	FilePath            string
	Filename            string
	MimeType            string
	SizeBytes           int64
	CreatedAt           time.Time
}

// ─── API request bodies ────────────────────────────────────────────────────

// CreatePurchaseRequestBody is the buyer's offer payload.
type CreatePurchaseRequestBody struct {
	OfferAmountCents int64   `json:"offer_amount_cents"`
	BuyerMessage     *string `json:"buyer_message,omitempty"`
}

// DeclinePurchaseBody carries the seller's optional decline reason.
type DeclinePurchaseBody struct {
	Reason *string `json:"reason,omitempty"`
}

// SignBOSBody: {role:"seller"|"buyer", signature_data_url}. The multipart
// alternative (file+role fields) shares the same handler.
type SignBOSBody struct {
	Role             string  `json:"role"`
	SignatureDataURL *string `json:"signature_data_url,omitempty"`
}

// UpdateBOSBody is the seller-side PATCH for BoS "vehicle" and identity fields.
//
// Deliberately DOES NOT include sale_amount_cents. The BoS sale amount is
// seeded from purchase_requests.offer_amount_cents when the BoS row is
// created and MUST remain identical to it — CreatePaymentIntent charges
// off the purchase's offer amount, so any BoS-side drift means the signed
// legal document says one number while the card is charged another.
type UpdateBOSBody struct {
	VehicleYear     *int    `json:"vehicle_year,omitempty"`
	VehicleMake     *string `json:"vehicle_make,omitempty"`
	VehicleModel    *string `json:"vehicle_model,omitempty"`
	VIN             *string `json:"vin,omitempty"`
	TermsConditions *string `json:"terms_conditions,omitempty"`
	SellerName      *string `json:"seller_name,omitempty"`
	SellerAddress   *string `json:"seller_address,omitempty"`
	// Structured geo-coordinates for the seller's address. The address string
	// stays the display value; lat/lng are optional supplements.
	SellerAddressLat *float64 `json:"seller_address_lat,omitempty"`
	SellerAddressLng *float64 `json:"seller_address_lng,omitempty"`
	// Seller-declared title condition (single source of truth). When set to
	// 'other', TitleConditionOther must be non-empty.
	TitleCondition      *string `json:"title_condition,omitempty"`
	TitleConditionOther *string `json:"title_condition_other,omitempty"`
}

// UpdateBOSBuyerFieldsBody is the buyer-owned identity block PATCH.
type UpdateBOSBuyerFieldsBody struct {
	BuyerName    *string `json:"buyer_name,omitempty"`
	BuyerAddress *string `json:"buyer_address,omitempty"`
	// Structured geo-coordinates for the buyer's address (display string stays
	// BuyerAddress).
	BuyerAddressLat *float64 `json:"buyer_address_lat,omitempty"`
	BuyerAddressLng *float64 `json:"buyer_address_lng,omitempty"`
}

// InspectVehicleAcceptBody is the buyer's inspection-checklist payload
// submitted at Accept. All booleans MUST be true and the BoS title_condition
// MUST be set before the sale is completed and payment captured (SAFETY
// CRITICAL — validated BEFORE Stripe capture; DESIGN SPEC item 22).
type InspectVehicleAcceptBody struct {
	VINMatches                                 bool `json:"vin_matches"`
	OdometerReviewed                           bool `json:"odometer_reviewed"`
	ExteriorOK                                 bool `json:"exterior_ok"`
	InteriorOK                                 bool `json:"interior_ok"`
	MechanicalTestDriveOK                      bool `json:"mechanical_test_drive_ok"`
	TitleReviewed                              bool `json:"title_reviewed"`
	KeysHandedOver                             bool `json:"keys_handed_over"`
	BuyerUnderstandsAcceptanceCompletesPayment bool `json:"buyer_understands_acceptance_completes_payment"`
}

// AllConfirmed reports whether every checklist item + the payment-completion
// acknowledgement is affirmatively true.
func (b InspectVehicleAcceptBody) AllConfirmed() bool {
	return b.VINMatches &&
		b.OdometerReviewed &&
		b.ExteriorOK &&
		b.InteriorOK &&
		b.MechanicalTestDriveOK &&
		b.TitleReviewed &&
		b.KeysHandedOver &&
		b.BuyerUnderstandsAcceptanceCompletesPayment
}

// ScheduleHandoverBody is the seller's handover time+location payload.
type ScheduleHandoverBody struct {
	HandoverScheduledAt time.Time `json:"handover_scheduled_at"`
	HandoverLocation    string    `json:"handover_location"`
	HandoverLatitude    *float64  `json:"handover_latitude,omitempty"`
	HandoverLongitude   *float64  `json:"handover_longitude,omitempty"`
}

// SubmitRejectionBody is the buyer's rejection submission.
type SubmitRejectionBody struct {
	ReasonCategory PurchaseRejectionReason `json:"reason_category"`
	Explanation    string                  `json:"explanation"`
	EvidenceIDs    []uuid.UUID             `json:"evidence_ids"`
}

// ResolvePurchaseRejectionBody is the admin adjudication payload.
type ResolvePurchaseRejectionBody struct {
	Resolution string  `json:"resolution"` // "accept" or "uphold"
	Note       *string `json:"note,omitempty"`
}

// ─── API response shapes ───────────────────────────────────────────────────

// PurchaseRequestResponse is the enriched shape returned to buyer/seller/admin.
type PurchaseRequestResponse struct {
	ID       uuid.UUID `json:"id"`
	CarID    uuid.UUID `json:"car_id"`
	CarTitle string    `json:"car_title"`
	ChatID   uuid.UUID `json:"chat_id"`
	SellerID uuid.UUID `json:"seller_id"`
	BuyerID  uuid.UUID `json:"buyer_id"`

	SellerName       string `json:"seller_name"`
	BuyerName        string `json:"buyer_name"`
	ViewerRole       string `json:"viewer_role"` // "seller" | "buyer" | "admin"
	CounterpartyName string `json:"counterparty_name"`

	OfferAmountCents int64   `json:"offer_amount_cents"`
	Currency         string  `json:"currency"`
	BuyerMessage     *string `json:"buyer_message,omitempty"`

	// Structured vehicle snapshot so iOS can render the Review step reliably
	// without re-deriving it from car_title. Sourced from the BoS row (the
	// legal snapshot seeded at Accept), falling back to the live car row when
	// the offer is still pre-accept.
	VehicleYear  int    `json:"vehicle_year"`
	VehicleMake  string `json:"vehicle_make"`
	VehicleModel string `json:"vehicle_model"`
	VehicleVIN   string `json:"vehicle_vin"`

	Status    PurchaseRequestStatus `json:"status"`
	ExpiresAt RFC3339Time           `json:"expires_at"`

	AuthExpiresAt        *RFC3339Time `json:"auth_expires_at,omitempty"`
	HandoverLocation     *string      `json:"handover_location,omitempty"`
	HandoverLatitude     *float64     `json:"handover_latitude,omitempty"`
	HandoverLongitude    *float64     `json:"handover_longitude,omitempty"`
	HandoverScheduledAt  *RFC3339Time `json:"handover_scheduled_at,omitempty"`
	KeysHandedOverAt     *RFC3339Time `json:"keys_handed_over_at,omitempty"`
	InspectionDeadlineAt *RFC3339Time `json:"inspection_deadline_at,omitempty"`
	InspectionAcceptedAt *RFC3339Time `json:"inspection_accepted_at,omitempty"`
	CompletedAt          *RFC3339Time `json:"completed_at,omitempty"`

	PaymentIntentID *string                    `json:"payment_intent_id,omitempty"`
	PaymentStatus   *PaymentStatus             `json:"payment_status,omitempty"`
	RefundStatus    *VehicleReturnRefundStatus `json:"refund_status,omitempty"`
	RefundID        *string                    `json:"refund_id,omitempty"`
	RefundedAt      *RFC3339Time               `json:"refunded_at,omitempty"`

	// Bill-of-sale summary. Nil until the seller accepts the offer.
	BillOfSale *PurchaseBillOfSaleResponse `json:"bill_of_sale,omitempty"`
	Rejection  *PurchaseRejectionResponse  `json:"rejection,omitempty"`

	// InspectionChecklist is the buyer's pre-capture safety checklist. Nil
	// until the buyer accepts the vehicle (or for pre-migration in-flight
	// purchases).
	InspectionChecklist *PurchaseInspectionChecklistResponse `json:"inspection_checklist,omitempty"`

	// AdminDetail is populated only on the admin purchase-detail endpoint
	// (role-gated). Nil on buyer/seller responses.
	AdminDetail *PurchaseAdminDetailResponse `json:"admin_detail,omitempty"`

	CreatedAt RFC3339Time `json:"created_at"`
	UpdatedAt RFC3339Time `json:"updated_at"`
}

// PurchaseInspectionChecklistResponse is the response shape for the buyer's
// pre-capture safety checklist.
type PurchaseInspectionChecklistResponse struct {
	VINMatches                                 bool        `json:"vin_matches"`
	OdometerReviewed                           bool        `json:"odometer_reviewed"`
	ExteriorOK                                 bool        `json:"exterior_ok"`
	InteriorOK                                 bool        `json:"interior_ok"`
	MechanicalTestDriveOK                      bool        `json:"mechanical_test_drive_ok"`
	TitleReviewed                              bool        `json:"title_reviewed"`
	KeysHandedOver                             bool        `json:"keys_handed_over"`
	BuyerUnderstandsAcceptanceCompletesPayment bool        `json:"buyer_understands_acceptance_completes_payment"`
	CreatedAt                                  RFC3339Time `json:"created_at"`
}

// PurchaseAdminDetailResponse is the admin-only enrichment block returned by
// the admin purchase-detail endpoint. Reuses the request signer for every
// private URL (car documents + ID documents are SIGNED; car photos are
// public and pass through).
type PurchaseAdminDetailResponse struct {
	CarMake  string `json:"car_make"`
	CarModel string `json:"car_model"`
	CarYear  int    `json:"car_year"`
	CarVIN   string `json:"car_vin"`

	CoverPhotoURL *string                            `json:"cover_photo_url,omitempty"`
	CarPhotos     []string                           `json:"car_photos"`
	CarDocuments  []PurchaseAdminCarDocumentResponse `json:"car_documents"`

	BuyerEmail    string  `json:"buyer_email"`
	BuyerPhone    *string `json:"buyer_phone,omitempty"`
	BuyerAddress  string  `json:"buyer_address"`
	SellerEmail   string  `json:"seller_email"`
	SellerPhone   *string `json:"seller_phone,omitempty"`
	SellerAddress string  `json:"seller_address"`

	BuyerIDDocumentURL  *string `json:"buyer_id_document_url,omitempty"`
	SellerIDDocumentURL *string `json:"seller_id_document_url,omitempty"`

	InspectionChecklist *PurchaseInspectionChecklistResponse `json:"inspection_checklist,omitempty"`

	RefundFailureReason *string `json:"refund_failure_reason,omitempty"`
	CancellationReason  *string `json:"cancellation_reason,omitempty"`
}

// PurchaseAdminCarDocumentResponse is one car document (title/registration/…)
// with a SIGNED private URL for the admin detail view.
type PurchaseAdminCarDocumentResponse struct {
	DocumentType string `json:"document_type"`
	FileName     string `json:"file_name"`
	FileURL      string `json:"file_url"`
}

// PurchaseBillOfSaleResponse is the response shape for the BoS satellite.
type PurchaseBillOfSaleResponse struct {
	ID                uuid.UUID `json:"id"`
	PurchaseRequestID uuid.UUID `json:"purchase_request_id"`

	VehicleYear  int    `json:"vehicle_year"`
	VehicleMake  string `json:"vehicle_make"`
	VehicleModel string `json:"vehicle_model"`
	VIN          string `json:"vin"`

	SaleAmountCents int64  `json:"sale_amount_cents"`
	Currency        string `json:"currency"`

	TermsConditions string `json:"terms_conditions"`

	SellerName         string       `json:"seller_name"`
	SellerAddress      string       `json:"seller_address"`
	SellerAddressLat   *float64     `json:"seller_address_lat,omitempty"`
	SellerAddressLng   *float64     `json:"seller_address_lng,omitempty"`
	SellerSignatureURL *string      `json:"seller_signature_url,omitempty"`
	SellerSignedAt     *RFC3339Time `json:"seller_signed_at,omitempty"`

	BuyerName         string       `json:"buyer_name"`
	BuyerAddress      string       `json:"buyer_address"`
	BuyerAddressLat   *float64     `json:"buyer_address_lat,omitempty"`
	BuyerAddressLng   *float64     `json:"buyer_address_lng,omitempty"`
	BuyerSignatureURL *string      `json:"buyer_signature_url,omitempty"`
	BuyerSignedAt     *RFC3339Time `json:"buyer_signed_at,omitempty"`

	// Seller-declared title brand (single source of truth). Nil until set.
	TitleCondition      *string `json:"title_condition,omitempty"`
	TitleConditionOther *string `json:"title_condition_other,omitempty"`

	// Party ID documents (driver's license), SIGNED and show-if-on-file. Nil
	// when absent — NEVER a hard requirement (buyers have a license; car-owner
	// sellers may not).
	SellerIDDocumentURL *string `json:"seller_id_document_url,omitempty"`
	BuyerIDDocumentURL  *string `json:"buyer_id_document_url,omitempty"`

	// Vehicle title document, joined from the car's 'title' car_document.
	// TitleDocumentURL is SIGNED (private); TitleUploaded is the presence flag
	// the buyer-Accept gate keys on.
	TitleDocumentURL *string `json:"title_document_url,omitempty"`
	TitleUploaded    bool    `json:"title_uploaded"`

	FinalizedPDFURL *string      `json:"finalized_pdf_url,omitempty"`
	FinalizedAt     *RFC3339Time `json:"finalized_at,omitempty"`
	Locked          bool         `json:"locked"`
	FullySigned     bool         `json:"fully_signed"`

	CreatedAt RFC3339Time `json:"created_at"`
	UpdatedAt RFC3339Time `json:"updated_at"`
}

// PurchaseRejectionResponse is the response shape for a rejection.
type PurchaseRejectionResponse struct {
	ID                uuid.UUID                           `json:"id"`
	PurchaseRequestID uuid.UUID                           `json:"purchase_request_id"`
	ReasonCategory    PurchaseRejectionReason             `json:"reason_category"`
	Explanation       string                              `json:"explanation"`
	Status            PurchaseRejectionStatus             `json:"status"`
	RefundStatus      *VehicleReturnRefundStatus          `json:"refund_status,omitempty"`
	AdminNote         *string                             `json:"admin_note,omitempty"`
	ResolvedBy        *uuid.UUID                          `json:"resolved_by,omitempty"`
	ResolvedAt        *RFC3339Time                        `json:"resolved_at,omitempty"`
	Evidence          []PurchaseRejectionEvidenceResponse `json:"evidence"`
	CreatedAt         RFC3339Time                         `json:"created_at"`
	UpdatedAt         RFC3339Time                         `json:"updated_at"`
}

// PurchaseRejectionEvidenceResponse is one evidence file.
type PurchaseRejectionEvidenceResponse struct {
	ID        uuid.UUID   `json:"id"`
	FileURL   string      `json:"file_url"`
	Filename  string      `json:"filename"`
	MimeType  string      `json:"mime_type"`
	SizeBytes int64       `json:"size_bytes"`
	CreatedAt RFC3339Time `json:"created_at"`
}

// PurchaseRequestsListResponse is returned by list endpoints.
type PurchaseRequestsListResponse struct {
	PurchaseRequests []PurchaseRequestResponse `json:"purchase_requests"`
}

// ─── Error sentinels ────────────────────────────────────────────────────────

const (
	ErrCodeCannotBuyOwnCar           = "CANNOT_BUY_OWN_CAR"
	ErrCodeCarNotForSale             = "CAR_NOT_FOR_SALE"
	ErrCodeCarSold                   = "CAR_SOLD"
	ErrCodeDuplicatePurchase         = "DUPLICATE_ACTIVE_REQUEST"
	ErrCodeInvalidPurchaseAction     = "INVALID_PURCHASE_ACTION"
	ErrCodeBOSLocked                 = "BOS_LOCKED"
	ErrCodeBOSNotSigned              = "BOS_NOT_SIGNED"
	ErrCodeAlreadySigned             = "ALREADY_SIGNED"
	ErrCodeNotAwaitingInspection     = "NOT_AWAITING_INSPECTION"
	ErrCodeNotHandoverScheduled      = "NOT_HANDOVER_SCHEDULED"
	ErrCodePurchaseRequestNotFound   = "PURCHASE_REQUEST_NOT_FOUND"
	ErrCodePurchaseRejectionNotFound = "PURCHASE_REJECTION_NOT_FOUND"
	ErrCodePurchaseNotCancellable    = "NOT_CANCELLABLE"
	ErrCodePurchaseOfferTooLow       = "OFFER_TOO_LOW"
	ErrCodeInvalidRoleField          = "INVALID_ROLE"
	ErrCodePurchaseEvidenceRequired  = "EVIDENCE_REQUIRED"
	// Required-address gate (DESIGN SPEC items 15/16): the signer's own
	// address string must be non-empty before they can sign.
	ErrCodeSellerAddressRequired = "SELLER_ADDRESS_REQUIRED"
	ErrCodeBuyerAddressRequired  = "BUYER_ADDRESS_REQUIRED"
	// Title enforcement at Bill of Sale (DESIGN SPEC item 11): the car must
	// have a 'title' car_document on file before the buyer can Accept.
	ErrCodeTitleRequired = "TITLE_REQUIRED"
	// Title condition validation (DESIGN SPEC item 20).
	ErrCodeInvalidTitleCondition       = "INVALID_TITLE_CONDITION"
	ErrCodeTitleConditionOtherRequired = "TITLE_CONDITION_OTHER_REQUIRED"
	// Inspection checklist gate (DESIGN SPEC item 22, SAFETY CRITICAL).
	ErrCodeInspectionChecklistIncomplete = "INSPECTION_CHECKLIST_INCOMPLETE"
	ErrCodeTitleConditionRequired        = "TITLE_CONDITION_REQUIRED"
)

var (
	ErrCannotBuyOwnCar       = &APIError{Code: ErrCodeCannotBuyOwnCar, Message: "You cannot buy your own car"}
	ErrCarNotForSale         = &APIError{Code: ErrCodeCarNotForSale, Message: "This car is not currently listed for sale"}
	ErrCarSold               = &APIError{Code: ErrCodeCarSold, Message: "This car has already been sold or is reserved for another purchase"}
	ErrDuplicatePurchase     = &APIError{Code: ErrCodeDuplicatePurchase, Message: "You already have an active purchase offer for this car"}
	ErrInvalidPurchaseAction = &APIError{Code: ErrCodeInvalidPurchaseAction, Message: "This action is not allowed for the current purchase state"}
	// ErrBOSSelfLocked is returned by PATCH /bos and /bos/buyer-fields when
	// the current caller's role has already signed. iOS shows this to the
	// signer to explain that unsigning is a support-only operation.
	ErrBOSSelfLocked = &APIError{Code: ErrCodeBOSLocked, Message: "You have already signed the Bill of Sale. Unsigning is not supported — contact support to reset."}
	// ErrBOSOtherRoleLocked is currently unused by the two PATCH endpoints
	// (buyer signature no longer blocks seller edits, and vice versa) but
	// is retained as a typed sentinel for future flows (e.g. an admin
	// re-open) so callers have a canonical string to key on.
	ErrBOSOtherRoleLocked = &APIError{Code: ErrCodeBOSLocked, Message: "The other party has signed. This section is now read-only."}
	// ErrBOSLocked is preserved as a compatibility alias so any callers or
	// tests that reference the old sentinel keep compiling. Deprecated —
	// new code should return ErrBOSSelfLocked or ErrBOSOtherRoleLocked.
	ErrBOSLocked                     = ErrBOSSelfLocked
	ErrBOSNotSigned                  = &APIError{Code: ErrCodeBOSNotSigned, Message: "The Bill of Sale must be fully signed before payment can be authorized"}
	ErrAlreadySigned                 = &APIError{Code: ErrCodeAlreadySigned, Message: "You have already signed this Bill of Sale"}
	ErrNotAwaitingInspection         = &APIError{Code: ErrCodeNotAwaitingInspection, Message: "The vehicle is not currently awaiting buyer inspection"}
	ErrNotHandoverScheduled          = &APIError{Code: ErrCodeNotHandoverScheduled, Message: "A handover has not been scheduled yet"}
	ErrPurchaseRequestNotFound       = &APIError{Code: ErrCodePurchaseRequestNotFound, Message: "Purchase request not found"}
	ErrPurchaseRejectionNotFound     = &APIError{Code: ErrCodePurchaseRejectionNotFound, Message: "Rejection record not found"}
	ErrPurchaseNotCancellable        = &APIError{Code: ErrCodePurchaseNotCancellable, Message: "This offer can no longer be cancelled"}
	ErrPurchaseOfferTooLow           = &APIError{Code: ErrCodePurchaseOfferTooLow, Message: "Offer must be greater than $0"}
	ErrInvalidRoleField              = &APIError{Code: ErrCodeInvalidRoleField, Message: "Role must be 'seller' or 'buyer' and match the caller's identity"}
	ErrPurchaseEvidenceRequired      = &APIError{Code: ErrCodePurchaseEvidenceRequired, Message: "At least one piece of evidence is required to reject the vehicle"}
	ErrSellerAddressRequired         = &APIError{Code: ErrCodeSellerAddressRequired, Message: "Enter the seller address on the Bill of Sale before signing"}
	ErrBuyerAddressRequired          = &APIError{Code: ErrCodeBuyerAddressRequired, Message: "Enter the buyer address on the Bill of Sale before signing"}
	ErrTitleRequired                 = &APIError{Code: ErrCodeTitleRequired, Message: "The seller must upload the vehicle title before the sale can be completed"}
	ErrInvalidTitleCondition         = &APIError{Code: ErrCodeInvalidTitleCondition, Message: "Invalid title condition"}
	ErrTitleConditionOtherRequired   = &APIError{Code: ErrCodeTitleConditionOtherRequired, Message: "Describe the title condition when selecting 'other'"}
	ErrInspectionChecklistIncomplete = &APIError{Code: ErrCodeInspectionChecklistIncomplete, Message: "All inspection checklist items must be confirmed before accepting the vehicle"}
	ErrTitleConditionRequired        = &APIError{Code: ErrCodeTitleConditionRequired, Message: "The seller must declare the title condition before the sale can be completed"}
)

// ─── Notification types (extension of existing enum) ────────────────────────

const (
	NotificationTypePurchaseRequest   NotificationType = "purchase_request"
	NotificationTypePurchasePayment   NotificationType = "purchase_payment"
	NotificationTypePurchaseHandover  NotificationType = "purchase_handover"
	NotificationTypePurchaseRejection NotificationType = "purchase_rejection"
)
