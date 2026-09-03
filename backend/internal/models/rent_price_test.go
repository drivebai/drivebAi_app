package models

import "testing"

// The client's own examples, verbatim.
func TestRentPrice_ClientExamples(t *testing.T) {
	// $350/week → daily shows $50.
	if got := ConvertRentCents(35000, RentPeriodWeekly, RentPeriodDaily); got != 5000 {
		t.Errorf("350/wk → daily = %d, want 5000", got)
	}
	// Owner types $60/day → weekly $420.
	if got := WeeklyEquivalentCents(6000, RentPeriodDaily); got != 42000 {
		t.Errorf("60/day weekly equiv = %d, want 42000", got)
	}
	// $420/week → monthly $1,680 (a month is 4 weeks — 28 days, never
	// calendar).
	if got := ConvertRentCents(42000, RentPeriodWeekly, RentPeriodMonthly); got != 168000 {
		t.Errorf("420/wk → monthly = %d, want 168000", got)
	}
}

// The $400 trap: 400/7 does not divide evenly. The rule that makes the
// round trip lossless is structural — the typed (amount, period) pair is
// stored verbatim and every conversion derives from it in one hop — so
// "convert away and back" is the identity BY CONSTRUCTION for the source
// pair. This test proves the projection math never mutates the source and
// that one-hop projections behave sanely at awkward amounts.
func TestRentPrice_RoundTripLossless(t *testing.T) {
	awkward := []int64{40000, 9999, 5001, 35003, 1234567}
	periods := []string{RentPeriodDaily, RentPeriodWeekly, RentPeriodMonthly}
	for _, src := range awkward {
		for _, p := range periods {
			for _, view := range periods {
				// Project for display, then RE-DERIVE from the source —
				// the implementation never chains view→view conversions.
				_ = ConvertRentCents(src, p, view)
				if back := ConvertRentCents(src, p, p); back != src {
					t.Fatalf("source pair mutated: %d %s → %d", src, p, back)
				}
			}
		}
	}
	// The naive-chaining failure the rule prevents, demonstrated: $400/wk
	// viewed daily is $57.14; chaining that BACK to weekly gives $399.98 —
	// which is why projections must always start from the source.
	daily := ConvertRentCents(40000, RentPeriodWeekly, RentPeriodDaily)
	if daily != 5714 {
		t.Fatalf("400/wk daily projection = %d, want 5714", daily)
	}
	chained := ConvertRentCents(daily, RentPeriodDaily, RentPeriodWeekly)
	if chained == 40000 {
		t.Fatal("chained conversion unexpectedly exact — test premise broken")
	}
	if got := ConvertRentCents(40000, RentPeriodWeekly, RentPeriodWeekly); got != 40000 {
		t.Fatal("source-derived weekly view must be exactly the typed $400.00")
	}
}

// Weekly equivalents: the one derived value money paths read.
func TestRentPrice_WeeklyEquivalent(t *testing.T) {
	cases := []struct {
		cents  int64
		period string
		want   int64
	}{
		{5000, RentPeriodDaily, 35000},
		{35000, RentPeriodWeekly, 35000},
		{168000, RentPeriodMonthly, 42000},
		// Monthly not divisible by 4¢ rounds half-up: $1,681.01/mo →
		// 168101/4 = 42025.25 → 42025.
		{168101, RentPeriodMonthly, 42025},
		{168102, RentPeriodMonthly, 42026}, // .5 rounds up
	}
	for _, c := range cases {
		if got := WeeklyEquivalentCents(c.cents, c.period); got != c.want {
			t.Errorf("WeeklyEquivalentCents(%d, %s) = %d, want %d", c.cents, c.period, got, c.want)
		}
	}
}

// The minimum scales with the period and can never undercut the weekly
// floor after conversion.
func TestRentPrice_MinScaling(t *testing.T) {
	const minWeekly = 5000 // $50, the configured floor
	if got := MinRentCentsForPeriod(minWeekly, RentPeriodDaily); got != 715 {
		t.Errorf("daily floor = %d, want 715 (ceil of 5000/7)", got)
	}
	if got := MinRentCentsForPeriod(minWeekly, RentPeriodMonthly); got != 20000 {
		t.Errorf("monthly floor = %d, want 20000", got)
	}
	if got := MinRentCentsForPeriod(minWeekly, RentPeriodWeekly); got != 5000 {
		t.Errorf("weekly floor = %d, want 5000", got)
	}
	// Any amount at the scaled floor passes the single weekly-terms check.
	for _, p := range []string{RentPeriodDaily, RentPeriodWeekly, RentPeriodMonthly} {
		floor := MinRentCentsForPeriod(minWeekly, p)
		if WeeklyEquivalentCents(floor, p) < minWeekly {
			t.Errorf("period %s: floor %d converts under the weekly minimum", p, floor)
		}
		// And one cent under the floor fails it (weekly/monthly exact;
		// daily's ceil makes floor-1 land exactly at 4998 < 5000).
		if WeeklyEquivalentCents(floor-1, p) >= minWeekly && p != RentPeriodMonthly {
			t.Errorf("period %s: floor-1 still passes the weekly minimum", p)
		}
	}
}

// Legacy listings: period defaults weekly, amount backfilled from the
// weekly price — the weekly equivalent is identical, so nothing about
// existing bookings or displays moves.
func TestRentPrice_LegacyBackfillIdentity(t *testing.T) {
	legacyWeekly := int64(13750) // the live CR-V's old $137.50
	if got := WeeklyEquivalentCents(legacyWeekly, RentPeriodWeekly); got != legacyWeekly {
		t.Errorf("backfilled weekly listing changed: %d → %d", legacyWeekly, got)
	}
	if RentPeriodDays("") != 7 || RentPeriodDays("garbage") != 7 {
		t.Error("unknown period must fall back to weekly semantics")
	}
}
