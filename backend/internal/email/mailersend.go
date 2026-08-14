package email

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const mailerSendAPIURL = "https://api.mailersend.com/v1/email"

// OTPSendResult holds the outcome of an OTP email send attempt.
type OTPSendResult struct {
	MessageID string // MailerSend X-Message-Id (empty for console sender)
}

// OTPSender is the minimal interface for sending OTP login emails.
// It is intentionally separate from the existing Sender interface so
// that OTP delivery can use MailerSend independently of SendGrid.
type OTPSender interface {
	SendLoginOTP(toEmail, code string) (*OTPSendResult, error)
	// SendContactChangeOTP delivers the code that confirms an email/phone
	// change (batch items 7+8). changeDesc is a short human phrase for the
	// email body, e.g. "change your email to x@y.com".
	SendContactChangeOTP(toEmail, code, changeDesc string) (*OTPSendResult, error)
}

// MailerSendOTPSender sends OTP emails via the MailerSend REST API.
type MailerSendOTPSender struct {
	apiKey    string
	fromEmail string
	fromName  string
	client    *http.Client
	logger    *slog.Logger
}

// MailerSendSender implements the transactional Sender interface
// (verification + password reset) using the MailerSend REST API. Used in
// production when SENDGRID_API_KEY is unset but MAILERSEND_API_KEY is
// configured — lets one vendor handle every outbound email.
type MailerSendSender struct {
	apiKey         string
	fromEmail      string
	fromName       string
	deeplinkScheme string
	baseURL        string
	client         *http.Client
	logger         *slog.Logger
}

// ConsoleOTPSender prints OTP to stdout (used when MAILERSEND_API_KEY is unset).
type ConsoleOTPSender struct {
	logger *slog.Logger
}

// NewOTPSender creates an OTPSender. Falls back to console output when apiKey is empty.
func NewOTPSender(apiKey, fromEmail, fromName string, logger *slog.Logger) OTPSender {
	if apiKey == "" {
		logger.Warn("MAILERSEND_API_KEY not set — OTP emails will be printed to console")
		return &ConsoleOTPSender{logger: logger}
	}
	logger.Info("MailerSend OTP sender configured", "from_email", fromEmail)
	return &MailerSendOTPSender{
		apiKey:    apiKey,
		fromEmail: fromEmail,
		fromName:  fromName,
		client:    &http.Client{},
		logger:    logger,
	}
}

// mailerSendPayload mirrors the MailerSend /v1/email request body.
type mailerSendPayload struct {
	From    mailerSendAddress   `json:"from"`
	To      []mailerSendAddress `json:"to"`
	Subject string              `json:"subject"`
	Text    string              `json:"text"`
	HTML    string              `json:"html"`
}

type mailerSendAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

