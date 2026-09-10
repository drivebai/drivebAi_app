package models

import (
	"time"

	"github.com/google/uuid"
)

// --- Rolling billing (batch 2 of the recurring-billing design) ---

// LeaseBillingMode distinguishes the legacy single-charge lease from the
// rolling weekly one. Set at INSERT, immutable. Every rolling-only
// predicate requires BillingModeRolling explicitly — the fixed-term
// guarantee is this default plus those predicates.
type LeaseBillingMode string

const (
	BillingModeFixedTerm LeaseBillingMode = "fixed_term"
	BillingModeRolling   LeaseBillingMode = "rolling"
)

const (
	// BillingCycleLength is v1's only cycle: weekly. 28-day cycles are an
	// explicit v2 with re-derived consent, dunning, and exposure bounds.
	BillingCycleLength = 7 * 24 * time.Hour
	// BillingMaxCatchUp bounds how stale a paid-through date may be and
	// still be billed automatically.
	//
	// Without it, a lease resumed after a long park — a 60-75 day dispute,
	// or a consent_revoked halt lasting until the driver replaces their
	// card — is back-billed one week per 60-second tick: ten weeks of rent
	// in ten minutes, off-session, each preceded by a notice quoting a
	// charge date already in the past. That is also precisely how a card
	// issuer learns to treat us as fraudulent.
	//
	// Beyond this the lease is left for a human rather than charged.
	BillingMaxCatchUp = 2 * BillingCycleLength
	// BillingChargeLead: the renewal charge fires this long BEFORE
	// paid-through lapses — simultaneously the decline-recovery runway,
	// the driver's stop cutoff, and the owner-termination notice floor.
	BillingChargeLead = 24 * time.Hour
	// BillingNoticeLead: the driver's "renews soon" notice (auto-renewal-law
	// hygiene: amount + moment disclosed before money moves).
	BillingNoticeLead = 48 * time.Hour
	// BillingRetrySpacing: confirm-retries at T0, +24h, +48h after the
	// T-24h first attempt — 4 attempts total, inside network budgets.
	BillingRetrySpacing = 24 * time.Hour
	// BillingMaxAttempts caps the automatic ladder.
	BillingMaxAttempts = 4
	// BillingNeedsActionTTL bounds the driver's 3DS rescue window.
	BillingNeedsActionTTL = 72 * time.Hour
)

type BillingCycleStatus string

const (
	CycleScheduled         BillingCycleStatus = "scheduled"
	CycleCharging          BillingCycleStatus = "charging"
	CycleNeedsAction       BillingCycleStatus = "needs_action"
	CycleRetrying          BillingCycleStatus = "retrying"
	CyclePaid              BillingCycleStatus = "paid"
	CycleFailedFinal       BillingCycleStatus = "failed_final"
	CycleArrearsDue        BillingCycleStatus = "arrears_due"
	CycleWaived            BillingCycleStatus = "waived"
	CycleRefunded          BillingCycleStatus = "refunded"
	CyclePartiallyRefunded BillingCycleStatus = "partially_refunded"
)

// BillingCycle is one lease-week of a rolling rental: ONE PaymentIntent for
// its whole life, created once and re-confirmed on retries.
type BillingCycle struct {
	ID                    uuid.UUID          `json:"id"`
	LeaseRequestID        uuid.UUID          `json:"lease_request_id"`
	CycleNumber           int                `json:"cycle_number"`
	PeriodStart           time.Time          `json:"period_start"`
	PeriodEnd             time.Time          `json:"period_end"`
	AmountCents           int64              `json:"amount_cents"`
	Status                BillingCycleStatus `json:"status"`
	StripePaymentIntentID *string            `json:"-"`
	AttemptCount          int                `json:"attempt_count"`
	NextAttemptAt         *time.Time         `json:"next_attempt_at,omitempty"`
	LastDeclineCode       *string            `json:"-"` // support only, never surfaced
	NeedsActionSince      *time.Time         `json:"needs_action_since,omitempty"`
	RefundedCents         int64              `json:"refunded_cents"`
	RefundID              *string            `json:"-"`
	FailureNotifiedAt     *time.Time         `json:"-"`
	DelinquentNotifiedAt  *time.Time         `json:"-"`
	AdminNote             *string            `json:"admin_note,omitempty"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

// BillingConsent is the driver's recorded agreement to recurring charges —
// the ONLY source of the amount and payment method for every off-session
// charge, and the durable record card networks require.
type BillingConsent struct {
	ID                    uuid.UUID  `json:"id"`
	LeaseRequestID        uuid.UUID  `json:"lease_request_id"`
	DriverID              uuid.UUID  `json:"driver_id"`
	AmountCents           int64      `json:"amount_cents"`
	BillingInterval       string     `json:"billing_interval"`
	TermsVersion          string     `json:"terms_version"`
	DisclosureText        string     `json:"disclosure_text"`
	StripePaymentMethodID *string    `json:"-"`
	CardBrand             *string    `json:"card_brand,omitempty"`
	CardLast4             *string    `json:"card_last4,omitempty"`
	CardFingerprint       *string    `json:"-"`
	ActivatedAt           *time.Time `json:"activated_at,omitempty"`
	RevokedAt             *time.Time `json:"revoked_at,omitempty"`
	RevokedReason         *string    `json:"revoked_reason,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

// Active reports whether renewals may charge under this consent.
func (c *BillingConsent) Active() bool {
	return c.RevokedAt == nil && c.ActivatedAt != nil && c.StripePaymentMethodID != nil
}

// --- Amendments (price / interval offers) ---

// AmendmentOfferTTL bounds how long an owner's proposal stays open.
const AmendmentOfferTTL = 7 * 24 * time.Hour

// BillingAmendmentOffer is an owner's proposed change to a rolling
// mandate. Nothing changes until the DRIVER accepts — the recorded
// disclosure promises "this amount never changes without a new agreement
// from you", so a unilateral write would be a card-network violation.
type BillingAmendmentOffer struct {
	ID             uuid.UUID  `json:"id"`
	LeaseRequestID uuid.UUID  `json:"lease_request_id"`
	ProposedBy     uuid.UUID  `json:"proposed_by"`
	Kind           string     `json:"kind"` // 'price' | 'interval'
	NewAmountCents int64      `json:"new_amount_cents"`
	NewInterval    string     `json:"new_interval"`
	Status         string     `json:"status"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ActedAt        *time.Time `json:"acted_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// BillingIntervalNoticeLead is how far ahead the driver is told a renewal is
// coming. Weekly keeps 48 hours; monthly gets 72.
//
// The lead is not cosmetic. Auto-renewal statutes in several states scale the
// required notice with the term, and the practical argument runs the same way:
// a month's rent is four times the weekly amount, so a driver needs more than
// a day and a half to move money or cancel before it is taken off their card.
func BillingIntervalNoticeLead(interval string) time.Duration {
	if interval == "monthly" {
		return 72 * time.Hour
	}
	return BillingNoticeLead
}

// BillingIntervalChargeLead is how far before paid-through the charge fires.
// Monthly gets a longer runway because the dunning ladder that follows a
// decline has to fit inside it, and a failed month is worth four weeks.
func BillingIntervalChargeLead(interval string) time.Duration {
	if interval == "monthly" {
		return 72 * time.Hour
	}
	return BillingChargeLead
}

// BillingIntervalLength maps a consent's interval to its cycle length.
// Month = 4 weeks (28d) platform-wide, per RentMonthWeeks.
func BillingIntervalLength(interval string) time.Duration {
	if interval == "monthly" {
		return 28 * 24 * time.Hour
	}
	return BillingCycleLength
}
