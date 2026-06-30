package config

import (
	"strings"
	"testing"
)

// Production validation must catch every footgun listed in
// ValidateForProduction. Dev should be a no-op.

func TestValidateForProduction_DevIsNoop(t *testing.T) {
	c := &Config{Env: "development"}
	if err := c.ValidateForProduction(); err != nil {
		t.Fatalf("dev env should skip validation, got %v", err)
	}
}

func TestValidateForProduction_RejectsDefaultJWT(t *testing.T) {
	c := goodProdConfig()
	c.JWTSecret = "dev-secret-change-me"
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Errorf("expected JWT_SECRET complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsEmptyStripeSecret(t *testing.T) {
	c := goodProdConfig()
	c.StripeSecretKey = ""
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "STRIPE_SECRET_KEY") {
		t.Errorf("expected STRIPE_SECRET_KEY complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsEmptyWebhookSecret(t *testing.T) {
	c := goodProdConfig()
	c.StripeWebhookSecret = ""
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "STRIPE_WEBHOOK_SECRET") {
		t.Errorf("expected STRIPE_WEBHOOK_SECRET complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsEmptyUploadSecret(t *testing.T) {
	c := goodProdConfig()
	c.UploadURLSecret = ""
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "UPLOAD_URL_SECRET") {
		t.Errorf("expected UPLOAD_URL_SECRET complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsHTTPBaseURL(t *testing.T) {
	c := goodProdConfig()
	c.AppBaseURL = "http://drivebai.com"
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("expected HTTPS complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsAutoApproveCars(t *testing.T) {
	c := goodProdConfig()
	c.AutoApproveCars = true
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "AUTO_APPROVE_CARS") {
		t.Errorf("expected AUTO_APPROVE_CARS complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsUnsignedPrivateUploads(t *testing.T) {
	c := goodProdConfig()
	c.RequirePrivateUploadSignatures = false
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "REQUIRE_PRIVATE_UPLOAD_SIGNATURES") {
		t.Errorf("expected REQUIRE_PRIVATE_UPLOAD_SIGNATURES complaint, got %v", err)
	}
}

func TestValidateForProduction_AcceptsGoodConfig(t *testing.T) {
	c := goodProdConfig()
	if err := c.ValidateForProduction(); err != nil {
		t.Errorf("good prod config rejected: %v", err)
	}
}

func TestValidateForProduction_AccumulatesAllProblems(t *testing.T) {
	c := goodProdConfig()
	c.JWTSecret = ""
	c.StripeSecretKey = ""
	c.StripeWebhookSecret = ""
	err := c.ValidateForProduction()
	if err == nil {
		t.Fatal("expected error")
	}
	// Operator deserves to see every missing var in one pass, not one at a time.
	for _, kw := range []string{"JWT_SECRET", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET"} {
		if !strings.Contains(err.Error(), kw) {
			t.Errorf("expected %q in error, got %v", kw, err)
		}
	}
}

func goodProdConfig() *Config {
	return &Config{
		Env:                            "production",
		JWTSecret:                      "32-bytes-of-real-entropy-please-x",
		StripeSecretKey:                "sk_live_x",
		StripePublishableKey:           "pk_live_x",
		StripeWebhookSecret:            "whsec_x",
		UploadURLSecret:                "upload-secret",
		AppBaseURL:                     "https://drivebai.com",
		AutoApproveCars:                false,
		RequirePrivateUploadSignatures: true,
		CORSAllowedOrigins:             []string{"https://drivebai-admin-team.fly.dev"},
		// APNs: production must have all four set. Tests for the
		// per-field-missing case toggle these off below.
		AppleTeamID:   "ABCDE12345",
		APNSKeyID:     "FGHIJ67890",
		APNSAuthKeyP8: "base64=",
		IOSBundleID:   "com.drivebai.DriveBai",
	}
}

// APNs is a soft requirement: push degrades gracefully without it (in-app +
// WebSocket still fire), so ValidateForProduction does NOT fail-loud on
// missing APNs fields — that would chicken-and-egg first deploys. The
// startup WARN driven by HasPushConfigured() is the loud-enough signal.
func TestHasPushConfigured_TrueWhenAllFieldsSet(t *testing.T) {
	c := goodProdConfig()
	if !c.HasPushConfigured() {
		t.Errorf("expected HasPushConfigured=true with all APNs fields set")
	}
}

func TestHasPushConfigured_FalseAndValidateOK_WhenAnyAPNsFieldMissing(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Config)
	}{
		{"team_id", func(c *Config) { c.AppleTeamID = "" }},
		{"key_id", func(c *Config) { c.APNSKeyID = "" }},
		{"auth_key_p8", func(c *Config) { c.APNSAuthKeyP8 = "" }},
		{"bundle_id", func(c *Config) { c.IOSBundleID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := goodProdConfig()
			tc.mod(c)
			if c.HasPushConfigured() {
				t.Errorf("HasPushConfigured should be false when %s is empty", tc.name)
			}
			if err := c.ValidateForProduction(); err != nil {
				t.Errorf("ValidateForProduction must NOT fail on missing APNs %s; got %v", tc.name, err)
			}
		})
	}
}

func TestValidateForProduction_RejectsWildcardCORS(t *testing.T) {
	c := goodProdConfig()
	c.CORSAllowedOrigins = []string{"*"}
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
		t.Errorf("expected CORS_ALLOWED_ORIGINS complaint, got %v", err)
	}
}

func TestValidateForProduction_RejectsEmptyCORS(t *testing.T) {
	c := goodProdConfig()
	c.CORSAllowedOrigins = nil
	err := c.ValidateForProduction()
	if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
		t.Errorf("expected CORS_ALLOWED_ORIGINS complaint, got %v", err)
	}
}

func TestParseCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{"a, b , c", []string{"a", "b", "c"}},
		{",a,,b,", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := parseCSV(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseCSV(%q): len mismatch got %v want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseCSV(%q)[%d]: got %q want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
