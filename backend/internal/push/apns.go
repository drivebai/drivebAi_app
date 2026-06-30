// Package push sends Apple Push Notification service (APNs) alerts.
// All sending is best-effort — a push failure never affects the in-app
// notification that was already stored in Postgres.
//
// Required env vars (all must be non-empty to enable push):
//
//	APPLE_TEAM_ID        — 10-char Apple developer Team ID
//	APNS_KEY_ID          — 10-char key identifier for the .p8 auth key
//	APNS_AUTH_KEY_P8     — base64-encoded contents of the .p8 key file
//	IOS_BUNDLE_ID        — e.g. com.drivebai.app
//
// When any of these are absent the service is a no-op and logs once at init.
package push

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenInvalidator is what the push Service calls when APNs tells us a
// device token is dead (410 Unregistered or 400 BadDeviceToken). The
// handler layer satisfies this with the DeviceTokenRepository so the
// row is pruned and the user's future pushes go only to live devices.
//
// Interface (rather than a concrete repo dep) keeps the push package
// from importing the repository package, which would create a cycle.
type TokenInvalidator interface {
	DeleteByToken(token string) error
}

// Service sends APNs push notifications via HTTP/2 + JWT auth.
type Service struct {
	teamID      string
	keyID       string
	key         *ecdsa.PrivateKey
	bundleID    string
	sandbox     bool
	logger      *slog.Logger
	invalidator TokenInvalidator

	mu          sync.Mutex
	cachedToken string
	tokenExp    time.Time

	httpClient *http.Client
}

// PushRequest is the per-call payload contract used by callers (the
// NotificationHandler dispatcher). Renders into APNs JSON + headers.
type PushRequest struct {
	Title    string
	Body     string
	Sound    string // defaults to "default" when empty
	Badge    *int   // nil → no badge key (don't reset the springboard count)
	Category string // APNs category — drives iOS notification actions
	ThreadID string // groups related notifications in Notification Center
	// CollapseID maps to apns-collapse-id so a stream of updates about the
	// same lease/chat collapses to one banner instead of stacking.
	CollapseID string
	// Priority must be 5 (energy-saving) or 10 (immediate). Defaults to 10
	// for alert pushes; silent/background pushes should pass 5.
	Priority int
	// Expiration: dropped after this time. Zero means "deliver once or
	// drop"; non-zero gets converted to the apns-expiration epoch header.
	Expiration time.Time
	// ContentAvailable=true sends a silent background push (no alert).
	// Used for future unread-refresh; current alert pushes leave this false.
	ContentAvailable bool
	// Data is the deep-link payload merged at the top level of the APNs
	// JSON (sibling of "aps"). iOS reads these keys to route on tap.
	Data map[string]string
	// IsSandbox selects api.sandbox.push.apple.com vs api.push.apple.com.
	// Authoritative source is the per-token sandbox flag stored in DB.
	IsSandbox bool
}

// payload is the JSON body actually sent to APNs. The top-level "aps"
// object holds the alert/sound/badge; custom keys live alongside it.
type payload struct {
	APS    apsPayload
	Custom map[string]string
}

type apsPayload struct {
	Alert            *apsAlert `json:"alert,omitempty"`
	Sound            string    `json:"sound,omitempty"`
	Badge            *int      `json:"badge,omitempty"`
	Category         string    `json:"category,omitempty"`
	ThreadID         string    `json:"thread-id,omitempty"`
	ContentAvailable int       `json:"content-available,omitempty"`
}

type apsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// MarshalJSON serializes the APNs payload with "aps" plus any custom
// top-level keys. Custom keys take precedence over reserved names only
// to the extent that we never put "aps" inside Custom — callers should
// avoid that.
func (p payload) MarshalJSON() ([]byte, error) {
	// Build a flat map: aps + each custom key at the top level.
	out := map[string]any{"aps": p.APS}
	for k, v := range p.Custom {
		if k == "aps" {
			continue
		}
		out[k] = v
	}
	return json.Marshal(out)
}

