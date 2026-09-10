package models

import (
	"testing"
	"time"
)

// Monthly interval arithmetic. Every expectation here is an INDEPENDENT
// literal — computed by hand from the interval, never by calling the function
// under test — because an expectation produced by the code being tested
// proves only that the code is self-consistent.
func TestMonthlyProRataUsesTheWholeMonth(t *testing.T) {
	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	const monthCents int64 = 59976 // 4 x $149.94
	const days = 28
	// 59976 / 28 = 2142 exactly. Same per-day rate as the weekly car, which
	// is the point: a month is four weeks of the same rental.
	const perDay int64 = 2142

	cases := []struct {
		name       string
		usedDays   int
		wantRefund int64
	}{
		{"returned after 1 day", 1, monthCents - perDay*1},
		{"returned after 7 days", 7, monthCents - perDay*7},
		{"returned after 20 days", 20, monthCents - perDay*20},
		{"returned after 27 days", 27, monthCents - perDay*27},
		{"full month used", 28, 0},
		{"beyond the month", 35, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			returned := start.Add(time.Duration(c.usedDays) * 24 * time.Hour)
			got := ComputeReturnRefundOverDays(monthCents, days, start, returned)
			if got.RefundAmountCents != c.wantRefund {
				t.Errorf("refund = %d, want %d (used %d of %d days at %d/day)",
					got.RefundAmountCents, c.wantRefund, c.usedDays, days, perDay)
			}
			if got.PerDayCents != perDay {
				t.Errorf("per-day = %d, want %d", got.PerDayCents, perDay)
			}
		})
	}
}

// The bug this exists to prevent: settling a 28-day cycle through the WEEKLY
// form pro-rates it over 7 days, so a driver 20 days into a paid month is
// treated as having used the whole term and refunded nothing — or, on the
// arrears side, billed as though they owed a full month.
func TestMonthlySettledAsWeeklyIsWrongByDesign(t *testing.T) {
	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	const monthCents int64 = 59976
	returned := start.Add(20 * 24 * time.Hour)

	right := ComputeReturnRefundOverDays(monthCents, 28, start, returned)
	wrong := ComputeReturnRefund(monthCents, 1, start, returned) // the old call

	if right.RefundAmountCents != 59976-2142*20 {
		t.Fatalf("interval-aware refund = %d, want %d", right.RefundAmountCents, 59976-2142*20)
	}
	if wrong.RefundAmountCents != 0 {
		t.Fatalf("expected the weekly form to refund nothing at 20 days, got %d", wrong.RefundAmountCents)
	}
	if right.RefundAmountCents <= wrong.RefundAmountCents {
		t.Error("the interval-aware form must refund MORE than the weekly one here")
	}
	t.Logf("  20 days into a paid month: correct refund %d¢, weekly form would refund %d¢ — a %d¢ error against the driver",
		right.RefundAmountCents, wrong.RefundAmountCents, right.RefundAmountCents-wrong.RefundAmountCents)
}

// Fixed-term rentals must be untouched by any of this.
func TestFixedTermRefundUnchanged(t *testing.T) {
	pickup := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	// One week at $149.94: 14994/7 = 2142/day exactly.
	got := ComputeReturnRefund(14994, 1, pickup, pickup.Add(3*24*time.Hour))
	if got.PerDayCents != 2142 || got.RefundAmountCents != 14994-2142*3 {
		t.Errorf("fixed-term one-week refund changed: per-day %d, refund %d", got.PerDayCents, got.RefundAmountCents)
	}
	// Two weeks: the weeks multiplier still governs.
	got2 := ComputeReturnRefund(29988, 2, pickup, pickup.Add(5*24*time.Hour))
	if got2.PerDayCents != 2142 {
		t.Errorf("fixed-term two-week per-day = %d, want 2142", got2.PerDayCents)
	}
	// Full term still refunds exactly zero, the six-cent bug.
	full := ComputeReturnRefund(14994, 1, pickup, pickup.Add(7*24*time.Hour))
	if full.RefundAmountCents != 0 {
		t.Errorf("full-term refund = %d, want exactly 0", full.RefundAmountCents)
	}
}

func TestIntervalLeadsAndLengths(t *testing.T) {
	if BillingIntervalLength("monthly") != 28*24*time.Hour {
		t.Error("monthly is not 28 days")
	}
	if BillingIntervalLength("weekly") != 7*24*time.Hour {
		t.Error("weekly is not 7 days")
	}
	if BillingIntervalNoticeLead("monthly") != 72*time.Hour {
		t.Error("monthly notice lead is not 72h")
	}
	if BillingIntervalNoticeLead("weekly") != BillingNoticeLead {
		t.Error("weekly notice lead changed")
	}
	// An unknown interval must fall back to weekly, never to zero.
	if BillingIntervalNoticeLead("quarterly") != BillingNoticeLead ||
		BillingIntervalLength("quarterly") != BillingCycleLength {
		t.Error("an unknown interval did not fall back to weekly")
	}
}

// Exposure under the owner guarantee scales with the interval: a guaranteed
// MONTH is four weeks of platform money, and the break-even rate does not
// move because both sides scale together.
func TestMonthlyGuaranteeExposureIsFourTimesWeekly(t *testing.T) {
	const weekly int64 = 14994
	const monthly int64 = 59976
	_, weeklyOwner := ComputePayoutSplit(weekly, 1000)
	_, monthlyOwner := ComputePayoutSplit(monthly, 1000)

	// Four weeks and one month are the same money to within the rounding
	// remainder: the fee is floored once per charge, so charging monthly
	// rounds once instead of four times. That is a real difference and it is
	// worth knowing which way it falls.
	diff := monthlyOwner - weeklyOwner*4
	if diff > 4 || diff < -4 {
		t.Errorf("monthly owner share %d differs from 4x weekly %d by %d — more than rounding explains",
			monthlyOwner, weeklyOwner*4, diff)
	}
	t.Logf("  four weekly charges pay the owner %d¢; one monthly charge pays %d¢ (%+d¢ from rounding once instead of four times)",
		weeklyOwner*4, monthlyOwner, diff)
	// Break-even is unchanged: a guaranteed period pays out 90% while a
	// collected one earns 10%, whatever the period length. Nine collected
	// periods fund one guarantee either way.
	weeklyFee, _ := ComputePayoutSplit(weekly, 1000)
	monthlyFee, _ := ComputePayoutSplit(monthly, 1000)
	// The ratio that sets break-even: owner share funded by fee income. It
	// must not move with the interval, or the guarantee's 10% break-even
	// would silently mean something different for monthly rentals.
	weeklyRatio := float64(weeklyOwner) / float64(weeklyFee)
	monthlyRatio := float64(monthlyOwner) / float64(monthlyFee)
	if d := weeklyRatio - monthlyRatio; d > 0.01 || d < -0.01 {
		t.Errorf("payout-to-fee ratio moved with the interval: weekly %.4f vs monthly %.4f — break-even shifts",
			weeklyRatio, monthlyRatio)
	}
	t.Logf("  guaranteed week costs %d¢, guaranteed month costs %d¢ (4x); break-even stays at one in nine",
		weeklyOwner, monthlyOwner)
}
