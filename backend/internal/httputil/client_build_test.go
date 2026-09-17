package httputil

import (
	"net/http/httptest"
	"testing"
)

func TestClientBuildParsesTheAppUserAgent(t *testing.T) {
	cases := []struct {
		ua   string
		want int
	}{
		{"DriveBai/41 CFNetwork/3860.500.112 Darwin/25.4.0", 41},
		{"DriveBai/13 CFNetwork/3860.300.31 Darwin/25.4.0", 13},
		{"DriveBai/40", 40},
		{"", 0},
		{"curl/8.4.0", 0},
		{"Mozilla/5.0 (admin console)", 0},
		{"DriveBai/", 0},
		{"DriveBai/abc", 0},
		{"DriveBai/0 CFNetwork/1", 0},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.ua != "" {
			r.Header.Set("User-Agent", c.ua)
		}
		if got := ClientBuild(r); got != c.want {
			t.Errorf("%q → %d, want %d", c.ua, got, c.want)
		}
	}
}
