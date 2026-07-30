package models

import (
	"regexp"
	"strings"
)

// Phone normalization (client item A3). Storage format is E.164
// ("+13475551234"): one canonical shape, matching the partial unique index
// users_phone_unique_idx (migration 000041) exactly — validation here and the
// index predicate must stay in lockstep, the same three-place discipline as
// the VIN index.

var e164Pattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// phoneJunk strips the separators people type: spaces, dashes, dots, parens.
var phoneJunk = strings.NewReplacer(" ", "", "-", "", ".", "", "(", "", ")", "")

// NormalizePhone canonicalizes user phone input to E.164. Returns the
// normalized value and whether it is valid. The empty string is NOT valid —
// callers decide whether an empty phone is allowed (it is optional at
// signup) and must skip the call for empties.
func NormalizePhone(raw string) (string, bool) {
	p := phoneJunk.Replace(strings.TrimSpace(raw))
	// Accept the common "00" international prefix as a courtesy.
	if strings.HasPrefix(p, "00") {
		p = "+" + p[2:]
	}
	if !e164Pattern.MatchString(p) {
		return "", false
	}
	return p, true
}
