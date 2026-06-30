package models

import (
	"math"
	"time"

	"github.com/google/uuid"
)

// VehicleReturnStatus tracks the two-party return handshake.
type VehicleReturnStatus string

const (
	// VehicleReturnDriverInitiated: driver tapped "I returned the car";
	// owner must confirm. Cancellable by the driver within
	// VehicleReturnDriverCancelWindow.
	VehicleReturnDriverInitiated VehicleReturnStatus = "driver_initiated"
	// VehicleReturnOwnerConfirmed: owner confirmed receipt. Transient —
	// the same handler immediately attempts the Stripe refund and flips
	// the row to `completed`. Stays here only if the Stripe call fails
	// so the stuck-refund scanner can retry.
	VehicleReturnOwnerConfirmed VehicleReturnStatus = "owner_confirmed"
	// VehicleReturnDisputed: owner rejected the return. Held for admin
	// resolution.
	VehicleReturnDisputed VehicleReturnStatus = "disputed"
	// VehicleReturnCompleted: refund issued (or none owed). Car released
	// back to discovery. Terminal.
	VehicleReturnCompleted VehicleReturnStatus = "completed"
	// VehicleReturnCancelled: driver self-cancel within the cancel window
	// or admin reject of a disputed return. Terminal.
	VehicleReturnCancelled VehicleReturnStatus = "cancelled"
)

// VehicleReturnRefundStatus mirrors models.RefundStatus but adds
// `not_applicable` for zero-refund cases (full term used / $0 promo lease).
type VehicleReturnRefundStatus string

const (
	VehicleReturnRefundPending       VehicleReturnRefundStatus = "pending"
	VehicleReturnRefundSucceeded     VehicleReturnRefundStatus = "succeeded"
	VehicleReturnRefundFailed        VehicleReturnRefundStatus = "failed"
	VehicleReturnRefundNotApplicable VehicleReturnRefundStatus = "not_applicable"
)

// VehicleReturnDriverCancelWindow is how long the driver has to undo their
// "I returned the car" submission. Mirrors KeyHandoverConfirmWindow's
// shape — a single const so tests and future tuning have one knob.
const VehicleReturnDriverCancelWindow = 5 * time.Minute