func (s *MailerSendOTPSender) SendLoginOTP(toEmail, code string) (*OTPSendResult, error) {
	plainText := fmt.Sprintf(
		"Your DrivaBai login code is: %s\n\nThis code expires in 10 minutes.\n\nIf you did not request this, you can safely ignore this email.\n\nThe DrivaBai Team",
		code,
	)

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8">
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;line-height:1.6;color:#333}
  .container{max-width:600px;margin:0 auto;padding:20px}
  .code{font-size:36px;font-weight:bold;letter-spacing:10px;color:#4ECDC4;text-align:center;padding:24px;background:#f5f5f5;border-radius:8px;margin:24px 0}
  .footer{margin-top:30px;font-size:12px;color:#666}
</style>
</head>
<body>
<div class="container">
  <h2>Your DrivaBai login code</h2>
  <p>Use the code below to sign in. It expires in <strong>10 minutes</strong>.</p>
  <div class="code">%s</div>
  <p>If you did not request this code, you can safely ignore this email.</p>
  <div class="footer"><p>The DrivaBai Team</p></div>
</div>
</body>
</html>`, code)

	payload := mailerSendPayload{
		From:    mailerSendAddress{Email: s.fromEmail, Name: s.fromName},
		To:      []mailerSendAddress{{Email: toEmail}},
		Subject: "Your DrivaBai login code",
		Text:    plainText,
		HTML:    htmlBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("mailersend: marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, mailerSendAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mailersend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Error("mailersend: request failed", "error", err, "to", toEmail)
		return nil, fmt.Errorf("mailersend: send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for diagnostics
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		s.logger.Error("mailersend: API rejected email",
			"status", resp.StatusCode,
			"to", toEmail,
			"response", string(respBody),
		)
		return nil, fmt.Errorf("mailersend: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// MailerSend returns X-Message-Id on 202 Accepted
	messageID := resp.Header.Get("X-Message-Id")
	s.logger.Info("OTP email accepted by MailerSend",
		"to", toEmail,
		"status", resp.StatusCode,
		"message_id", messageID,
	)
	return &OTPSendResult{MessageID: messageID}, nil
}

// sendMailerSend posts a transactional email through MailerSend and returns
// the X-Message-Id. Used by both the OTP path and the transactional Sender
// path so they share one HTTP body / error-handling shape.
func sendMailerSend(client *http.Client, apiKey string, payload mailerSendPayload, logger *slog.Logger, toEmail, kind string) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("mailersend: marshal %s payload: %w", kind, err)
	}

	req, err := http.NewRequest(http.MethodPost, mailerSendAPIURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("mailersend: build %s request: %w", kind, err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		logger.Error("mailersend: request failed", "error", err, "to", toEmail, "kind", kind)
		return "", fmt.Errorf("mailersend: send %s request: %w", kind, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 {
		logger.Error("mailersend: API rejected email",
			"status", resp.StatusCode,
			"to", toEmail,
			"kind", kind,
			"response", string(respBody),
		)
		return "", fmt.Errorf("mailersend: API returned status %d for %s: %s", resp.StatusCode, kind, string(respBody))
	}

	return resp.Header.Get("X-Message-Id"), nil
}

// SendVerificationEmail sends a 6-digit verification code via MailerSend.
// Re-uses the same copy as the SendGrid path so users see identical email
// content regardless of which provider is wired.
func (s *MailerSendSender) SendVerificationEmail(toEmail, toName, code string) error {
	plainText := fmt.Sprintf(`Hello %s,

Your verification code is: %s

This code will expire in 10 minutes.

If you didn't create a DriveBai account, you can safely ignore this email.

Best,
The DriveBai Team`, toName, code)

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8">
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;line-height:1.6;color:#333}
  .container{max-width:600px;margin:0 auto;padding:20px}
  .code{font-size:32px;font-weight:bold;letter-spacing:8px;color:#4ECDC4;text-align:center;padding:20px;background:#f5f5f5;border-radius:8px;margin:20px 0}
  .footer{margin-top:30px;font-size:12px;color:#666}
</style>
</head>
<body>
<div class="container">
  <h2>Verify your email</h2>
  <p>Hello %s,</p>
  <p>Your verification code is:</p>
  <div class="code">%s</div>
  <p>This code will expire in 10 minutes.</p>
  <p>If you didn't create a DriveBai account, you can safely ignore this email.</p>
  <div class="footer"><p>Best,<br>The DriveBai Team</p></div>
</div>
</body>
</html>`, toName, code)

	payload := mailerSendPayload{
		From:    mailerSendAddress{Email: s.fromEmail, Name: s.fromName},
		To:      []mailerSendAddress{{Email: toEmail, Name: toName}},
		Subject: "Verify your DriveBai account",
		Text:    plainText,
		HTML:    htmlBody,
	}

	messageID, err := sendMailerSend(s.client, s.apiKey, payload, s.logger, toEmail, "verification")
	if err != nil {
		return err
	}
	s.logger.Info("verification email accepted by MailerSend",
		"to", toEmail,
		"message_id", messageID,
	)
	return nil
}

// SendPasswordResetEmail sends a password-reset email with both an in-app
// deep link (drivebai://reset-password?token=...) and a web fallback link.
func (s *MailerSendSender) SendPasswordResetEmail(toEmail, toName, token string) error {
	resetLink := fmt.Sprintf("%s://reset-password?token=%s", s.deeplinkScheme, token)
	webLink := fmt.Sprintf("%s/reset-password?token=%s", s.baseURL, token)

	plainText := fmt.Sprintf(`Hello %s,

You requested to reset your password.

Open in DriveBai app:
%s

Or use this web link:
%s

This link will expire in 1 hour.

If you didn't request a password reset, you can safely ignore this email.

Best,
The DriveBai Team`, toName, resetLink, webLink)

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8">
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;line-height:1.6;color:#333}
  .container{max-width:600px;margin:0 auto;padding:20px}
  .button{display:inline-block;background-color:#4ECDC4;color:white;text-decoration:none;padding:14px 28px;border-radius:8px;font-weight:bold;margin:20px 0}
  .button-secondary{background-color:#6c757d}
  .link{word-break:break-all;color:#4ECDC4;font-size:12px}
  .footer{margin-top:30px;font-size:12px;color:#666}
  .divider{margin:20px 0;text-align:center;color:#999}
</style>
</head>
<body>
<div class="container">
  <h2>Reset your password</h2>
  <p>Hello %s,</p>
  <p>You requested to reset your password. Tap the button to open DriveBai:</p>
  <p style="text-align:center;"><a href="%s" class="button">Reset Password in App</a></p>
  <p class="divider">— or —</p>
  <p>If the button doesn't work, use this web link:</p>
  <p style="text-align:center;"><a href="%s" class="button button-secondary">Reset via Web</a></p>
  <p class="link">%s</p>
  <p>This link expires in 1 hour.</p>
  <p>If you didn't request a password reset, you can safely ignore this email.</p>
  <div class="footer"><p>Best,<br>The DriveBai Team</p></div>
</div>
</body>
</html>`, toName, resetLink, webLink, webLink)

	payload := mailerSendPayload{
		From:    mailerSendAddress{Email: s.fromEmail, Name: s.fromName},
		To:      []mailerSendAddress{{Email: toEmail, Name: toName}},
		Subject: "Reset your DriveBai password",
		Text:    plainText,
		HTML:    htmlBody,
	}

	messageID, err := sendMailerSend(s.client, s.apiKey, payload, s.logger, toEmail, "password-reset")
	if err != nil {
		return err
	}
	s.logger.Info("password reset email accepted by MailerSend",
		"to", toEmail,
		"message_id", messageID,
		"token_prefix", redactToken(token),
	)
	return nil
}

func (s *ConsoleOTPSender) SendLoginOTP(toEmail, code string) (*OTPSendResult, error) {
	// NOTE: logging OTP code is intentional in dev/console mode only.
	// Production always uses MailerSendOTPSender which never logs the code.
	s.logger.Info("OTP EMAIL (dev mode — MailerSend not configured)",
		"to", toEmail,
	)
	fmt.Printf("\n"+
		"╔══════════════════════════════════════════════════════════╗\n"+
		"║  📧 LOGIN OTP EMAIL (MailerSend not configured)          ║\n"+
		"╠══════════════════════════════════════════════════════════╣\n"+
		"║  To:   %-50s ║\n"+
		"║  Code: %-50s ║\n"+
		"╚══════════════════════════════════════════════════════════╝\n\n",
		toEmail, code)
	return &OTPSendResult{}, nil
}

// SendContactChangeOTP — the confirmation code for an email/phone change
// (batch items 7+8). Same shape as the login code; nothing is committed
// until this code verifies.
func (s *MailerSendOTPSender) SendContactChangeOTP(toEmail, code, changeDesc string) (*OTPSendResult, error) {
	plainText := fmt.Sprintf(
		"Your DrivaBai confirmation code is: %s\n\nEnter it in the app to %s.\n\nThis code expires in 10 minutes. Nothing changes until you enter it.\n\nIf you did not request this, you can safely ignore this email.\n\nThe DrivaBai Team",
		code, changeDesc,
	)

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8">
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;line-height:1.6;color:#333}
  .container{max-width:600px;margin:0 auto;padding:20px}
  .code{font-size:36px;font-weight:bold;letter-spacing:10px;color:#4ECDC4;text-align:center;padding:24px;background:#f5f5f5;border-radius:8px;margin:24px 0}
  .footer{margin-top:30px;font-size:12px;color:#666}
</style>
</head>
<body>
<div class="container">
  <h2>Confirm your account change</h2>
  <p>Enter the code below in the app to %s. It expires in <strong>10 minutes</strong> — nothing changes until you enter it.</p>
  <div class="code">%s</div>
  <p>If you did not request this change, you can safely ignore this email.</p>
  <div class="footer"><p>The DrivaBai Team</p></div>
</div>
</body>
</html>`, changeDesc, code)

	payload := mailerSendPayload{
		From:    mailerSendAddress{Email: s.fromEmail, Name: s.fromName},
		To:      []mailerSendAddress{{Email: toEmail}},
		Subject: "Confirm your DrivaBai account change",
		Text:    plainText,
		HTML:    htmlBody,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("mailersend: marshal payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, mailerSendAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mailersend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Error("mailersend: contact-change request failed", "error", err, "to", toEmail)
		return nil, fmt.Errorf("mailersend: send request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		s.logger.Error("mailersend: API rejected contact-change email",
			"status", resp.StatusCode, "to", toEmail, "response", string(respBody))
		return nil, fmt.Errorf("mailersend: API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	messageID := resp.Header.Get("X-Message-Id")
	s.logger.Info("contact-change OTP email accepted by MailerSend", "to", toEmail, "message_id", messageID)
	return &OTPSendResult{MessageID: messageID}, nil
}

// SendContactChangeOTP (console/dev variant).
func (s *ConsoleOTPSender) SendContactChangeOTP(toEmail, code, changeDesc string) (*OTPSendResult, error) {
	s.logger.Info("CONTACT-CHANGE OTP EMAIL (dev mode — MailerSend not configured)", "to", toEmail)
	fmt.Printf("\n"+
		"╔══════════════════════════════════════════════════════════╗\n"+
		"║  📧 CONTACT-CHANGE OTP (MailerSend not configured)       ║\n"+
		"╠══════════════════════════════════════════════════════════╣\n"+
		"║  To:     %-48s ║\n"+
		"║  Change: %-48s ║\n"+
		"║  Code:   %-48s ║\n"+
		"╚══════════════════════════════════════════════════════════╝\n\n",
		toEmail, changeDesc, code)
	return &OTPSendResult{}, nil
}

// SendEmailChangedNotice — MailerSend variant of the account email-change
// notice (admin email edit). Same copy as the SendGrid path.
func (s *MailerSendSender) SendEmailChangedNotice(toEmail, toName, oldEmail, newEmail string) error {
	plainText := fmt.Sprintf(`Hello %s,

The login email on your DriveBai account was changed by our support team:

  From: %s
  To:   %s

The new address must be verified before it is trusted. If you asked for this change, no action is needed beyond verifying.

If you did NOT ask for this, contact DriveBai support immediately.

Best,
The DriveBai Team`, toName, oldEmail, newEmail)

	htmlBody := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><meta charset="UTF-8">
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;line-height:1.6;color:#333}
  .container{max-width:600px;margin:0 auto;padding:20px}
  .change{padding:16px 20px;background:#f5f5f5;border-radius:8px;margin:20px 0}
  .footer{margin-top:30px;font-size:12px;color:#666}
</style>
</head>
<body>
<div class="container">
  <h2>Your login email was changed</h2>
  <p>Hello %s,</p>
  <p>The login email on your DriveBai account was changed by our support team:</p>
  <div class="change"><strong>From:</strong> %s<br><strong>To:</strong> %s</div>
  <p>The new address must be verified before it is trusted. If you asked for this change, no action is needed beyond verifying.</p>
  <p><strong>If you did NOT ask for this, contact DriveBai support immediately.</strong></p>
  <div class="footer"><p>The DriveBai Team</p></div>
</div>
</body>
</html>`, toName, oldEmail, newEmail)

	payload := mailerSendPayload{
		From:    mailerSendAddress{Email: s.fromEmail, Name: s.fromName},
		To:      []mailerSendAddress{{Email: toEmail, Name: toName}},
		Subject: "Your DriveBai login email was changed",
		Text:    plainText,
		HTML:    htmlBody,
	}
	_, err := sendMailerSend(s.client, s.apiKey, payload, s.logger, toEmail, "email-changed notice")
	return err
}
