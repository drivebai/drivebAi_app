package models

import "fmt"

// Versioned terms for recurring billing. The consent record stores
// TermsVersionRolling verbatim; changing ANY user-facing sentence below
// requires minting a new version string — never edit text under an
// existing version, the recorded consents reference it.

// TermsVersionRolling identifies the terms package in force when a driver
// consents to rolling weekly billing.
const TermsVersionRolling = "rolling-billing-v1 (2026-09-07)"

// RollingOwnerTermsSentence is the owner-facing risk-allocation term the
// client approved on 2026-09-07 (sign-off #3 condition): it appears in the
// owner-facing terms page and is part of the versioned terms package
// recorded on every consent.
const RollingOwnerTermsSentence = "DriveBai collects rent from the driver's saved card each week and passes your share to you after each rental week completes. If a driver's payment cannot be collected after all retries, we pursue it through the app and our support process — but rental days that are never successfully collected are borne by the owner. DriveBai does not advance or guarantee rental income."

// RollingDriverDisclosure renders the exact text the driver agrees to at
// booking. It is stored verbatim on the consent row.
func RollingDriverDisclosure(amountCents int64, weekday, firstChargeDay string) string {
	return fmt.Sprintf(
		"You agree that DriveBai will charge your saved card $%.2f every week, on %s, starting %s, for as long as this rental continues. "+
			"You'll be reminded before each charge. End the rental anytime in the app by returning the vehicle; "+
			"if you return mid-week, the unused days of your current week are refunded pro-rata. "+
			"Terms: %s.",
		float64(amountCents)/100, weekday, firstChargeDay, TermsVersionRolling)
}
