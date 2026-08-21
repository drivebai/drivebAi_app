package models

import "github.com/google/uuid"

// TodayActionType distinguishes the source of an action.
type TodayActionType string

const (
	// TodayActionLeaseRequest: owner-side card for a lease the driver
	// just sent (status=requested) — Accept/Decline lives in the chat.
	TodayActionLeaseRequest TodayActionType = "lease_request"
	// TodayActionLeasePayment: driver-side card for a lease the owner
	// accepted that's now waiting for the driver to pay
	// (status=accepted, or payment_pending after a failed/retry attempt).
	// The card is a fast-access surface; payment itself happens in the
	// existing Chat → Requests lease card.
	TodayActionLeasePayment TodayActionType = "lease_payment"
	// TodayActionLeasePriceReview: driver-side card that fires the moment
	// the owner adjusts the offered price on a non-terminal lease. Pay Now
	// is held until the driver explicitly accepts or declines the new
	// offer from Chat → Requests; this card is the fast-access shortcut
	// so they don't miss the update.
	TodayActionLeasePriceReview TodayActionType = "lease_price_review"
	// TodayActionPurchaseAction: buyer- or seller-side card for a
	// non-terminal purchase_request. The status field carries the exact
	// state so iOS can render the right CTA copy.
	TodayActionPurchaseAction TodayActionType = "purchase_action"
)

// TodayAction is a single item in the owner/driver's Today feed.
type TodayAction struct {
	ID               uuid.UUID       `json:"id"`
	Type             TodayActionType `json:"type"`
	Title            string          `json:"title"`
	Body             string          `json:"body"`
	CarID            uuid.UUID       `json:"car_id"`
	CarTitle         string          `json:"car_title"`
	ChatID           uuid.UUID       `json:"chat_id"`
	CounterpartyID   uuid.UUID       `json:"counterparty_id"`
	CounterpartyName string          `json:"counterparty_name"`
	Status           string          `json:"status"`
	CreatedAt        RFC3339Time     `json:"created_at"`
	ExpiresAt        RFC3339Time     `json:"expires_at"`
	PrimaryAction    string          `json:"primary_action"`
	SecondaryAction  string          `json:"secondary_action"`
}

// DriverActiveRental is the driver-side "rental in progress" card (lifecycle
// batch, defect 3) — the standing view of the car they hold: what, from whom,
// until when, and where it goes back. Money is cents, matching
// ActiveRentalSummary and the payments pipeline.
//
// Delivered as a NEW top-level field on TodayActionsResponse rather than a
// new TodayAction type: the iOS TodayActionAPIModel decodes every action
// field as non-optional, so one unknown action shape would kill the whole
// feed on old builds, while an extra response key is ignored by Codable.
type DriverActiveRental struct {
	LeaseRequestID    uuid.UUID   `json:"lease_request_id"`
	CarID             uuid.UUID   `json:"car_id"`
	CarTitle          string      `json:"car_title"`
	CarPhotoURL       *string     `json:"car_photo_url,omitempty"`
	OwnerID           uuid.UUID   `json:"owner_id"`
	OwnerName         string      `json:"owner_name"`
	ChatID            *uuid.UUID  `json:"chat_id,omitempty"`
	PickupConfirmedAt RFC3339Time `json:"pickup_confirmed_at"`
	RentalEndsAt      RFC3339Time `json:"rental_ends_at"`
	// DaysRemaining goes negative once overdue (−1 = up to 24h past due).
	DaysRemaining int             `json:"days_remaining"`
	TermState     RentalTermState `json:"term_state"`
	Weeks         int             `json:"weeks"`
	WeeklyCents   int64           `json:"weekly_price_cents"`
	PaidCents     int64           `json:"paid_amount_cents"`
	// Return location: the pickup snapshot from key_handovers when one
	// exists (that is where the parties actually met), else the car's
	// listed area. ReturnLocationSource tells the UI which label to render
	// — "pickup" ("Return where you picked up") vs "listing" ("Owner's
	// listed location"). No negotiated return point exists anywhere in the
	// system; surfacing anything else would be inventing data.
	ReturnLocationArea   *string  `json:"return_location_area,omitempty"`
	ReturnLocationLat    *float64 `json:"return_location_lat,omitempty"`
	ReturnLocationLng    *float64 `json:"return_location_lng,omitempty"`
	ReturnLocationSource string   `json:"return_location_source"`
}

// TodayActionsResponse is the API response for GET /today/actions.
type TodayActionsResponse struct {
	Actions          []TodayAction `json:"actions"`
	HasUnreadActions bool          `json:"has_unread_actions"`
	// ActiveRentals: the driver's rentals in progress (paid, picked up, not
	// returned). A driver can hold two cars from two listings at once, so
	// this is a slice; empty/omitted when none. Additive field — see
	// DriverActiveRental's doc for why this is not a TodayAction type.
	ActiveRentals []DriverActiveRental `json:"active_rentals,omitempty"`
}
