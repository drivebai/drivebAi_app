// Package push sends Apple Push Notification service (APNs) alerts.
// All sending is best-effort — a push failure never affects the in-app
// notification that was already stored in Postgres.
//
// Required env vars (all must be non-empty to enable push):
//   APPLE_TEAM_ID        — 10-char Apple developer Team ID
//   APNS_KEY_ID          — 10-char key identifier for the .p8 auth key
//   APNS_AUTH_KEY_P8     — base64-encoded contents of the .p8 key file
//   IOS_BUNDLE_ID        — e.g. com.drivebai.app
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
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Service sends APNs push notifications via HTTP/2 + JWT auth.
type Service struct {
	teamID   string
	keyID    string
	key      *ecdsa.PrivateKey
	bundleID string
	sandbox  bool
	logger   *slog.Logger

	mu         sync.Mutex
	cachedToken string
	tokenExp    time.Time

	httpClient *http.Client
}

// Payload is the APNS JSON body.
type Payload struct {
	APS APSPayload `json:"aps"`
}

type APSPayload struct {
	Alert APSAlert `json:"alert"`
	Sound string   `json:"sound"`
	Badge *int     `json:"badge,omitempty"`
}

type APSAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
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

// Send delivers a push notification to a single device token.
// Errors are logged but not returned — push is always best-effort.
func (s *Service) Send(token, title, body string, isSandbox bool) {
	if s == nil {
		return
	}

	apnsToken, err := s.providerToken()
	if err != nil {
		s.logger.Warn("push: provider token error", "error", err)
		return
	}

	host := "https://api.push.apple.com"
	if isSandbox {
		host = "https://api.sandbox.push.apple.com"
	}
	url := fmt.Sprintf("%s/3/device/%s", host, token)

	pl := Payload{
		APS: APSPayload{
			Alert: APSAlert{Title: title, Body: body},
			Sound: "default",
		},
	}
	plBytes, _ := json.Marshal(pl)

	req, err := http.NewRequest("POST", url, bytes.NewReader(plBytes))
	if err != nil {
		s.logger.Warn("push: build request error", "error", err)
		return
	}
	req.Header.Set("authorization", "bearer "+apnsToken)
	req.Header.Set("apns-topic", s.bundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("content-type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.Warn("push: request error", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]string
		json.NewDecoder(resp.Body).Decode(&errBody)
		s.logger.Warn("push: APNs rejected", "status", resp.StatusCode, "reason", errBody["reason"], "token", token[:min(8, len(token))]+"...")
	}
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
