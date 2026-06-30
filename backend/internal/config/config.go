package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	Env         string
	DatabaseURL string

	JWTSecret          string
	JWTAccessTokenTTL  time.Duration
	JWTRefreshTokenTTL time.Duration

	// Email configuration (SendGrid - existing flows)
	SendGridAPIKey    string
	SendGridFromEmail string
	SendGridFromName  string

	// MailerSend configuration (OTP login emails)
	MailerSendAPIKey string
	MailerFromEmail  string
	MailerFromName   string

	// App configuration
	AppDeeplinkScheme string
	AppBaseURL        string

	UploadDir string
	// UploadURLSecret keys the HMAC that signs private file URLs (chat
	// attachments, driver documents, accident files, …). Required in
	// production; when empty in dev we fall back to JWTSecret so local
	// flows still work without extra setup.
	UploadURLSecret string
	// UploadURLTTL is how long a signed private-file URL remains valid.
	// Defaults to 1 hour — short enough that an accidentally-shared link
	// dies quickly, long enough that the iOS image cache + chat reload
	// stay happy without re-fetching parent JSON.
	UploadURLTTL time.Duration
	// RequirePrivateUploadSignatures must be true in production so private
	// paths are 404'd without a valid signature. Off in development so
	// running with old clients that still hold unsigned URLs doesn't
	// break local QA. Defaults to true when ENV != "development".
	RequirePrivateUploadSignatures bool

	// Stripe configuration
	StripeSecretKey      string
	StripePublishableKey string
	StripeWebhookSecret  string
	PlatformFeeBPS       int // basis points, e.g. 500 = 5%

	// Listing price constraints
	MinWeeklyRentPrice float64 // minimum allowed weekly rent price; default 50

	// PickupDeadlineMinutes is the grace window after payment_intent.succeeded
	// in which the driver must press "Confirm pickup". Past this, the background
	// scanner refunds the payment and returns the car to discovery. Default 120.
	PickupDeadlineMinutes int
	// PickupExpiryScanIntervalSeconds controls how often the background worker
	// polls for expired pickups. Default 60s. Tunable for tests/demo.
	PickupExpiryScanIntervalSeconds int

	// Test/staging bypass: auto-approve newly created cars so they appear in Discover immediately.
	// Set AUTO_APPROVE_CARS=true in dev/staging; must be false (default) in production.
	AutoApproveCars bool

	// CORS allowed origins, comma-separated. In production this must be a
	// concrete list (e.g. https://drivebai-admin-team.fly.dev). Default of
	// "*" is fine for development (iOS clients don't care about CORS); the
	// production-validation step rejects "*" in non-dev envs.
	CORSAllowedOrigins []string

	// APNs push notification (all required; if any empty, push is disabled)
	AppleTeamID   string
	APNSKeyID     string
	APNSAuthKeyP8 string // base64-encoded .p8 key file contents
	IOSBundleID   string
	APNSSandbox   bool // true for dev/TestFlight builds
}

