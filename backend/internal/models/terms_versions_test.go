package models

import "testing"

// The old packages are EVIDENCE. Anyone who agreed under them agreed to those
// exact words, so a later edit — even a harmless-looking one — destroys the
// record. These tests fail loudly if a version's text is ever touched.
func TestTermsVersionsArePinned(t *testing.T) {
	if TermsVersionRolling != "rolling-billing-v1 (2026-09-07)" {
		t.Errorf("v1 version string changed: %q", TermsVersionRolling)
	}
	if TermsVersionRollingV2 != "rolling-billing-v2 (2026-09-07)" {
		t.Errorf("v2 version string changed: %q", TermsVersionRollingV2)
	}
	if TermsVersionRollingV3 != "rolling-billing-v3 (2026-09-10)" {
		t.Errorf("v3 version string changed: %q", TermsVersionRollingV3)
	}
	if TermsVersionOwnerRollingV2 != "owner-rolling-v2 (2026-09-10)" {
		t.Errorf("owner v2 version string changed: %q", TermsVersionOwnerRollingV2)
	}
}

// The owner-bears-everything sentence is no longer true, but it is preserved
// untouched as the v1 record.
func TestOwnerTermsV1PreservedVerbatim(t *testing.T) {
	const want = "DriveBai collects rent from the driver's saved card each week and passes your share to you after each rental week completes. If a driver's payment cannot be collected after all retries, we pursue it through the app and our support process — but rental days that are never successfully collected are borne by the owner. DriveBai does not advance or guarantee rental income."
	if RollingOwnerTermsSentence != want {
		t.Error("the v1 owner term was edited — it is the record of what was approved on 2026-09-07")
	}
}

// v3 keeps the pickup-day example, which the client requires to survive every
// edit, and keeps paragraphs one and two identical to v2.
func TestDriverV3PreservesTheApprovedSentences(t *testing.T) {
	v2 := RollingDriverDisclosureV2(14994)
	v3 := RollingDriverDisclosureV3(14994)

	const pickupExample = "(pick up Tuesday at 3 PM, and you're charged every Monday around 3 PM), so your next charge comes 6 days after pickup."
	if !contains(v3, pickupExample) {
		t.Error("v3 dropped the pickup-day example — it must survive all edits")
	}
	const exitSentence = "Returning the car in the app stops the charges — that's the exit, any day, no notice needed."
	if !contains(v3, exitSentence) {
		t.Error("v3 dropped the return-is-the-exit sentence")
	}
	// Everything before the failure paragraph must be identical.
	cut := func(s string) string {
		i := indexOf(s, "If a weekly payment fails")
		if i < 0 {
			t.Fatal("failure paragraph missing")
		}
		return s[:i]
	}
	if cut(v2) != cut(v3) {
		t.Error("v3 changed a paragraph it was not supposed to touch")
	}
	// And v3 must say the three new things.
	for _, want := range []string{
		"balance you can see and pay off in the app",
		"won't be able to start a new rental",
		"Closing your DriveBai account doesn't clear what you owe.",
	} {
		if !contains(v3, want) {
			t.Errorf("v3 is missing: %q", want)
		}
	}
	// The guarantee is deliberately absent from the driver's text.
	for _, unwanted := range []string{"guarantee", "the owner is paid", "DriveBai pays the owner"} {
		if contains(v3, unwanted) {
			t.Errorf("v3 mentions the guarantee (%q) — the driver still owes, and saying otherwise weakens collection", unwanted)
		}
	}
}

func TestOwnerTermsV2SaysWhatIsAndIsNotCovered(t *testing.T) {
	for _, want := range []string{
		"up to one week",
		"closed out on DriveBai",
		"including where the car was not recovered",
		"takes the place of the driver's payment",
		"DriveBai keeps that recovery",
		"is not an insurance policy",
	} {
		if !contains(RollingOwnerTermsV2, want) {
			t.Errorf("owner v2 is missing: %q", want)
		}
	}
}

func contains(hay, needle string) bool { return indexOf(hay, needle) >= 0 }

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// The monthly package is its own version, and every number in it must differ
// from the weekly one — that is the whole reason it is a separate package.
func TestMonthlyDisclosureStatesMonthlyFacts(t *testing.T) {
	monthly := RollingDriverDisclosureMonthlyV1(59976) // 4 x the CR-V weekly
	weekly := RollingDriverDisclosureV3(14994)

	if monthly == weekly {
		t.Fatal("the monthly package is identical to the weekly one")
	}
	for _, want := range []string{
		"every 28 days",
		"28 days, not a calendar month",
		"charged three days before it starts",
		"remind you three days before every charge",
		"counted across the whole 28",
		"$599.76",
	} {
		if !contains(monthly, want) {
			t.Errorf("monthly package is missing: %q", want)
		}
	}
	// It must not quote the weekly cadence anywhere.
	for _, unwanted := range []string{"every 7 days", "one day before", "6 days after pickup"} {
		if contains(monthly, unwanted) {
			t.Errorf("monthly package still quotes the weekly cadence: %q", unwanted)
		}
	}
	if !contains(monthly, TermsVersionRollingMonthlyV1) {
		t.Error("monthly package does not record its own version string")
	}
}

func TestRollingDisclosureForPicksTheRightPackage(t *testing.T) {
	mText, mVer := RollingDisclosureFor("monthly", 59976)
	if mVer != TermsVersionRollingMonthlyV1 || !contains(mText, "every 28 days") {
		t.Errorf("monthly interval got the wrong package: %s", mVer)
	}
	wText, wVer := RollingDisclosureFor("weekly", 14994)
	if wVer != TermsVersionRollingV3 || !contains(wText, "every 7 days") {
		t.Errorf("weekly interval got the wrong package: %s", wVer)
	}
	// An unknown interval must fall back to weekly, never to nothing.
	_, uVer := RollingDisclosureFor("fortnightly", 14994)
	if uVer != TermsVersionRollingV3 {
		t.Errorf("unknown interval fell through to %q", uVer)
	}
}