// VehicleReturn is the DB-backed return record. Exactly one per lease
// (enforced by UNIQUE(lease_request_id) in migration 000030).
type VehicleReturn struct {
	ID             uuid.UUID
	LeaseRequestID uuid.UUID
	CarID          uuid.UUID
	OwnerID        uuid.UUID
	DriverID       uuid.UUID

	Status VehicleReturnStatus

	DriverInitiatedAt time.Time
	OwnerConfirmedAt  *time.Time
	DisputedAt        *time.Time
	CompletedAt       *time.Time
	CancelledAt       *time.Time

	PickupConfirmedAt time.Time
	ReturnedAt        time.Time
	RentalWeeks       int
	PaidAmountCents   int64

	UsedDays            int
	RefundAmountCents   int64
	RefundID            *string
	RefundStatus        *VehicleReturnRefundStatus
	RefundedAt          *time.Time
	RefundFailureReason *string

	DisputeReason     *string
	DisputeResolvedBy *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsActive reports whether the return is still in an actionable (Today) state.
// Completed/cancelled rows are terminal and surface only as history.
func (v *VehicleReturn) IsActive() bool {
	return v.Status == VehicleReturnDriverInitiated ||
		v.Status == VehicleReturnOwnerConfirmed ||
		v.Status == VehicleReturnDisputed
}

// CancelWindowExpiresAt returns the deadline by which the driver must call
// /cancel to undo their submission. Returns zero time if the return is
// past driver_initiated (cancellation is no longer permitted).
func (v *VehicleReturn) CancelWindowExpiresAt() time.Time {
	if v.Status != VehicleReturnDriverInitiated {
		return time.Time{}
	}
	return v.DriverInitiatedAt.Add(VehicleReturnDriverCancelWindow)
}

// ─── Refund formula ─────────────────────────────────────────────────────────

// RefundComputation captures the inputs + outputs of the partial-refund
// formula so handlers can persist and surface them without re-deriving.
type RefundComputation struct {
	UsedDays          int
	PerDayCents       int64
	RefundAmountCents int64
	// NotApplicable is true when there's nothing to refund at Stripe — either
	// paid_amount_cents was 0 (promo / free lease) or the computed refund
	// rounds below 1¢. Caller should skip the Stripe call and persist
	// refund_status = 'not_applicable'.
	NotApplicable bool
}

// ComputeReturnRefund implements the spec's partial-refund formula.
//
//	per_day_cents       = paid_amount_cents / total_paid_days
//	elapsed_seconds     = max(0, returnedAt - pickupConfirmedAt)
//	used_days           = ceil(elapsed_seconds / 86400), capped at total_paid_days,
//	                      floored at 1 day (no $0 same-minute returns).
//	refund_amount_cents = paid_amount_cents - per_day_cents * used_days
//
// Edge cases the formula absorbs (see migration 000030 + spec §3):
//   - rental_weeks == 0 → treated as 1 (defensive; lease handler enforces ≥1).
//   - paid_amount_cents == 0 → NotApplicable=true; UsedDays still reported.
//   - returnedAt < pickupConfirmedAt (clock skew) → elapsed clamped to 0.
//   - returnedAt beyond paid window → used_days capped at total_paid_days.
//   - refund < 1¢ → NotApplicable=true (Stripe rejects sub-cent refunds).
//   - refund > paid_amount_cents → impossible by formula, but min-guarded.
func ComputeReturnRefund(paidAmountCents int64, rentalWeeks int, pickupConfirmedAt, returnedAt time.Time) RefundComputation {
	if rentalWeeks <= 0 {
		rentalWeeks = 1
	}
	totalPaidDays := rentalWeeks * 7

	elapsedSeconds := returnedAt.Sub(pickupConfirmedAt).Seconds()
	if elapsedSeconds < 0 {
		elapsedSeconds = 0
	}
	usedDays := int(math.Ceil(elapsedSeconds / 86400.0))
	if usedDays > totalPaidDays {
		usedDays = totalPaidDays
	}
	if usedDays < 1 {
		usedDays = 1
	}

	if paidAmountCents <= 0 {
		return RefundComputation{
			UsedDays:          usedDays,
			PerDayCents:       0,
			RefundAmountCents: 0,
			NotApplicable:     true,
		}
	}

	perDayCents := paidAmountCents / int64(totalPaidDays)
	refundCents := paidAmountCents - perDayCents*int64(usedDays)
	if refundCents < 0 {
		refundCents = 0
	}
	if refundCents > paidAmountCents {
		refundCents = paidAmountCents
	}

	notApplicable := refundCents < 1
	return RefundComputation{
		UsedDays:          usedDays,
		PerDayCents:       perDayCents,
		RefundAmountCents: refundCents,
		NotApplicable:     notApplicable,
	}
}

// ─── Error sentinels ────────────────────────────────────────────────────────

const (
	ErrCodeVehicleReturnNotFound   = "VEHICLE_RETURN_NOT_FOUND"
	ErrCodeReturnNotAllowed        = "RETURN_NOT_ALLOWED"
	ErrCodeReturnAlreadyExists     = "RETURN_ALREADY_EXISTS"
	ErrCodeInvalidReturnState      = "INVALID_RETURN_STATE"
	ErrCodeReturnCancelExpired     = "CANCEL_WINDOW_EXPIRED"
	ErrCodeNotLeaseDriver          = "NOT_LEASE_DRIVER"
	ErrCodeReturnDisputeReasonReqd = "DISPUTE_REASON_REQUIRED"
)

var (
	ErrVehicleReturnNotFound = &APIError{Code: ErrCodeVehicleReturnNotFound, Message: "Vehicle return not found"}
	ErrReturnNotAllowed      = &APIError{Code: ErrCodeReturnNotAllowed, Message: "This rental cannot be returned right now"}
	ErrReturnAlreadyExists   = &APIError{Code: ErrCodeReturnAlreadyExists, Message: "A return is already in progress for this rental"}
	ErrInvalidReturnState    = &APIError{Code: ErrCodeInvalidReturnState, Message: "Invalid action for the current return status"}
	ErrReturnCancelExpired   = &APIError{Code: ErrCodeReturnCancelExpired, Message: "The undo window for this return has expired"}
	ErrNotLeaseDriver        = &APIError{Code: ErrCodeNotLeaseDriver, Message: "Only the driver of this rental can perform this action"}
	ErrDisputeReasonRequired = &APIError{Code: ErrCodeReturnDisputeReasonReqd, Message: "A dispute reason is required (5–500 characters)"}
)

// ─── Request / response shapes ──────────────────────────────────────────────

// DisputeVehicleReturnBody is the JSON payload for POST
// /api/v1/vehicle-returns/{id}/dispute.
type DisputeVehicleReturnBody struct {
	Reason string `json:"reason"`
}

// ResolveVehicleReturnBody is the admin payload for POST
// /api/v1/admin/vehicle-returns/{id}/resolve.
type ResolveVehicleReturnBody struct {
	Resolution string `json:"resolution"` // "accept" | "reject"
	Note       string `json:"note,omitempty"`
}

// VehicleReturnResponse is the per-viewer API shape, mirroring
// KeyHandoverResponse's structure: enriched with the participant names,
// car title, chat id, and a precomputed viewer_role so iOS doesn't need
// to compare UUIDs locally to know which CTA to render.
type VehicleReturnResponse struct {
	ID                uuid.UUID                  `json:"id"`
	LeaseRequestID    uuid.UUID                  `json:"lease_request_id"`
	ChatID            *uuid.UUID                 `json:"chat_id,omitempty"`
	CarID             uuid.UUID                  `json:"car_id"`
	CarTitle          string                     `json:"car_title"`
	OwnerID           uuid.UUID                  `json:"owner_id"`
	DriverID          uuid.UUID                  `json:"driver_id"`
	OwnerName         string                     `json:"owner_name"`
	DriverName        string                     `json:"driver_name"`
	ViewerRole        string                     `json:"viewer_role"` // "owner" | "driver"
	CounterpartyName  string                     `json:"counterparty_name"`
	Status            VehicleReturnStatus        `json:"status"`
	DriverInitiatedAt RFC3339Time                `json:"driver_initiated_at"`
	OwnerConfirmedAt  *RFC3339Time               `json:"owner_confirmed_at,omitempty"`
	DisputedAt        *RFC3339Time               `json:"disputed_at,omitempty"`
	CompletedAt       *RFC3339Time               `json:"completed_at,omitempty"`
	CancelledAt       *RFC3339Time               `json:"cancelled_at,omitempty"`
	PickupConfirmedAt RFC3339Time                `json:"pickup_confirmed_at"`
	ReturnedAt        RFC3339Time                `json:"returned_at"`
	RentalWeeks       int                        `json:"rental_weeks"`
	PaidAmountCents   int64                      `json:"paid_amount_cents"`
	UsedDays          int                        `json:"used_days"`
	RefundAmountCents int64                      `json:"refund_amount_cents"`
	RefundStatus      *VehicleReturnRefundStatus `json:"refund_status,omitempty"`
	RefundID          *string                    `json:"refund_id,omitempty"`
	RefundedAt        *RFC3339Time               `json:"refunded_at,omitempty"`
	DisputeReason     *string                    `json:"dispute_reason,omitempty"`
	// CancelWindowExpiresAt is the deadline by which the driver can call
	// /cancel to undo. Omitted (nil) once the row has moved past
	// driver_initiated. Computed server-side so the iOS card can render a
	// live countdown without timezone math.
	CancelWindowExpiresAt *RFC3339Time `json:"cancel_window_expires_at,omitempty"`
	CreatedAt             RFC3339Time  `json:"created_at"`
	UpdatedAt             RFC3339Time  `json:"updated_at"`
}

// VehicleReturnsListResponse is the API response for
// GET /vehicle-returns/today.
type VehicleReturnsListResponse struct {
	VehicleReturns []VehicleReturnResponse `json:"vehicle_returns"`
}
