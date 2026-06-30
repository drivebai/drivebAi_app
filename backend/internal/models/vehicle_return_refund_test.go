package models

import (
	"testing"
	"time"
)

// TestComputeReturnRefund walks the worked examples from the spec plus the
// extra edge cases the handler relies on (clock skew, zero-paid, ceiling
// rounding, sub-cent results, late return).
func TestComputeReturnRefund(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name              string
		paidCents         int64
		rentalWeeks       int
		pickup            time.Time
		returnedAt        time.Time
		wantUsedDays      int
		wantRefundCents   int64
		wantNotApplicable bool
	}{
		// Same-minute return → floor at 1 day so refund != full amount.
		{
			name:            "same_day_12min_return",
			paidCents:       8000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(12 * time.Minute),
			wantUsedDays:    1,
			wantRefundCents: 6858, // 8000 - (8000/7=1142)*1 = 6858
		},
		// 3 days 5 hours → ceil to 4 days.
		{
			name:            "3.2_days_ceils_to_4",
			paidCents:       10000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(3*24*time.Hour + 5*time.Hour),
			wantUsedDays:    4,
			wantRefundCents: 4288, // 10000 - (10000/7=1428)*4 = 4288
		},
		// Returned at exactly 7 days (no leftover) → used_days=7, refund crumb.
		{
			name:            "exact_7_days_no_refund",
			paidCents:       10000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(7 * 24 * time.Hour),
			wantUsedDays:    7,
			wantRefundCents: 4, // 10000 - 1428*7 = 4 (cent crumb)
		},
		// Returned 8 days into a 7-day rental → capped at 7, refund == crumb.
		{
			name:            "8_days_late_capped_at_paid_window",
			paidCents:       10000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(8 * 24 * time.Hour),
			wantUsedDays:    7,
			wantRefundCents: 4,
		},
		// Spec example #3: 5 days into a 2-week lease.
		{
			name:            "2_week_5_days",
			paidCents:       30000,
			rentalWeeks:     2,
			pickup:          now,
			returnedAt:      now.Add(5 * 24 * time.Hour),
			wantUsedDays:    5,
			wantRefundCents: 19290, // 30000 - (30000/14=2142)*5 = 19290
		},
		// Spec example #4: 10d12h → ceil 11 days, 4 weeks, $200/week.
		{
			name:            "4_week_10d12h_ceils_to_11",
			paidCents:       80000,
			rentalWeeks:     4,
			pickup:          now,
			returnedAt:      now.Add(10*24*time.Hour + 12*time.Hour),
			wantUsedDays:    11,
			wantRefundCents: 48573, // 80000 - (80000/28=2857)*11 = 48573
		},
		// Spec example #6: 16 days into a 2-week lease (late return).
		{
			name:            "2_week_late_16_days",
			paidCents:       20000,
			rentalWeeks:     2,
			pickup:          now,
			returnedAt:      now.Add(16 * 24 * time.Hour),
			wantUsedDays:    14, // capped
			wantRefundCents: 8,  // 20000 - (20000/14=1428)*14 = 8
		},
		// Promo / $0 lease — Stripe skipped, marked not_applicable.
		{
			name:              "zero_paid_amount",
			paidCents:         0,
			rentalWeeks:       1,
			pickup:            now,
			returnedAt:        now.Add(2 * 24 * time.Hour),
			wantUsedDays:      2,
			wantRefundCents:   0,
			wantNotApplicable: true,
		},
		// rental_weeks=0 (defensive) — treated as 1 week.
		{
			name:            "weeks_zero_defaults_to_one",
			paidCents:       7000,
			rentalWeeks:     0,
			pickup:          now,
			returnedAt:      now.Add(2 * 24 * time.Hour),
			wantUsedDays:    2,
			wantRefundCents: 5000, // 7000 - (7000/7=1000)*2 = 5000
		},
		// Negative duration (clock skew) → clamped to 0 elapsed, floored to 1.
		{
			name:            "negative_duration_clamped",
			paidCents:       10000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(-3 * time.Hour),
			wantUsedDays:    1,
			wantRefundCents: 8572, // 10000 - 1428*1
		},
		// Ceiling boundary: exactly 24h → 1 day (not 2).
		{
			name:            "exactly_24h_stays_one_day",
			paidCents:       10000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(24 * time.Hour),
			wantUsedDays:    1,
			wantRefundCents: 8572,
		},
		// Ceiling boundary: 24h + 1s → 2 days.
		{
			name:            "24h_plus_one_second_ceils_to_two",
			paidCents:       10000,
			rentalWeeks:     1,
			pickup:          now,
			returnedAt:      now.Add(24*time.Hour + 1*time.Second),
			wantUsedDays:    2,
			wantRefundCents: 7144, // 10000 - 1428*2
		},
		// Spec example #8: exact 3-week return = 21 days, refund collapses to
		// $0 (the per-day cents land exactly), so the row is marked
		// not_applicable (sub-cent ⇒ Stripe call skipped).
		{
			name:              "3_week_exact_21_days_zero_refund",
			paidCents:         52500,
			rentalWeeks:       3,
			pickup:            now,
			returnedAt:        now.Add(21 * 24 * time.Hour),
			wantUsedDays:      21,
			wantRefundCents:   0, // 52500 - (52500/21=2500)*21 = 0
			wantNotApplicable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeReturnRefund(tc.paidCents, tc.rentalWeeks, tc.pickup, tc.returnedAt)
			if got.UsedDays != tc.wantUsedDays {
				t.Errorf("used_days = %d, want %d", got.UsedDays, tc.wantUsedDays)
			}
			if got.RefundAmountCents != tc.wantRefundCents {
				t.Errorf("refund_cents = %d, want %d", got.RefundAmountCents, tc.wantRefundCents)
			}
			if got.NotApplicable != tc.wantNotApplicable {
				t.Errorf("not_applicable = %v, want %v", got.NotApplicable, tc.wantNotApplicable)
			}
			if got.RefundAmountCents > tc.paidCents {
				t.Errorf("refund (%d) exceeds paid (%d)", got.RefundAmountCents, tc.paidCents)
			}
		})
	}
}

// TestComputeReturnRefund_SubCentMarksNotApplicable exercises the rare case
// where the formula leaves a sub-cent crumb that Stripe would reject.
func TestComputeReturnRefund_SubCentMarksNotApplicable(t *testing.T) {
	// 7-day lease at exactly 7000¢ → per_day=1000¢, return at 7d → refund=0¢
	// → NotApplicable=true.
	now := time.Now().UTC()
	got := ComputeReturnRefund(7000, 1, now, now.Add(7*24*time.Hour))
	if got.RefundAmountCents != 0 {
		t.Errorf("expected zero refund, got %d", got.RefundAmountCents)
	}
	if !got.NotApplicable {
		t.Errorf("expected not_applicable=true for zero refund")
	}
}
