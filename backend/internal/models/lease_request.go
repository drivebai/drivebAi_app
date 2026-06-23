package models

import (
	"time"

	"github.com/google/uuid"
)

// LeaseRequestStatus represents the lifecycle of a lease request
type LeaseRequestStatus string

const (
	LeaseStatusRequested      LeaseRequestStatus = "requested"
	LeaseStatusAccepted       LeaseRequestStatus = "accepted"
	LeaseStatusDeclined       LeaseRequestStatus = "declined"
	LeaseStatusCancelled      LeaseRequestStatus = "cancelled"
	LeaseStatusPaymentPending LeaseRequestStatus = "payment_pending"
	LeaseStatusPaid           LeaseRequestStatus = "paid"
	LeaseStatusExpired        LeaseRequestStatus = "expired"
	// LeaseStatusExpiredRefunded: driver did not confirm pickup within the
	// PICKUP_DEADLINE_MINUTES window after payment. Payment is refunded via
	// Stripe and the listing returns to discovery. Terminal.
	LeaseStatusExpiredRefunded LeaseRequestStatus = "expired_refunded"
)

// PickupMaxExtensionMinutes is the hard cap on the total minutes an owner can
// add to a single lease's pickup deadline. Also enforced by a DB CHECK
// constraint (migration 000025) as defence-in-depth.
const PickupMaxExtensionMinutes = 120

// AllowedPickupExtensionMinutes enumerates the preset increments the owner
// may pick. The handler rejects anything outside this set so the iOS preset
// buttons remain the source of truth.
var AllowedPickupExtensionMinutes = []int{15, 30, 60}

// IsAllowedPickupExtensionMinutes reports whether `m` is one of the presets.
func IsAllowedPickupExtensionMinutes(m int) bool {
	for _, v := range AllowedPickupExtensionMinutes {
		if v == m {
			return true
		}
	}
	return false
}

// RefundStatus tracks the Stripe refund lifecycle for an expired pickup.
type RefundStatus string

const (
	RefundStatusPending   RefundStatus = "pending"   // Stripe call about to be made
	RefundStatusSucceeded RefundStatus = "succeeded" // Stripe Refund created and accepted
	RefundStatusFailed    RefundStatus = "failed"    // Stripe rejected the refund; needs human intervention
)

// PaymentStatus mirrors Stripe PaymentIntent statuses
type PaymentStatus string

const (
	PaymentStatusRequiresPaymentMethod PaymentStatus = "requires_payment_method"
	PaymentStatusRequiresConfirmation  PaymentStatus = "requires_confirmation"
	PaymentStatusProcessing            PaymentStatus = "processing"
	PaymentStatusSucceeded             PaymentStatus = "succeeded"
	PaymentStatusCanceled              PaymentStatus = "canceled"
	PaymentStatusFailed                PaymentStatus = "failed"
)

