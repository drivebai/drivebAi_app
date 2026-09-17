package models

import (
	"testing"
	"time"
)

// Recurring-only (2026-09-17): the interval model. The engine already keyed
// cycle length and leads on the consent's interval; these pin the new pieces
// — listing period → interval, the monthly cycle amount, and monthly
// proration on the SAME per-day formula the weekly path uses.

func TestBillingIntervalForRentPeriod(t *testing.T) {
	cases := []struct {
		period string
		want   string
		ok     bool
	}{
		{"weekly", "weekly", true},
		{"", "weekly", true}, // legacy listings default to weekly
		{"monthly", "monthly", true},
		{"daily", "", false}, // refused, never silently weekly
		{"hourly", "", false},
	}
	for _, c := range cases {
		got, ok := BillingIntervalForRentPeriod(c.period)
		if got != c.want || ok != c.ok {
			t.Errorf("period %q → (%q,%v), want (%q,%v)", c.period, got, ok, c.want, c.ok)
		}
	}
	if IntervalUnit("monthly") != "month" || IntervalUnit("weekly") != "week" || IntervalLabel("monthly") != "monthly" {
		t.Error("interval copy helpers disagree with the interval")
	}
}

func TestMonthlyCycleAmountIsTheOwnersTypedMonth(t *testing.T) {
	// Listing typed $600/month → derived weekly 150.00 → one monthly cycle
	// must be exactly 60000 cents, not a rounded weekly ×4.3.
	lr := &LeaseRequest{WeeklyPrice: 150.00, Weeks: 1, BillingMode: BillingModeRolling, BillingInterval: "monthly"}
	if got := lr.IntervalAmountCents(); got != 60000 {
		t.Fatalf("monthly cycle = %d, want 60000", got)
	}
	if got := lr.IntervalPrice(); got != 600.00 {
		t.Fatalf("IntervalPrice = %.2f, want 600.00", got)
	}
	// An agreed weekly offer converts the same way.
	offer := 100.00
	lr.OfferedWeeklyPrice = &offer
	if got := lr.IntervalAmountCents(); got != 40000 {
		t.Fatalf("offered 100/wk → monthly cycle = %d, want 40000", got)
	}
	// Weekly is unchanged: identical to TotalAmountCents with weeks=1.
	w := &LeaseRequest{WeeklyPrice: 150.00, Weeks: 1, BillingMode: BillingModeRolling, BillingInterval: "weekly"}
	if w.IntervalAmountCents() != w.TotalAmountCents() {
		t.Fatalf("weekly cycle %d != TotalAmountCents %d", w.IntervalAmountCents(), w.TotalAmountCents())
	}
	if IntervalPriceFromWeekly(150, "monthly") != 600 {
		t.Errorf("IntervalPriceFromWeekly(150, monthly) = %v", IntervalPriceFromWeekly(150, "monthly"))
	}
}

// The case the task names: a 28-day cycle, the car comes back on day 20.
// Per-day = floor(paid / 28); the driver used 20 days; refund = 8 × per-day
// plus the rounding remainder (paid mod 28) — the same rule the weekly path
// applies, so the two intervals can never disagree on a partial refund.
func TestMonthlyProrationTwentyOfTwentyEightDays(t *testing.T) {
	start := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	end := start.Add(BillingIntervalLength("monthly"))
	if days := DaysInPeriod(start, end); days != 28 {
		t.Fatalf("DaysInPeriod(monthly) = %d, want 28", days)
	}
	paid := int64(60000) // $600.00
	returned := start.Add(20 * 24 * time.Hour)
	calc := ComputeReturnRefundOverDays(paid, 28, start, returned)

	perDay := paid / 28 // 2142 (floor)
	remainder := paid % 28
	wantRefund := perDay*8 + remainder
	if calc.RefundAmountCents != wantRefund {
		t.Fatalf("refund = %d, want %d (per-day %d × 8 unused + %d remainder)", calc.RefundAmountCents, wantRefund, perDay, remainder)
	}
	// Money identity: what the owner keeps + what the driver gets back = paid.
	if kept := paid - calc.RefundAmountCents; kept != perDay*20 {
		t.Fatalf("owner keeps %d, want %d (20 used days)", kept, perDay*20)
	}

	// Return on day 20 at 00:01 past the boundary still counts day 21 (ceil).
	late := start.Add(20*24*time.Hour + time.Minute)
	if got := ComputeReturnRefundOverDays(paid, 28, start, late).RefundAmountCents; got != perDay*7+remainder {
		t.Errorf("20d+1min → refund %d, want %d (21 used days)", got, perDay*7+remainder)
	}
	// Full month used → refund exactly 0.
	if got := ComputeReturnRefundOverDays(paid, 28, start, end).RefundAmountCents; got != 0 {
		t.Errorf("full month → refund %d, want 0", got)
	}
}

func TestMonthlyLeadsAndLength(t *testing.T) {
	if BillingIntervalLength("monthly") != 28*24*time.Hour {
		t.Error("monthly cycle is not 28 days")
	}
	if BillingIntervalNoticeLead("monthly") != 144*time.Hour || BillingIntervalChargeLead("monthly") != 72*time.Hour {
		t.Error("monthly leads are not 144h notice / 72h charge")
	}
	// The consent text promises a reminder BEFORE the charge, for both
	// intervals. If these ever meet, the notice and the charge land in the
	// same sweep (review 2026-09-17).
	for _, iv := range []string{"weekly", "monthly"} {
		if BillingIntervalNoticeLead(iv) <= BillingIntervalChargeLead(iv) {
			t.Errorf("%s: notice lead %v is not ahead of charge lead %v", iv, BillingIntervalNoticeLead(iv), BillingIntervalChargeLead(iv))
		}
	}
	if BillingIntervalLength("weekly") != BillingCycleLength || BillingIntervalNoticeLead("weekly") != BillingNoticeLead {
		t.Error("weekly interval no longer maps to the weekly constants")
	}
}
