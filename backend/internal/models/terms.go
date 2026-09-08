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
// booking. It is stored verbatim on the consent row. The anchor weekday is
// unknowable until pickup, so the text names the RULE, not a guess.
func RollingDriverDisclosure(amountCents int64) string {
	return fmt.Sprintf(
		"You agree that DriveBai will charge your saved card $%.2f today for your first week, and then $%.2f every 7 days "+
			"(charged one day before each new week begins, anchored to your pickup time) for as long as this rental continues. "+
			"You'll be reminded before every charge. End the rental anytime in the app by returning the vehicle; "+
			"if you return mid-week, the unused days of your current week are refunded pro-rata. "+
			"Terms: %s.",
		float64(amountCents)/100, float64(amountCents)/100, TermsVersionRolling)
}

// TermsVersionRollingV2 is the client-approved consent package for the
// batch-4 consent screen (approved 2026-09-07 with three edits: explicit
// first-charge line, arrears payment path, return-first exit ordering).
const TermsVersionRollingV2 = "rolling-billing-v2 (2026-09-07)"

// RollingDriverDisclosureV2 renders the consent screen's recorded text.
// Client-approved verbatim — NEVER edit under this version string; any
// wording change mints v3. The pickup-day example sentence survives all
// future edits by client instruction.
func RollingDriverDisclosureV2(amountCents int64) string {
	amt := float64(amountCents) / 100
	return fmt.Sprintf(
		"You're authorizing an automatic weekly charge. $%.2f will be charged now, for your first week. "+
			"After that, DriveBai will charge your card $%.2f every 7 days for as long as you keep the car — there is no fixed end date. "+
			"Each new week is charged one day before it starts, counted from your pickup day and time "+
			"(pick up Tuesday at 3 PM, and you're charged every Monday around 3 PM), so your next charge comes 6 days after pickup. "+
			"We'll remind you before every charge, and this amount never changes without a new agreement from you.\n\n"+
			"Returning the car in the app stops the charges — that's the exit, any day, no notice needed. "+
			"You can also turn off auto-renew instead: your rental then ends when your paid week runs out. "+
			"Return mid-week and we refund the days you didn't use.\n\n"+
			"If a weekly payment fails, we'll retry your card over the next two days and notify you each time; you keep the car while we retry. "+
			"If it still can't be collected, weekly billing stops and your rental ends when your paid time runs out. "+
			"Any days you used but didn't pay for are still owed — you can settle them in the app with Pay now, and we'll contact you if the balance isn't paid.\n\n"+
			"Terms: %s.",
		amt, amt, TermsVersionRollingV2)
}

// TermsVersionRollingAmendV1 is the amendment-acceptance package: the
// successor consent must describe THE AMENDMENT, not the booking (review
// HIGH: recording the booking text would assert a charge 'now' that never
// happens at acceptance — corrupting the very evidence the ceremony
// exists to produce).
const TermsVersionRollingAmendV1 = "rolling-amendment-v1 (2026-09-08)"

// RollingAmendmentDisclosure renders what the driver agrees to when
// accepting a price amendment mid-tenancy. Recorded verbatim on the
// successor consent; shown to the driver before accepting.
func RollingAmendmentDisclosure(newAmountCents, oldAmountCents int64) string {
	return fmt.Sprintf(
		"You agree that from your next rental week, DriveBai will charge your saved card $%.2f every 7 days, "+
			"replacing the current $%.2f. Your current paid week is unaffected and no charge happens now. "+
			"Everything else you agreed to at booking stays the same: each new week is charged one day before it starts, "+
			"you're reminded before every charge, returning the car in the app stops the charges, unused days of a "+
			"returned week are refunded pro-rata, and the failed-payment policy is unchanged. "+
			"Terms: %s.",
		float64(newAmountCents)/100, float64(oldAmountCents)/100, TermsVersionRollingAmendV1)
}
