package models

import "math"

// Rent pricing periods (client request, Part B). The owner types a number
// in ONE unit; that (amount, period) pair is the source of truth. The
// weekly equivalent is DERIVED from it — once, at write time — and remains
// the canonical booking price every money path reads. Conversions for
// display are always re-derived from the source pair, never chained, which
// is what makes unit switching provably lossless (see rent_price_test.go).
const (
	RentPeriodDaily   = "daily"
	RentPeriodWeekly  = "weekly"
	RentPeriodMonthly = "monthly"
)

// RentMonthWeeks: a pricing MONTH is exactly 4 weeks (28 days) — the
// client's own arithmetic ($420/wk × 4 = $1,680/mo), never a calendar
// month. THE single definition; every conversion and every helper-text
// floor goes through the period tables below.
const RentMonthWeeks = 4

// RentPeriodDays returns the length of a pricing period in days.
// Unknown periods fall back to weekly — the pre-migration behavior.
func RentPeriodDays(period string) int {
	switch period {
	case RentPeriodDaily:
		return 1
	case RentPeriodMonthly:
		return 7 * RentMonthWeeks
	default:
		return 7
	}
}

// ValidRentPeriod reports whether p is one of the three pricing periods.
func ValidRentPeriod(p string) bool {
	return p == RentPeriodDaily || p == RentPeriodWeekly || p == RentPeriodMonthly
}

// WeeklyEquivalentCents converts an owner-typed price (in cents, in their
// chosen period) to the canonical weekly booking price. Daily and weekly
// scale up exactly (×7, ×1); monthly divides by 4 and rounds HALF-UP to
// the cent (the only case where a remainder can exist: a monthly price
// not divisible by 4¢). The derived value is a projection — the owner's
// typed pair stays stored verbatim, so no precision is ever lost.
func WeeklyEquivalentCents(amountCents int64, period string) int64 {
	switch period {
	case RentPeriodDaily:
		return amountCents * 7
	case RentPeriodMonthly:
		return int64(math.Round(float64(amountCents) / float64(RentMonthWeeks)))
	default:
		return amountCents
	}
}

// ConvertRentCents re-expresses a price between periods, always in one
// hop: cents-per-day scaling with half-up rounding at the end. Used for
// DISPLAY projections (e.g. the create-listing dropdown pre-filling the
// converted number); the stored source pair is never overwritten by a
// projection.
func ConvertRentCents(amountCents int64, fromPeriod, toPeriod string) int64 {
	if fromPeriod == toPeriod {
		return amountCents
	}
	from := float64(RentPeriodDays(fromPeriod))
	to := float64(RentPeriodDays(toPeriod))
	return int64(math.Round(float64(amountCents) * to / from))
}

// MinRentCentsForPeriod scales the configured weekly floor to the selected
// period, rounding UP to the cent so a period-converted price can never
// sneak under the weekly minimum: daily floor = ceil(weekly/7), monthly
// floor = weekly × 4. Enforcement itself happens once, in weekly terms
// (WeeklyEquivalentCents(amount) >= weekly minimum); this helper only
// drives UI copy and pre-checks.
func MinRentCentsForPeriod(minWeeklyCents int64, period string) int64 {
	switch period {
	case RentPeriodDaily:
		return int64(math.Ceil(float64(minWeeklyCents) / 7.0))
	case RentPeriodMonthly:
		return minWeeklyCents * RentMonthWeeks
	default:
		return minWeeklyCents
	}
}

// RentPeriodLabel is the display suffix for a period ("/day", "/week",
// "/month"). A price without its unit is a bug — every surface shows this.
func RentPeriodLabel(period string) string {
	switch period {
	case RentPeriodDaily:
		return "day"
	case RentPeriodMonthly:
		return "month"
	default:
		return "week"
	}
}
