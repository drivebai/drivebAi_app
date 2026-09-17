package httputil

import (
	"net/http"
	"strconv"
	"strings"
)

// ClientBuild returns the iOS build number from a User-Agent of the form
// "DriveBai/<build> CFNetwork/… Darwin/…" — the one header that names the
// build. 0 means unknown (curl, the admin console, a foreign client), and
// callers must treat unknown as "not proven old", never as old.
func ClientBuild(r *http.Request) int {
	ua := r.UserAgent()
	const prefix = "DriveBai/"
	i := strings.Index(ua, prefix)
	if i < 0 {
		return 0
	}
	rest := ua[i+len(prefix):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
