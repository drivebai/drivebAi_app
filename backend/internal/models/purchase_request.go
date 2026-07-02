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
	// PurchaseOfferMinCents mirrors the $1,000 floor from CreateListing.
	PurchaseOfferMinCents int64 = 100000
	// PurchaseExplanationMinLen / MaxLen mirror the DB CHECK constraint.
	PurchaseExplanationMinLen = 20
	PurchaseExplanationMaxLen = 2000
	// PurchaseRejectionEvidenceMaxFiles / MaxBytes match the accidents flow.
	PurchaseRejectionEvidenceMaxFiles = 20
	PurchaseRejectionEvidenceMaxBytes = 50 * 1024 * 1024
)

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

// PurchaseBillOfSale is the MV-912-shaped record for a purchase.
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
	SellerSignatureURL *string
	SellerSignedAt     *time.Time

	BuyerName         string
	BuyerAddress      string
	BuyerSignatureURL *string
	BuyerSignedAt     *time.Time

	FinalizedPDFURL *string
	FinalizedAt     *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SellerSigned / BuyerSigned are small convenience helpers.
func (b *PurchaseBillOfSale) SellerSigned() bool { return b.SellerSignedAt != nil }
func (b *PurchaseBillOfSale) BuyerSigned() bool  { return b.BuyerSignedAt != nil }
func (b *PurchaseBillOfSale) FullySigned() bool  { return b.SellerSigned() && b.BuyerSigned() }

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
}

// UpdateBOSBuyerFieldsBody is the buyer-owned identity block PATCH.
type UpdateBOSBuyerFieldsBody struct {
	BuyerName    *string `json:"buyer_name,omitempty"`
	BuyerAddress *string `json:"buyer_address,omitempty"`
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

	CreatedAt RFC3339Time `json:"created_at"`
	UpdatedAt RFC3339Time `json:"updated_at"`
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
	SellerSignatureURL *string      `json:"seller_signature_url,omitempty"`
	SellerSignedAt     *RFC3339Time `json:"seller_signed_at,omitempty"`

	BuyerName         string       `json:"buyer_name"`
	BuyerAddress      string       `json:"buyer_address"`
	BuyerSignatureURL *string      `json:"buyer_signature_url,omitempty"`
	BuyerSignedAt     *RFC3339Time `json:"buyer_signed_at,omitempty"`

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
	ErrCodeCannotBuyOwnCar        = "CANNOT_BUY_OWN_CAR"
	ErrCodeCarNotForSale          = "CAR_NOT_FOR_SALE"
	ErrCodeCarSold                = "CAR_SOLD"
	ErrCodeDuplicatePurchase      = "DUPLICATE_ACTIVE_REQUEST"
	ErrCodeInvalidPurchaseAction  = "INVALID_PURCHASE_ACTION"
	ErrCodeBOSLocked              = "BOS_LOCKED"
	ErrCodeBOSNotSigned           = "BOS_NOT_SIGNED"
	ErrCodeAlreadySigned          = "ALREADY_SIGNED"
	ErrCodeNotAwaitingInspection  = "NOT_AWAITING_INSPECTION"
	ErrCodeNotHandoverScheduled   = "NOT_HANDOVER_SCHEDULED"
	ErrCodePurchaseRequestNotFound = "PURCHASE_REQUEST_NOT_FOUND"
	ErrCodePurchaseRejectionNotFound = "PURCHASE_REJECTION_NOT_FOUND"
	ErrCodePurchaseNotCancellable  = "NOT_CANCELLABLE"
	ErrCodePurchaseOfferTooLow    = "OFFER_TOO_LOW"
	ErrCodeInvalidRoleField       = "INVALID_ROLE"
	ErrCodePurchaseEvidenceRequired = "EVIDENCE_REQUIRED"
)

var (
	ErrCannotBuyOwnCar        = &APIError{Code: ErrCodeCannotBuyOwnCar, Message: "You cannot buy your own car"}
	ErrCarNotForSale          = &APIError{Code: ErrCodeCarNotForSale, Message: "This car is not currently listed for sale"}
	ErrCarSold                = &APIError{Code: ErrCodeCarSold, Message: "This car has already been sold or is reserved for another purchase"}
	ErrDuplicatePurchase      = &APIError{Code: ErrCodeDuplicatePurchase, Message: "You already have an active purchase offer for this car"}
	ErrInvalidPurchaseAction  = &APIError{Code: ErrCodeInvalidPurchaseAction, Message: "This action is not allowed for the current purchase state"}
	ErrBOSLocked              = &APIError{Code: ErrCodeBOSLocked, Message: "Bill of Sale is locked — one or more parties have already signed"}
	ErrBOSNotSigned           = &APIError{Code: ErrCodeBOSNotSigned, Message: "The Bill of Sale must be fully signed before payment can be authorized"}
	ErrAlreadySigned          = &APIError{Code: ErrCodeAlreadySigned, Message: "You have already signed this Bill of Sale"}
	ErrNotAwaitingInspection  = &APIError{Code: ErrCodeNotAwaitingInspection, Message: "The vehicle is not currently awaiting buyer inspection"}
	ErrNotHandoverScheduled   = &APIError{Code: ErrCodeNotHandoverScheduled, Message: "A handover has not been scheduled yet"}
	ErrPurchaseRequestNotFound = &APIError{Code: ErrCodePurchaseRequestNotFound, Message: "Purchase request not found"}
	ErrPurchaseRejectionNotFound = &APIError{Code: ErrCodePurchaseRejectionNotFound, Message: "Rejection record not found"}
	ErrPurchaseNotCancellable = &APIError{Code: ErrCodePurchaseNotCancellable, Message: "This offer can no longer be cancelled"}
	ErrPurchaseOfferTooLow    = &APIError{Code: ErrCodePurchaseOfferTooLow, Message: "Offer must be at least $1,000"}
	ErrInvalidRoleField       = &APIError{Code: ErrCodeInvalidRoleField, Message: "Role must be 'seller' or 'buyer' and match the caller's identity"}
	ErrPurchaseEvidenceRequired = &APIError{Code: ErrCodePurchaseEvidenceRequired, Message: "At least one piece of evidence is required to reject the vehicle"}
)

// ─── Notification types (extension of existing enum) ────────────────────────

const (
	NotificationTypePurchaseRequest   NotificationType = "purchase_request"
	NotificationTypePurchasePayment   NotificationType = "purchase_payment"
	NotificationTypePurchaseHandover  NotificationType = "purchase_handover"
	NotificationTypePurchaseRejection NotificationType = "purchase_rejection"
)
