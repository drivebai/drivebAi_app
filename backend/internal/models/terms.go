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

// ─── Guarantee amendment (2026-09-10) ───────────────────────────────────────
//
// DriveBai now covers up to one week of uncollected rent for the owner. The
// trigger is the rental being CLOSED OUT on the platform by any route we
// recognise — including a car that was never recovered, because a guarantee
// that pays when an owner loses a week's rent and pays nothing when they lose
// the car is backwards for a policy whose purpose is keeping owners.
//
// RollingOwnerTermsSentence above is preserved BYTE-IDENTICAL as the v1
// record. It was never written to a row; it is the text that was approved
// on 2026-09-07 and it stays untouched. Since migration 000062 the owner's
// acceptance of the CURRENT package is recorded verbatim in
// owner_terms_acceptances (GET/POST /me/owner-terms, admin record), and an
// owner cannot accept a rolling lease request without one.

// TermsVersionOwnerRollingV2 identifies the owner-facing package that
// replaces the owner-bears-everything term.
const TermsVersionOwnerRollingV2 = "owner-rolling-v2 (2026-09-10)"

// RollingOwnerTermsV2 is the owner-facing risk allocation under the capped
// guarantee. Shown on the owner terms page and recorded against the owner's
// acceptance.
const RollingOwnerTermsV2 = "How you get paid. DriveBai collects the weekly rent from the driver's saved card and pays your share after each rental week completes. Your share is that week's rent less the DriveBai fee.\n\n" +
	"If we can't collect. If a weekly payment cannot be collected from the driver after all retries, DriveBai will pay you for up to one week of that unpaid rent, at your normal share, once the rental has been closed out on DriveBai. A rental is closed out when the driver returns the car through the app and the return is completed, or when DriveBai support records a settlement that ends the rental — including where the car was not recovered. Nothing is paid before the rental is closed out, and never more than one week per rental, however long the unpaid period ran.\n\n" +
	"This payment takes the place of the driver's payment; it is not in addition to it. If DriveBai later recovers the money from the driver, DriveBai keeps that recovery and you keep the payment already made to you.\n\n" +
	"What this does not cover. Unpaid time beyond that one week is not covered. The vehicle itself is not covered. DriveBai does not insure your car, does not guarantee its return, and this payment is not an insurance policy. If a driver does not return your car, DriveBai will pursue the driver and support you, but recovering the vehicle is a matter for you, your insurer and law enforcement.\n\n" +
	"Terms: " + TermsVersionOwnerRollingV2 + "."

// TermsVersionRollingV3 is the driver consent package that describes the
// persistent balance. NOTE the reason it exists: the guarantee changes
// NOTHING for the driver — they still owe what they owe, and a payment we
// make to their owner does not discharge it. What changed is that the debt is
// now a visible, payable balance that blocks new rentals, and that closing an
// account does not clear it. A driver must be told those before agreeing.
//
// The guarantee is deliberately NOT mentioned: "the owner gets paid anyway"
// weakens the urgency to pay a debt we intend to pursue, and invites the
// belief that the debt is settled.
const TermsVersionRollingV3 = "rolling-billing-v3 (2026-09-10)"

// RollingDriverDisclosureV3 renders the v3 consent text. Paragraphs one and
// two are BYTE-IDENTICAL to v2 — the pickup-day example survives, per
// standing client instruction. Only the failure paragraph changes.
func RollingDriverDisclosureV3(amountCents int64) string {
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
			"Any days you used but didn't pay for are still owed. They're added to a balance you can see and pay off in the app at any time. "+
			"Until that balance is cleared you won't be able to start a new rental, and we'll contact you about it. "+
			"Closing your DriveBai account doesn't clear what you owe.\n\n"+
			"Terms: %s.",
		amt, amt, TermsVersionRollingV3)
}

// ─── Monthly interval (2026-09-10) ──────────────────────────────────────────

// TermsVersionRollingMonthlyV1 is the consent package for a MONTHLY rolling
// rental. It is its own version, not a parameterised weekly one: every number
// a driver is agreeing to changes — the amount, the cadence, the notice lead,
// and how much is at stake if a charge fails.
const TermsVersionRollingMonthlyV1 = "rolling-billing-monthly-v1 (2026-09-10)"

// RollingDriverDisclosureMonthlyV1 renders the monthly consent text.
//
// It keeps the shape of the weekly package the client approved — charged now,
// then the cadence, the pickup-day example, returning as the exit, the failure
// policy — because that shape was approved for being clear, and only the facts
// that actually differ are changed. The pickup-day example survives in monthly
// form.
func RollingDriverDisclosureMonthlyV1(amountCents int64) string {
	amt := float64(amountCents) / 100
	return fmt.Sprintf(
		"You're authorizing an automatic monthly charge. $%.2f will be charged now, for your first month. "+
			"After that, DriveBai will charge your card $%.2f every 28 days for as long as you keep the car — there is no fixed end date. "+
			"A month here is 28 days, not a calendar month, so the date moves earlier through the year. "+
			"Each new month is charged three days before it starts, counted from your pickup day and time "+
			"(pick up Tuesday the 3rd at 3 PM, and you're charged on the 28th at 3 PM), so your next charge comes 25 days after pickup. "+
			"We'll remind you three days before every charge, and this amount never changes without a new agreement from you.\n\n"+
			"Returning the car in the app stops the charges — that's the exit, any day, no notice needed. "+
			"You can also turn off auto-renew instead: your rental then ends when your paid month runs out. "+
			"Return mid-month and we refund the days you didn't use, counted across the whole 28.\n\n"+
			"If a monthly payment fails, we'll retry your card over the next two days and notify you each time; you keep the car while we retry. "+
			"If it still can't be collected, billing stops and your rental ends when your paid time runs out. "+
			"Any days you used but didn't pay for are still owed. They're added to a balance you can see and pay off in the app at any time. "+
			"Until that balance is cleared you won't be able to start a new rental, and we'll contact you about it. "+
			"Closing your DriveBai account doesn't clear what you owe.\n\n"+
			"Terms: %s.",
		amt, amt, TermsVersionRollingMonthlyV1)
}

// RollingDisclosureFor picks the package for an interval, so no caller has to
// remember which text goes with which cadence.
func RollingDisclosureFor(interval string, amountCents int64) (text, version string) {
	if interval == "monthly" {
		return RollingDriverDisclosureMonthlyV1(amountCents), TermsVersionRollingMonthlyV1
	}
	return RollingDriverDisclosureV3(amountCents), TermsVersionRollingV3
}
