package models

import (
	"time"

	"github.com/google/uuid"
)

// ChargeDispute mirrors one Stripe card dispute (chargeback). One row per
// stripe_dispute_id — UNIQUE in the schema, which is what makes webhook
// redelivery idempotent. Policy (recurring-billing design §5):
//
//	OPEN  → withhold the lease's UNPAID payout rows + support ticket.
//	        Never claw back on an open dispute — most are won.
//	LOST  → partial Transfer Reversal of exactly the paid transfer.
//	WON   → release what was withheld.
type ChargeDispute struct {
	ID              uuid.UUID  `json:"id"`
	StripeDisputeID string     `json:"stripe_dispute_id"`
	StripeChargeID  string     `json:"stripe_charge_id"`
	PaymentIntentID *string    `json:"payment_intent_id,omitempty"`
	LeaseRequestID  *uuid.UUID `json:"lease_request_id,omitempty"`
	AmountCents     int64      `json:"amount_cents"`
	Currency        string     `json:"currency"`
	Reason          *string    `json:"reason,omitempty"`
	Status          string     `json:"status"`
	Outcome         *string    `json:"outcome,omitempty"`
	TicketID        *uuid.UUID `json:"ticket_id,omitempty"`
	PayoutsWithheld bool       `json:"payouts_withheld"`
	ReversalDone    bool       `json:"reversal_done"`
	OutcomeSettled  bool       `json:"outcome_settled"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
