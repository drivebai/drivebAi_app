package models

import "testing"

// Pins NormalizePhone to the users_phone_unique_idx predicate (migration
// 000041): everything this function accepts must match the index's regex,
// and common human formatting must normalize rather than reject.
func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"+13475551234", "+13475551234", true},
		{"+1 (347) 555-1234", "+13475551234", true},
		{"+1 347.555.1234", "+13475551234", true},
		{"  +44 20 7946 0958 ", "+442079460958", true},
		{"0013475551234", "+13475551234", true}, // 00 international prefix
		{"+77029871591", "+77029871591", true},
		{"+1234567", "+1234567", true},           // minimum length (7 digits after +)
		{"+123456", "", false},                   // too short
		{"+1234567890123456", "", false},         // too long (>15)
		{"3475551234", "", false},                // no country code
		{"+0475551234", "", false},               // leading zero country code
		{"abc", "", false},
		{"", "", false},
		{"+1-abc-555", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizePhone(tc.in)
		if ok != tc.valid || got != tc.want {
			t.Errorf("NormalizePhone(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.valid)
		}
	}
}
