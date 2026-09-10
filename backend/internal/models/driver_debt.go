package models

import (
	"time"

	"github.com/google/uuid"
)

// Debt statuses. A debt is 'open' until the money is collected, waived by an
// admin, or written off; every one of those is an exit.
const (
	DebtOpen       = "open"
	DebtPaid       = "paid"
	DebtWaived     = "waived"
	DebtWrittenOff = "written_off"
)

// Debt entry kinds — the append-only history behind a balance change.
const (
	DebtEntryOpened     = "opened"
	DebtEntryPayment    = "payment"
	DebtEntryWaive      = "waive"
	DebtEntryWriteOff   = "write_off"
	DebtEntryAdjustment = "adjustment"
)

// DebtReasonUncollectedRental is the only source today: days a driver used
// and we could not collect for.
const DebtReasonUncollectedRental = "uncollected_rental_days"

// DriverDebt is one debt incident owed by one driver. It is deliberately a
// row of its own rather than a view over billing_cycles: it must sum across
// rentals, survive the driver's account deletion, and carry the identifier
// snapshot that deletion erases from the users row.
type DriverDebt struct {
	ID                  uuid.UUID  `json:"id"`
	DriverID            uuid.UUID  `json:"driver_id"`
	LeaseRequestID      uuid.UUID  `json:"lease_request_id"`
	BillingCycleID      *uuid.UUID `json:"billing_cycle_id,omitempty"`
	OriginalAmountCents int64      `json:"original_amount_cents"`
	OutstandingCents    int64      `json:"outstanding_cents"`
	Currency            string     `json:"currency"`
	Status              string     `json:"status"`
	Reason              string     `json:"reason"`

	// Snapshot — never returned to a driver, admin surfaces only.
	SnapshotEmail   *string `json:"-"`
	SnapshotPhone   *string `json:"-"`
	SnapshotName    *string `json:"-"`
	CardFingerprint *string `json:"-"`
	CardLast4       *string `json:"-"`

	OpenedAt  time.Time  `json:"opened_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// DriverDebtSnapshot carries the identifiers captured when a debt opens.
type DriverDebtSnapshot struct {
	Email           string
	Phone           string
	Name            string
	CardFingerprint string
	CardLast4       string
}

// DriverBalance is what the driver, the owner's support ticket and the admin
// console all read: one number per driver, plus the debts behind it.
type DriverBalance struct {
	DriverID         uuid.UUID    `json:"driver_id"`
	OutstandingCents int64        `json:"outstanding_cents"`
	Currency         string       `json:"currency"`
	OpenDebtCount    int          `json:"open_debt_count"`
	Debts            []DriverDebt `json:"debts,omitempty"`
}

// HasBalance is the predicate every enforcement decision reads.
func (b *DriverBalance) HasBalance() bool { return b != nil && b.OutstandingCents > 0 }
