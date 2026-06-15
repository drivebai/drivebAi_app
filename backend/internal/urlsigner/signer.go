// Package urlsigner produces and verifies HMAC-signed, time-limited URLs for
// private file downloads.
//
// Threat model: an authenticated user who is allowed to view a file (e.g. a
// chat attachment they're a participant in) gets a `path?sig=…&exp=…` URL
// back in the JSON response. The file-serving handler accepts only paths
// whose `sig` is a valid HMAC of `path|exp` and whose `exp` is in the future.
// Sharing the URL with someone outside the relationship only leaks access
// for `ttl` minutes; after that the file is unreachable without re-fetching
// the JSON parent — which the unauthorized user can't.
//
// This deliberately doesn't try to bind the URL to a specific viewer. That
// would require either auth-header-on-image-loads (which the iOS client
// doesn't do today for chat thumbnails) or per-user nonces stored server-side
// (an extra round-trip on every fetch). The TTL is the security boundary.
package urlsigner

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Signer signs and verifies private-file URLs. Zero value is unusable —
// always construct via New().
type Signer struct {
	secret []byte
}

// New returns a signer keyed off `secret`. Production callers should pass a
// long random string (e.g. 32+ bytes from `openssl rand -hex 32`). Passing an
// empty secret returns nil, which downstream code must treat as "signing not
// configured — refuse to issue private URLs".
func New(secret string) *Signer {
	if secret == "" {
		return nil
	}
	return &Signer{secret: []byte(secret)}
}

// Sign appends `?sig=&exp=` to `path` such that Verify will accept the
// returned URL until `time.Now().Add(ttl)`. `path` is expected to be the
// leading-slash relative URL (e.g. "/uploads/chats/<chat>/<file>"); any
// pre-existing query string is preserved.
func (s *Signer) Sign(path string, ttl time.Duration) string {
	expUnix := time.Now().Add(ttl).Unix()
	sig := s.compute(path, expUnix)
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%ssig=%s&exp=%d", path, sep, sig, expUnix)
}

// Verify confirms that `sigHex` and `expUnix` are a valid signature for
// `path` and that the deadline has not elapsed. `path` MUST be the bare
// path (no query string). Returns ErrInvalidSignature on mismatch and
// ErrExpired if `time.Now().Unix() > expUnix`.
func (s *Signer) Verify(path string, sigHex string, expUnix int64) error {
	if sigHex == "" || expUnix <= 0 {
		return ErrInvalidSignature
	}
	if time.Now().Unix() > expUnix {
		return ErrExpired
	}
	want := s.compute(path, expUnix)
	gotBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return ErrInvalidSignature
	}
	wantBytes, _ := hex.DecodeString(want)
	if !hmac.Equal(gotBytes, wantBytes) {
		return ErrInvalidSignature
	}
	return nil
}

// VerifyFromQuery is a convenience wrapper that pulls `sig`/`exp` out of
// the request's URL query. Returns the same sentinels as Verify.
func (s *Signer) VerifyFromQuery(path string, q url.Values) error {
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	return s.Verify(path, q.Get("sig"), exp)
}

func (s *Signer) compute(path string, expUnix int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(path))
	mac.Write([]byte{'|'})
	mac.Write([]byte(strconv.FormatInt(expUnix, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// ErrInvalidSignature is returned by Verify when the signature is malformed
// or doesn't match the expected HMAC. Callers should map this to 403/404.
var ErrInvalidSignature = errors.New("urlsigner: invalid signature")

// ErrExpired is returned by Verify when the signed URL's deadline has
// elapsed. Callers should map this to 403/404 — typically by refusing the
// download so the client refetches the parent JSON.
var ErrExpired = errors.New("urlsigner: expired")
