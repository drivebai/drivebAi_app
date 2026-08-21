package models

import (
	"testing"
	"time"
)

// Boundary tests for the rental-term computation, mirroring the defensive
// style (and the test discipline) of ComputeReturnRefund: every clamp gets
// a case, including the ones that "cannot happen".

func TestRentalEndsAt(t *testing.T) {
	pickup := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if got := RentalEndsAt(pickup, 2); !got.Equal(pickup.AddDate(0, 0, 14)) {
		t.Errorf("2 weeks: got %v", got)
	}
	// Defensive floor: weeks <= 0 treated as 1 (mirrors ComputeReturnRefund).
	if got := RentalEndsAt(pickup, 0); !got.Equal(pickup.AddDate(0, 0, 7)) {
		t.Errorf("0 weeks must floor to 1: got %v", got)
	}
	if got := RentalEndsAt(pickup, -3); !got.Equal(pickup.AddDate(0, 0, 7)) {
		t.Errorf("negative weeks must floor to 1: got %v", got)
	}
}

func TestComputeRentalTermState(t *testing.T) {
	end := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		now  time.Time
		want RentalTermState
	}{
		{"well before the window", end.Add(-48 * time.Hour), RentalTermActive},
		{"exactly at window edge (T-24h)", end.Add(-24 * time.Hour), RentalTermActive},
		{"one second inside the window", end.Add(-24*time.Hour + time.Second), RentalTermEndingSoon},
		{"one second before the end", end.Add(-time.Second), RentalTermEndingSoon},
		// Inclusive end: a rental whose end falls EXACTLY on the scan tick
		// is overdue — the scanner's `rental_ends_at <= $1` must agree.
		{"exactly at the end", end, RentalTermOverdue},
		{"past the end", end.Add(time.Hour), RentalTermOverdue},
		{"way past the end", end.Add(30 * 24 * time.Hour), RentalTermOverdue},
	}
	for _, c := range cases {
		if got := ComputeRentalTermState(end, c.now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}

	// A zero end time (term never stamped) must read active, never overdue —
	// the scanner keys on the column being present, not on this bucket.
	if got := ComputeRentalTermState(time.Time{}, end); got != RentalTermActive {
		t.Errorf("zero end time must be active, got %s", got)
	}
}

func TestRentalDaysRemaining(t *testing.T) {
	end := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		now  time.Time
		want int
	}{
		// Ceil on the remaining side: 26h left reads as 2 days.
		{"26 hours left", end.Add(-26 * time.Hour), 2},
		{"exactly 24h left", end.Add(-24 * time.Hour), 1},
		{"one hour left (same day)", end.Add(-time.Hour), 1},
		{"exactly at the end", end, 0},
		// Negative once overdue: −1 = up to 24h past due.
		{"an hour overdue", end.Add(time.Hour), -1},
		{"exactly 24h overdue", end.Add(24 * time.Hour), -1},
		{"25 hours overdue", end.Add(25 * time.Hour), -2},
	}
	for _, c := range cases {
		if got := RentalDaysRemaining(end, c.now); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}

	if got := RentalDaysRemaining(time.Time{}, end); got != 0 {
		t.Errorf("zero end time must report 0 days, got %d", got)
	}
}

// Clock skew: a pickup stamped "in the future" relative to the scanner's
// clock must not produce an instant overdue — the ending-soon window opens
// only as the real end approaches.
func TestRentalTerm_ClockSkew(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	futurePickup := now.Add(5 * time.Minute) // skewed writer
	end := RentalEndsAt(futurePickup, 1)

	if got := ComputeRentalTermState(end, now); got != RentalTermActive {
		t.Errorf("skewed pickup must read active, got %s", got)
	}
	if days := RentalDaysRemaining(end, now); days < 7 || days > 8 {
		t.Errorf("skewed pickup days remaining out of range: %d", days)
	}
}