func Load() (*Config, error) {
	// Load .env file if it exists (ignore error if not found)
	_ = godotenv.Load()

	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		Env:         getEnv("ENV", "development"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://drivebai:drivebai_secret@localhost:5432/drivebai?sslmode=disable"),

		JWTSecret:          getEnv("JWT_SECRET", "dev-secret-change-me"),
		JWTAccessTokenTTL:  getDuration("JWT_ACCESS_TOKEN_TTL", 15*time.Minute),
		JWTRefreshTokenTTL: getDuration("JWT_REFRESH_TOKEN_TTL", 30*24*time.Hour),

		SendGridAPIKey:    getEnv("SENDGRID_API_KEY", ""),
		SendGridFromEmail: getEnv("SENDGRID_FROM_EMAIL", "noreply@drivebai.com"),
		SendGridFromName:  getEnv("SENDGRID_FROM_NAME", "DriveBai"),

		MailerSendAPIKey: getEnv("MAILERSEND_API_KEY", ""),
		MailerFromEmail:  getEnv("MAIL_FROM_EMAIL", "noreply@drivebai.com"),
		MailerFromName:   getEnv("MAIL_FROM_NAME", "DrivaBai"),

		AppDeeplinkScheme: getEnv("APP_DEEPLINK_SCHEME", "drivebai"),
		AppBaseURL:        getEnv("APP_BASE_URL", "http://localhost:8080"),

		UploadDir: getEnv("UPLOAD_DIR", "./uploads"),
		// UploadURLSecret: prefer the explicit env var; in dev, fall back to
		// the JWT secret so a fresh local checkout works without extra wiring.
		// Production must set both explicitly.
		UploadURLSecret: getEnv("UPLOAD_URL_SECRET", ""),
		UploadURLTTL:    getDuration("UPLOAD_URL_TTL", 1*time.Hour),
		// Defaults to true (enforce signatures) UNLESS we're in development.
		RequirePrivateUploadSignatures: getEnv("REQUIRE_PRIVATE_UPLOAD_SIGNATURES", "") != "false" &&
			getEnv("ENV", "development") != "development",

		StripeSecretKey:      getEnv("STRIPE_SECRET_KEY", ""),
		StripePublishableKey: getEnv("STRIPE_PUBLISHABLE_KEY", ""),
		StripeWebhookSecret:  getEnv("STRIPE_WEBHOOK_SECRET", ""),
		PlatformFeeBPS:       getIntEnv("PLATFORM_FEE_BPS", 500), // default 5%

		MinWeeklyRentPrice: getFloat64Env("MIN_WEEKLY_RENT_PRICE", 50),
		AutoApproveCars:    getEnv("AUTO_APPROVE_CARS", "false") == "true",

		PickupDeadlineMinutes:           getIntEnv("PICKUP_DEADLINE_MINUTES", 120),
		PickupExpiryScanIntervalSeconds: getIntEnv("PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS", 60),

		// CORS origins. Production should pin to the admin URL(s) only.
		// "*" is allowed in dev and rejected by ValidateForProduction in prod.
		CORSAllowedOrigins: parseCSV(getEnv("CORS_ALLOWED_ORIGINS", "*")),

		AppleTeamID:   getEnv("APPLE_TEAM_ID", ""),
		APNSKeyID:     getEnv("APNS_KEY_ID", ""),
		APNSAuthKeyP8: getEnv("APNS_AUTH_KEY_P8", ""),
		IOSBundleID:   getEnv("IOS_BUNDLE_ID", ""),
		APNSSandbox:   getEnv("APNS_SANDBOX", "true") != "false",
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}

func getIntEnv(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

// parseCSV splits "a,b , c" into ["a","b","c"], trimming whitespace and
// dropping empty entries. Used for CORS_ALLOWED_ORIGINS so a single env var
// can carry multiple admin/admin-staging URLs.
func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getFloat64Env(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	}
	return defaultValue
}

func (c *Config) IsDevelopment() bool {
	return c.Env == "development"
}

// ValidateForProduction enforces the small set of invariants that must hold
// in a non-development env. Each one would otherwise fail silently in a way
// that's hard to diagnose post-deploy (Stripe webhooks dropped on the floor,
// password reset emails undeliverable, sessions signed with the dev secret,
// etc.). Failing fast at startup is the cheapest fix.
//
// Returns a multi-error string (joined with "; ") so the operator sees
// every missing piece in one pass, not one at a time.
//
// In development this is a no-op and returns nil — convenience matters
// more than enforcement on a laptop.
func (c *Config) ValidateForProduction() error {
	if c.IsDevelopment() {
		return nil
	}

	var problems []string

	if c.JWTSecret == "" || c.JWTSecret == "dev-secret-change-me" {
		problems = append(problems, "JWT_SECRET must be set to a strong random value (not the dev default)")
	}
	if c.StripeSecretKey == "" {
		problems = append(problems, "STRIPE_SECRET_KEY is required")
	}
	if c.StripePublishableKey == "" {
		problems = append(problems, "STRIPE_PUBLISHABLE_KEY is required")
	}
	if c.StripeWebhookSecret == "" {
		// Without this the webhook handler 400s every legitimate event,
		// pickups never auto-confirm, and refunds never finalize via the
		// happy path. The polling fallback only partially masks it.
		problems = append(problems, "STRIPE_WEBHOOK_SECRET is required (webhooks will be rejected without it)")
	}
	if c.UploadURLSecret == "" {
		problems = append(problems, "UPLOAD_URL_SECRET is required (private file URLs will refuse to sign)")
	}
	if c.AppBaseURL != "" && !strings.HasPrefix(c.AppBaseURL, "https://") {
		problems = append(problems, "APP_BASE_URL must be HTTPS")
	}
	if c.AutoApproveCars {
		// Cars must go through admin approval in prod — auto-approve bypasses
		// the safety check that's the whole point of the moderation queue.
		problems = append(problems, "AUTO_APPROVE_CARS must be false in production")
	}
	if !c.RequirePrivateUploadSignatures {
		// In production we want the FilesHandler to reject unsigned access
		// to private paths. Toggling this off is a privacy hole.
		problems = append(problems, "REQUIRE_PRIVATE_UPLOAD_SIGNATURES must not be disabled in production")
	}
	if len(c.CORSAllowedOrigins) == 0 {
		problems = append(problems, "CORS_ALLOWED_ORIGINS is required (comma-separated admin URLs)")
	} else {
		for _, o := range c.CORSAllowedOrigins {
			if o == "*" {
				problems = append(problems, "CORS_ALLOWED_ORIGINS must not be '*' in production (set the explicit admin URL)")
				break
			}
		}
	}

	// APNs push: in production we fail loud when any of the four required
	// pieces are missing. Previous behavior was a silent disable inside
	// push.NewService, which let the app ship to TestFlight with zero push
	// delivery — exactly the failure mode this validation exists to catch.
	if c.AppleTeamID == "" {
		problems = append(problems, "APPLE_TEAM_ID is required for push notifications")
	}
	if c.APNSKeyID == "" {
		problems = append(problems, "APNS_KEY_ID is required for push notifications")
	}
	if c.APNSAuthKeyP8 == "" {
		problems = append(problems, "APNS_AUTH_KEY_P8 is required for push notifications (base64-encoded .p8 contents)")
	}
	if c.IOSBundleID == "" {
		problems = append(problems, "IOS_BUNDLE_ID is required for push notifications (e.g. com.drivebai.DriveBai)")
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}
