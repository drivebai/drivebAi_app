package handlers

import "testing"

// Pins the stale-after constant so a future refactor doesn't accidentally
// drop the retry sweep cadence (too eager → competes with the in-flight
// first attempt; too lazy → dangling refunds linger). 2 min is the balance.
func TestStuckRefundStaleAfter(t *testing.T) {
	if stuckRefundStaleAfter.Minutes() != 2 {
		t.Errorf("stuckRefundStaleAfter drift: got %v, want 2m", stuckRefundStaleAfter)
	}
}

func TestStrOrEmpty(t *testing.T) {
	s := "succeeded"
	cases := []struct {
		in   *string
		want string
	}{
		{nil, ""},
		{&s, "succeeded"},
	}
	for _, c := range cases {
		if got := strOrEmpty(c.in); got != c.want {
			t.Errorf("strOrEmpty(%v): got %q want %q", c.in, got, c.want)
		}
	}
}