// NewService creates a push Service. Returns nil (disabled) if any required
// env var is absent — callers must nil-check before using.
func NewService(teamID, keyID, authKeyP8Base64, bundleID string, sandbox bool, logger *slog.Logger) *Service {
	if teamID == "" || keyID == "" || authKeyP8Base64 == "" || bundleID == "" {
		logger.Info("push: APNs not configured — in-app notifications only (set APPLE_TEAM_ID, APNS_KEY_ID, APNS_AUTH_KEY_P8, IOS_BUNDLE_ID to enable)")
		return nil
	}

	keyBytes, err := base64.StdEncoding.DecodeString(authKeyP8Base64)
	if err != nil {
		logger.Warn("push: failed to base64-decode APNS_AUTH_KEY_P8", "error", err)
		return nil
	}

	block, _ := pem.Decode(keyBytes)
	if block == nil {
		logger.Warn("push: APNS_AUTH_KEY_P8 is not valid PEM")
		return nil
	}

	iface, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		logger.Warn("push: failed to parse APNs private key", "error", err)
		return nil
	}

	ecKey, ok := iface.(*ecdsa.PrivateKey)
	if !ok {
		logger.Warn("push: APNs key is not ECDSA")
		return nil
	}

	logger.Info("push: APNs configured", "bundle_id", bundleID, "sandbox", sandbox)

	return &Service{
		teamID:     teamID,
		keyID:      keyID,
		key:        ecKey,
		bundleID:   bundleID,
		sandbox:    sandbox,
		logger:     logger,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetTokenInvalidator wires the DB repo that prunes dead tokens. Called
// once from main.go after handlers are constructed. Optional: when nil,
// 410/BadDeviceToken responses are still logged but the row is left in
// place (less harmful than crashing at startup).
func (s *Service) SetTokenInvalidator(inv TokenInvalidator) {
	if s == nil {
		return
	}
	s.invalidator = inv
}

// ErrTokenUnregistered is returned by send when APNs reports the device
// token is permanently dead. Callers can use this to drive token cleanup;
// most callers should ignore — the Service already invalidates internally.
var ErrTokenUnregistered = errors.New("push: device token no longer registered")

// Send delivers a push notification to a single device token. The
// PushRequest contract is the same one chosen by the dispatcher in
// handlers/notifications.go. Errors are logged but not returned — push
// is always best-effort. On HTTP 5xx / network error we retry up to two
// times with 1s/3s backoff; 4xx is treated as permanent and never retried.
//
// Returns the underlying error so callers that care (tests, the dispatcher's
// errgroup fan-out) can react; in production we ignore the return value.
func (s *Service) Send(token string, p PushRequest) error {
	if s == nil {
		return nil
	}
	if token == "" {
		return nil
	}

	apnsToken, err := s.providerToken()
	if err != nil {
		s.logger.Warn("push: provider token error", "error", err)
		return err
	}

	host := "https://api.push.apple.com"
	if p.IsSandbox {
		host = "https://api.sandbox.push.apple.com"
	}
	url := fmt.Sprintf("%s/3/device/%s", host, token)

	// Build the JSON body. Silent pushes (content-available=1) MUST NOT
	// include an alert per APNs rules, so we only attach the alert block
	// when we have a title or body.
	aps := apsPayload{
		Sound:    p.Sound,
		Badge:    p.Badge,
		Category: p.Category,
		ThreadID: p.ThreadID,
	}
	if !p.ContentAvailable && (p.Title != "" || p.Body != "") {
		aps.Alert = &apsAlert{Title: p.Title, Body: p.Body}
		if aps.Sound == "" {
			aps.Sound = "default"
		}
	}
	if p.ContentAvailable {
		aps.ContentAvailable = 1
	}

	plBytes, err := json.Marshal(payload{APS: aps, Custom: p.Data})
	if err != nil {
		s.logger.Warn("push: marshal payload", "error", err)
		return err
	}

	// Retry on transient errors only. 1s then 3s of backoff; the request
	// itself has a 10s timeout so the whole fan-out for a single token
	// caps at ~34s in the worst case. The errgroup limit in the dispatcher
	// keeps that bounded across many tokens.
	backoffs := []time.Duration{0, 1 * time.Second, 3 * time.Second}
	var lastErr error
	for attempt, wait := range backoffs {
		if wait > 0 {
			time.Sleep(wait)
		}
		req, rerr := http.NewRequest(http.MethodPost, url, bytes.NewReader(plBytes))
		if rerr != nil {
			s.logger.Warn("push: build request error", "error", rerr)
			return rerr
		}
		req.Header.Set("authorization", "bearer "+apnsToken)
		req.Header.Set("apns-topic", s.bundleID)
		pushType := "alert"
		if p.ContentAvailable {
			pushType = "background"
		}
		req.Header.Set("apns-push-type", pushType)
		req.Header.Set("content-type", "application/json")

		priority := p.Priority
		if priority == 0 {
			if p.ContentAvailable {
				priority = 5
			} else {
				priority = 10
			}
		}
		req.Header.Set("apns-priority", strconv.Itoa(priority))

		if p.CollapseID != "" {
			req.Header.Set("apns-collapse-id", p.CollapseID)
		}
		if !p.Expiration.IsZero() {
			req.Header.Set("apns-expiration", strconv.FormatInt(p.Expiration.Unix(), 10))
		}

		resp, derr := s.httpClient.Do(req)
		if derr != nil {
			lastErr = derr
			s.logger.Warn("push: request error", "attempt", attempt, "error", derr)
			continue
		}

		// Drain + close so the HTTP/2 stream can be reused.
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		var errBody map[string]string
		_ = json.Unmarshal(body, &errBody)
		reason := errBody["reason"]

		// 410 Unregistered: token is dead, prune from DB and stop retrying.
		// 400 with reason=BadDeviceToken is the other "permanently bad" code.
		if resp.StatusCode == http.StatusGone ||
			(resp.StatusCode == http.StatusBadRequest && reason == "BadDeviceToken") {
			s.logger.Info("push: token unregistered, pruning",
				"status", resp.StatusCode, "reason", reason, "token", tokenSuffix(token))
			if s.invalidator != nil {
				if derr := s.invalidator.DeleteByToken(token); derr != nil {
					s.logger.Warn("push: invalidator delete failed", "error", derr, "token", tokenSuffix(token))
				}
			}
			return ErrTokenUnregistered
		}

		// 4xx (other than 410) is permanent — bad payload, expired auth,
		// topic mismatch. Don't retry; log loudly so it gets fixed.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			s.logger.Warn("push: APNs rejected (4xx, no retry)",
				"status", resp.StatusCode, "reason", reason, "token", tokenSuffix(token))
			return fmt.Errorf("apns rejected: %d %s", resp.StatusCode, reason)
		}

		// 5xx — retry the inner loop after backoff.
		lastErr = fmt.Errorf("apns 5xx: %d %s", resp.StatusCode, reason)
		s.logger.Warn("push: APNs 5xx (will retry)",
			"status", resp.StatusCode, "reason", reason, "attempt", attempt, "token", tokenSuffix(token))
	}
	return lastErr
}

// providerToken returns a cached JWT, refreshing it when within 5 min of expiry.
// APNs tokens are valid for 1 hour.
func (s *Service) providerToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cachedToken != "" && time.Until(s.tokenExp) > 5*time.Minute {
		return s.cachedToken, nil
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iss": s.teamID,
		"iat": now.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = s.keyID

	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("sign APNs JWT: %w", err)
	}

	s.cachedToken = signed
	s.tokenExp = now.Add(55 * time.Minute) // refresh before 1h expiry
	return signed, nil
}

// tokenSuffix returns a safe-to-log prefix of the token (8 chars). Tokens
// aren't strictly secret but logging the full string is noisy and slightly
// privacy-leaky, so we truncate.
func tokenSuffix(token string) string {
	if len(token) <= 8 {
		return token + "..."
	}
	return token[:8] + "..."
}
