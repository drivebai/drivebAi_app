package models

import (
	"time"

	"github.com/google/uuid"
)

// KeyHandoverStatus tracks the two-party handover handshake.
type KeyHandoverStatus string

const (
	// KeyHandoverPending: created on payment success; owner must confirm they handed over keys.
	KeyHandoverPending KeyHandoverStatus = "pending"
	// KeyHandoverOwnerConfirmed: owner confirmed; driver must confirm receipt before the deadline.
	KeyHandoverOwnerConfirmed KeyHandoverStatus = "owner_confirmed"
	// KeyHandoverCompleted: both parties confirmed; the rental clock starts at started_at.
	KeyHandoverCompleted KeyHandoverStatus = "completed"
	// KeyHandoverExpired: the driver did not confirm within the window.
	KeyHandoverExpired KeyHandoverStatus = "expired"
)

// KeyHandoverConfirmWindow is how long the driver has to confirm receipt after
// the owner marks the keys as handed over.
const KeyHandoverConfirmWindow = 15 * time.Minute

// KeyHandover is the DB-backed record. Exactly one per lease request.
type KeyHandover struct {
	ID                   uuid.UUID
	LeaseRequestID       uuid.UUID
	CarID                uuid.UUID
	OwnerID              uuid.UUID
	DriverID             uuid.UUID
	PickupLatitude       *float64
	PickupLongitude      *float64
	PickupArea           *string
	Status               KeyHandoverStatus
	OwnerConfirmedAt     *time.Time
	DriverConfirmedAt    *time.Time
	ConfirmationDeadline *time.Time
	StartedAt            *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// IsActive reports whether the handover is still in an actionable (Today) state.
func (k *KeyHandover) IsActive() bool {
	return k.Status == KeyHandoverPending || k.Status == KeyHandoverOwnerConfirmed
}

// Error codes for key handover operations.
const (
	ErrCodeKeyHandoverNotFound   = "KEY_HANDOVER_NOT_FOUND"
	ErrCodeInvalidHandoverAction = "INVALID_HANDOVER_ACTION"
	ErrCodeHandoverExpired       = "KEY_HANDOVER_EXPIRED"
	// ErrCodeHandoverOwnerNotConfirmed: driver tried to confirm receipt of the
	// keys before the owner has marked the keys handed over. Distinct from
	// ErrCodeInvalidHandoverAction so the iOS client can render a role-specific
	// "waiting on the owner" message instead of the generic guard copy.
	ErrCodeHandoverOwnerNotConfirmed = "HANDOVER_OWNER_NOT_CONFIRMED"
)

var (
	ErrKeyHandoverNotFound     = &APIError{Code: ErrCodeKeyHandoverNotFound, Message: "Key handover not found"}
	ErrInvalidHandoverAction   = &APIError{Code: ErrCodeInvalidHandoverAction, Message: "Invalid action for the current handover status"}
	ErrHandoverExpired         = &APIError{Code: ErrCodeHandoverExpired, Message: "The key handover confirmation window has expired"}
	ErrHandoverNotDismissable  = &APIError{Code: "HANDOVER_NOT_DISMISSABLE", Message: "This card can only be dismissed once the pickup deadline has been refunded/cancelled"}
	ErrHandoverOwnerNotConfirmed = &APIError{Code: ErrCodeHandoverOwnerNotConfirmed, Message: "The owner hasn't handed over the keys yet. Please wait for the owner to confirm."}
)

// --- API response types ---

// KeyHandoverResponse is the per-viewer API shape. ViewerRole tells the client
// which confirmation CTA to render without needing to compare user IDs locally.
//
// Pickup deadline fields (Pickup*) are mirrored from the underlying lease so
// the Today tab can render the pickup countdown + owner extension CTA without
// an extra round-trip to /lease-requests/{id}. They're nil for handovers
// whose lease has already moved past the awaiting-pickup state.
type KeyHandoverResponse struct {
	ID                   uuid.UUID         `json:"id"`
	LeaseRequestID       uuid.UUID         `json:"lease_request_id"`
	CarID                uuid.UUID         `json:"car_id"`
	CarTitle             string            `json:"car_title"`
	ChatID               *uuid.UUID        `json:"chat_id,omitempty"`
	OwnerID              uuid.UUID         `json:"owner_id"`
	DriverID             uuid.UUID         `json:"driver_id"`
	OwnerName            string            `json:"owner_name"`
	DriverName           string            `json:"driver_name"`
	CounterpartyName     string            `json:"counterparty_name"`
	ViewerRole           string            `json:"viewer_role"` // "owner" | "driver"
	PickupArea           string            `json:"pickup_area"`
	PickupLatitude       *float64          `json:"pickup_latitude,omitempty"`
	PickupLongitude      *float64          `json:"pickup_longitude,omitempty"`
	Status               KeyHandoverStatus `json:"status"`
	OwnerConfirmedAt     *RFC3339Time      `json:"owner_confirmed_at,omitempty"`
	DriverConfirmedAt    *RFC3339Time      `json:"driver_confirmed_at,omitempty"`
	ConfirmationDeadline *RFC3339Time      `json:"confirmation_deadline,omitempty"`
	StartedAt            *RFC3339Time      `json:"started_at,omitempty"`
	// Mirrored from lease_requests so the Today tab can render the pickup
	// countdown + extension CTA in one fetch.
	LeaseStatus                 *LeaseRequestStatus `json:"lease_status,omitempty"`
	PickupDeadlineAt            *RFC3339Time        `json:"pickup_deadline_at,omitempty"`
	PickupConfirmedAt           *RFC3339Time        `json:"pickup_confirmed_at,omitempty"`
	PickupExtensionTotalMinutes int                 `json:"pickup_extension_total_minutes"`
	PickupExtensionCount        int                 `json:"pickup_extension_count"`
	PickupExtensionRemainingMin int                 `json:"pickup_extension_remaining_minutes"`
	PickupLastExtendedAt        *RFC3339Time        `json:"pickup_last_extended_at,omitempty"`
	CreatedAt                   RFC3339Time         `json:"created_at"`
	UpdatedAt                   RFC3339Time         `json:"updated_at"`
}

// KeyHandoversListResponse is the API response for GET /key-handovers/today.
type KeyHandoversListResponse struct {
	KeyHandovers []KeyHandoverResponse `json:"key_handovers"`
}

// NewRFC3339TimePtr converts an optional time into an optional RFC3339Time for
// JSON responses (nil stays nil so the field is omitted).
func NewRFC3339TimePtr(t *time.Time) *RFC3339Time {
	if t == nil {
		return nil
	}
	v := RFC3339Time(*t)
	return &v
}
