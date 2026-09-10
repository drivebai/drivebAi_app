package models

import (
	"time"

	"github.com/google/uuid"
)

// Owner payouts (Stripe Connect, separate charges & transfers). The charge
// always lands whole in the platform balance; the owner's share moves as a
// Transfer when the rental reaches a settled end. One ledger row per lease.

// OwnerPayoutStatus is the ledger row lifecycle.
type OwnerPayoutStatus string

const (
	// PayoutAwaitingOnboarding: the owner isn't payout-ready; the money
	// waits in the platform balance and the transfer fires automatically
	// the moment account.updated reports ready. Time-bounded escrow —
	// reminders and an escalation ticket keep it from silently aging.
	PayoutAwaitingOnboarding OwnerPayoutStatus = "awaiting_onboarding"
	PayoutPending            OwnerPayoutStatus = "pending"
	PayoutPaid               OwnerPayoutStatus = "paid"
	PayoutFailed             OwnerPayoutStatus = "failed"
	// PayoutWithheld: a deliberate admin decision NOT to pay, with the
	// reason recorded — the "explicitly and deliberately not paid" leg of
	// the settlement matrix.
	PayoutWithheld OwnerPayoutStatus = "withheld"
	// PayoutReversed: the transfer was clawed back after a LOST card
	// dispute (partial Transfer Reversal against the connected account).
	// Terminal; reversal columns record the trr_…, amount, and reason.
	PayoutReversed OwnerPayoutStatus = "reversed"
	// PayoutAccruing: a rolling cycle's charge landed; amounts provisional
	// until the week is CONSUMED (arrears model) — a mid-week return
	// rewrites them while no money has moved.
	PayoutAccruing OwnerPayoutStatus = "accruing"
	// PayoutVoided: cycle fully refunded before consumption. Terminal.
	PayoutVoided OwnerPayoutStatus = "voided"
)

// OwnerPayoutSource records why the split exists.
type OwnerPayoutSource string

const (
	PayoutSourceReturnCompleted OwnerPayoutSource = "return_completed"
	PayoutSourceAdminSettlement OwnerPayoutSource = "admin_settlement"
	// PayoutSourceSaleCompleted: a car sale completed — the inspection window
	// closed without an upheld rejection and the money was captured.
	PayoutSourceSaleCompleted OwnerPayoutSource = "sale_completed"
	// PayoutSourceCycleConsumed: a rolling cycle's week completed (arrears
	// promotion) — the normal weekly payout source.
	PayoutSourceCycleConsumed OwnerPayoutSource = "cycle_consumed"
)

// UserPayoutStatus is the coarse connected-account state the app renders.
type UserPayoutStatus string

const (
	PayoutAccountNone         UserPayoutStatus = "none"                 // never started
	PayoutAccountOnboarding   UserPayoutStatus = "onboarding"           // account created, details not submitted
	PayoutAccountPendingVerif UserPayoutStatus = "pending_verification" // submitted, Stripe reviewing
	PayoutAccountActionNeeded UserPayoutStatus = "action_needed"        // Stripe wants more (currently_due non-empty)
	PayoutAccountReady        UserPayoutStatus = "ready"                // payouts_enabled, nothing due
	PayoutAccountRestricted   UserPayoutStatus = "restricted"           // disabled/rejected — payouts off
)

// OwnerPayout is the ledger row.
type OwnerPayout struct {
	ID               uuid.UUID         `json:"id"`
	// Exactly one of LeaseRequestID / PurchaseRequestID is set — enforced by
	// the owner_payouts_one_source CHECK (migration 000060). A sale has no
	// lease row and never will.
	LeaseRequestID    *uuid.UUID `json:"lease_request_id,omitempty"`
	PurchaseRequestID *uuid.UUID `json:"purchase_request_id,omitempty"`
	OwnerID          uuid.UUID         `json:"owner_id"`
	StripeAccountID  *string           `json:"-"`
	GrossKeptCents   int64             `json:"gross_kept_cents"`
	FeeBPS           int               `json:"fee_bps"`
	FeeCents         int64             `json:"fee_cents"`
	OwnerAmountCents int64             `json:"owner_amount_cents"`
	Currency         string            `json:"currency"`
	Status           OwnerPayoutStatus `json:"status"`
	Source           OwnerPayoutSource `json:"source"`
	SourceChargeID   *string           `json:"-"`
	StripeTransferID *string           `json:"stripe_transfer_id,omitempty"`
	FailureReason    *string           `json:"failure_reason,omitempty"`
	Note             *string           `json:"note,omitempty"`
	ReminderCount    int               `json:"reminder_count"`
	LastReminderAt   *time.Time        `json:"last_reminder_at,omitempty"`
	EscalatedAt      *time.Time        `json:"escalated_at,omitempty"`
	PaidAt           *time.Time        `json:"paid_at,omitempty"`
	BillingCycleID   *uuid.UUID        `json:"billing_cycle_id,omitempty"`
	PeriodStart      *time.Time        `json:"period_start,omitempty"`
	PeriodEnd        *time.Time        `json:"period_end,omitempty"`
	ConsumedAt       *time.Time        `json:"consumed_at,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// ComputePayoutSplit is THE money split — the single definition used by the
// completion path, admin settlement, tests, and any future surface. The fee
// FLOORS; the owner receives the remainder. Invariant (tested):
//
//	fee + owner == kept, always — no cent lost or invented.
//
// Defensive floors mirror ComputeReturnRefund: negative kept clamps to 0;
// a nonsensical bps clamps into [0, 10000].
func ComputePayoutSplit(keptCents int64, feeBPS int) (feeCents, ownerCents int64) {
	if keptCents <= 0 {
		return 0, 0
	}
	if feeBPS < 0 {
		feeBPS = 0
	}
	if feeBPS > 10000 {
		feeBPS = 10000
	}
	feeCents = keptCents * int64(feeBPS) / 10000 // integer floor
	ownerCents = keptCents - feeCents
	return feeCents, ownerCents
}

// PayoutEscrowReminderEvery is the cadence of "finish setup to get your $X"
// reminders while a payout sits in awaiting_onboarding.
const PayoutEscrowReminderEvery = 7 * 24 * time.Hour

// PayoutEscrowEscalateAfter is when an unclaimed balance stops being a
// nudge-loop and becomes a human's problem: a support ticket is opened
// (once) so an admin sees every stale balance with its age. The 30/60/90
// day POLICY beyond this is a business decision recorded in the report —
// nothing here ever converts or forfeits the owner's money on its own.
const PayoutEscrowEscalateAfter = 60 * 24 * time.Hour
