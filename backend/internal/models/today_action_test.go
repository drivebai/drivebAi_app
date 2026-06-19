package models

import "testing"

// Wire-value pins for TodayActionType. iOS clients switch on these strings
// to decide rendering (single CTA vs Accept/Decline), so a drift here would
// silently break the Today screen on shipped builds.
func TestTodayActionType_WireValues(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{string(TodayActionLeaseRequest), "lease_request"},
		{string(TodayActionLeasePayment), "lease_payment"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("wire-value drift: got %q want %q", c.got, c.want)
		}
	}
}