// LeaseRequest represents a driver's request to lease a car listing
type LeaseRequest struct {
	ID                    uuid.UUID          `json:"id"`
	ChatID                uuid.UUID          `json:"chat_id"`
	ListingID             uuid.UUID          `json:"listing_id"`
	OwnerID               uuid.UUID          `json:"owner_id"`
	DriverID              uuid.UUID          `json:"driver_id"`
	Status                LeaseRequestStatus `json:"status"`
	WeeklyPrice           float64            `json:"weekly_price"`
	OfferedWeeklyPrice    *float64           `json:"offered_weekly_price,omitempty"`
	OfferedPriceUpdatedAt *time.Time         `json:"offered_price_updated_at,omitempty"`
	Currency              string             `json:"currency"`
	Weeks                 int                `json:"weeks"`
	Message               *string            `json:"message,omitempty"`
	ExpiresAt             time.Time          `json:"expires_at"`
	// Pickup deadline (added in migration 000024): set when payment succeeds,
	// cleared by ConfirmPickup or by the expiry scanner.
	PickupDeadlineAt  *time.Time `json:"pickup_deadline_at,omitempty"`
	PickupConfirmedAt *time.Time `json:"pickup_confirmed_at,omitempty"`
	// Refund tracking (populated by the expiry scanner when status moves to
	// expired_refunded). Stripe Refund ID is kept for idempotency on retries.
	RefundID     *string    `json:"refund_id,omitempty"`
	RefundedAt   *time.Time `json:"refunded_at,omitempty"`
	RefundStatus *string    `json:"refund_status,omitempty"`
	// Owner-initiated pickup extension (migration 000025). Total minutes
	// added across all extensions is capped at PickupMaxExtensionMinutes.
	PickupExtensionTotalMinutes int        `json:"pickup_extension_total_minutes"`
	PickupExtensionCount        int        `json:"pickup_extension_count"`
	PickupLastExtendedAt        *time.Time `json:"pickup_last_extended_at,omitempty"`
	// Owner-changed-price review (migration 000028). When the owner adjusts
	// `OfferedWeeklyPrice` mid-flow (before payment has actually succeeded)
	// we set PriceChangePending=true and snapshot the prior price into
	// PreviousOfferedWeeklyPrice so the driver UI can render an
	// old-vs-new comparison. The driver must explicitly accept or decline
	// before payment is allowed again — `IsPayable` enforces the gate.
	// PriceChangeActedAt records when the driver's decision landed
	// (useful for audit + idempotency).
	PriceChangePending         bool       `json:"price_change_pending"`
	PreviousOfferedWeeklyPrice *float64   `json:"previous_offered_weekly_price,omitempty"`
	PriceChangeActedAt         *time.Time `json:"price_change_acted_at,omitempty"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

// IsPayable reports whether the driver is allowed to initiate a payment
// for this lease right now. Excludes everything that isn't `accepted` or
// `payment_pending` and additionally refuses while a price change is
// still awaiting the driver's accept/decline decision — paying through a
// stale offered price would silently lock in an amount the driver never
// agreed to.
func (lr *LeaseRequest) IsPayable() bool {
	if lr.PriceChangePending {
		return false
	}
	return lr.Status == LeaseStatusAccepted || lr.Status == LeaseStatusPaymentPending
}

// RemainingExtensionMinutes returns how many more minutes can still be added
// by the owner before the cap is reached.
func (lr *LeaseRequest) RemainingExtensionMinutes() int {
	r := PickupMaxExtensionMinutes - lr.PickupExtensionTotalMinutes
	if r < 0 {
		return 0
	}
	return r
}

// TotalAmountCents returns the total amount in smallest currency unit (cents),
// using offered_weekly_price when set by the owner.
func (lr *LeaseRequest) TotalAmountCents() int64 {
	price := lr.WeeklyPrice
	if lr.OfferedWeeklyPrice != nil {
		price = *lr.OfferedWeeklyPrice
	}
	return int64(price * float64(lr.Weeks) * 100)
}

// Payment represents a Stripe payment linked to a lease request
type Payment struct {
	ID                uuid.UUID     `json:"id"`
	LeaseRequestID    uuid.UUID     `json:"lease_request_id"`
	Provider          string        `json:"provider"`
	StripeCustomerID  *string       `json:"stripe_customer_id,omitempty"`
	PaymentIntentID   *string       `json:"payment_intent_id,omitempty"`
	ClientSecret      *string       `json:"-"`      // never serialized; sent to client via PaymentIntentResponse only
	Amount            int64         `json:"amount"` // in cents
	Currency          string        `json:"currency"`
	PlatformFeeAmount int64         `json:"platform_fee_amount"` // in cents
	Status            PaymentStatus `json:"status"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

// Error codes for lease request operations
const (
	ErrCodeLeaseRequestNotFound = "LEASE_REQUEST_NOT_FOUND"
	ErrCodeDuplicateLeaseReq    = "DUPLICATE_LEASE_REQUEST"
	ErrCodeCannotLeaseOwnCar    = "CANNOT_LEASE_OWN_CAR"
	ErrCodeCarNotForRent        = "CAR_NOT_FOR_RENT"
	ErrCodePaymentNotFound      = "PAYMENT_NOT_FOUND"
	ErrCodePaymentAlreadyExists = "PAYMENT_ALREADY_EXISTS"
	ErrCodeInvalidLeaseAction   = "INVALID_LEASE_ACTION"
	ErrCodePriceLocked          = "PRICE_LOCKED"
	// ErrCodePriceReviewPending: caller tried to pay (or otherwise progress
	// the lease) while the driver still needs to accept/decline a price
	// change the owner just made.
	ErrCodePriceReviewPending = "PRICE_REVIEW_PENDING"
	// ErrCodeNoPriceChangePending: caller hit /accept-price or
	// /decline-price on a lease that has no pending price change. Could be
	// a stale client view or a double-tap after the other side acted.
	ErrCodeNoPriceChangePending = "NO_PRICE_CHANGE_PENDING"
)

var (
	ErrLeaseRequestNotFound = &APIError{Code: ErrCodeLeaseRequestNotFound, Message: "Lease request not found"}
	ErrDuplicateLeaseReq    = &APIError{Code: ErrCodeDuplicateLeaseReq, Message: "You already have an active lease request for this listing"}
	ErrCannotLeaseOwnCar    = &APIError{Code: ErrCodeCannotLeaseOwnCar, Message: "You cannot request a lease on your own car"}
	ErrCarNotForRent        = &APIError{Code: ErrCodeCarNotForRent, Message: "This car is not available for rent"}
	ErrPaymentNotFound      = &APIError{Code: ErrCodePaymentNotFound, Message: "Payment not found"}
	ErrPaymentAlreadyExists = &APIError{Code: ErrCodePaymentAlreadyExists, Message: "Payment already exists for this lease request"}
	ErrInvalidLeaseAction   = &APIError{Code: ErrCodeInvalidLeaseAction, Message: "Invalid action for the current lease request status"}
	ErrPriceLocked          = &APIError{Code: ErrCodePriceLocked, Message: "Price can no longer be adjusted — payment has already succeeded."}
	ErrPriceReviewPending   = &APIError{Code: ErrCodePriceReviewPending, Message: "The owner updated the price. Accept or decline the new offer before continuing."}
	ErrNoPriceChangePending = &APIError{Code: ErrCodeNoPriceChangePending, Message: "There is no pending price change on this request."}
	ErrPickupDeadlinePassed = &APIError{Code: "PICKUP_DEADLINE_PASSED", Message: "The pickup deadline has already passed; the rental was refunded."}
	ErrPickupExtensionCap   = &APIError{Code: "PICKUP_EXTENSION_CAP_REACHED", Message: "Pickup deadline can't be extended further; the cap has been reached."}
	ErrInvalidExtensionMin  = &APIError{Code: "INVALID_EXTENSION_MINUTES", Message: "Pickup extension must be 15, 30, or 60 minutes."}
)

// --- API request types ---

type CreateLeaseRequestBody struct {
	Weeks   *int    `json:"weeks,omitempty"`
	Message *string `json:"message,omitempty"`
}

type UpdateOfferedPriceBody struct {
	OfferedWeeklyPrice float64 `json:"offered_weekly_price"`
}

// ExtendPickupDeadlineBody is the payload for
// POST /api/v1/lease-requests/{id}/pickup-deadline/extend.
type ExtendPickupDeadlineBody struct {
	Minutes int `json:"minutes"`
}

// --- API response types ---

type LeaseRequestResponse struct {
	ID                 uuid.UUID          `json:"id"`
	ChatID             uuid.UUID          `json:"chat_id"`
	ListingID          uuid.UUID          `json:"listing_id"`
	OwnerID            uuid.UUID          `json:"owner_id"`
	DriverID           uuid.UUID          `json:"driver_id"`
	DriverName         string             `json:"driver_name"`
	OwnerName          string             `json:"owner_name"`
	Status             LeaseRequestStatus `json:"status"`
	WeeklyPrice        float64            `json:"weekly_price"`
	OfferedWeeklyPrice *float64           `json:"offered_weekly_price,omitempty"`
	TotalAmount        float64            `json:"total_amount"`
	Currency           string             `json:"currency"`
	Weeks              int                `json:"weeks"`
	Message            *string            `json:"message,omitempty"`
	CarTitle           string             `json:"car_title"`
	Payment            *PaymentSummary    `json:"payment,omitempty"`
	ExpiresAt          RFC3339Time        `json:"expires_at"`
	// New pickup-deadline fields (migration 000024). Present iff the lease
	// has reached the paid state and the driver hasn't confirmed yet.
	PickupDeadlineAt  *RFC3339Time `json:"pickup_deadline_at,omitempty"`
	PickupConfirmedAt *RFC3339Time `json:"pickup_confirmed_at,omitempty"`
	RefundID          *string      `json:"refund_id,omitempty"`
	RefundedAt        *RFC3339Time `json:"refunded_at,omitempty"`
	RefundStatus      *string      `json:"refund_status,omitempty"`
	// Extension tracking (migration 000025). Surfaced to both client roles
	// so the owner UI can disable the "Add more time" button once the cap
	// is hit, and the driver UI can show "extended by 30 min" toasts.
	PickupExtensionTotalMinutes int          `json:"pickup_extension_total_minutes"`
	PickupExtensionCount        int          `json:"pickup_extension_count"`
	PickupExtensionRemainingMin int          `json:"pickup_extension_remaining_minutes"`
	PickupLastExtendedAt        *RFC3339Time `json:"pickup_last_extended_at,omitempty"`
	// Price-review (migration 000028). PriceChangePending drives the
	// driver-side accept/decline gate on iOS — Pay Now is hidden while
	// it's true. PreviousOfferedWeeklyPrice gives the UI an "old → new"
	// comparison without the client having to remember stale values.
	PriceChangePending         bool         `json:"price_change_pending"`
	PreviousOfferedWeeklyPrice *float64     `json:"previous_offered_weekly_price,omitempty"`
	PriceChangeActedAt         *RFC3339Time `json:"price_change_acted_at,omitempty"`
	CreatedAt                  RFC3339Time  `json:"created_at"`
	UpdatedAt                  RFC3339Time  `json:"updated_at"`
}

type PaymentSummary struct {
	ID                uuid.UUID     `json:"id"`
	PaymentIntentID   *string       `json:"payment_intent_id,omitempty"`
	Amount            int64         `json:"amount"`
	PlatformFeeAmount int64         `json:"platform_fee_amount"`
	Currency          string        `json:"currency"`
	Status            PaymentStatus `json:"status"`
}

type LeaseRequestsListResponse struct {
	LeaseRequests []LeaseRequestResponse `json:"lease_requests"`
}

type CreateLeaseRequestResponse struct {
	ChatID       uuid.UUID            `json:"chat_id"`
	LeaseRequest LeaseRequestResponse `json:"lease_request"`
}

type PaymentIntentResponse struct {
	PaymentIntentClientSecret string `json:"payment_intent_client_secret"`
	PaymentIntentID           string `json:"payment_intent_id"`
	PublishableKey            string `json:"publishable_key"`
	CustomerID                string `json:"customer_id,omitempty"`
	EphemeralKeySecret        string `json:"ephemeral_key_secret,omitempty"`
	Amount                    int64  `json:"amount"`
	Currency                  string `json:"currency"`
}
