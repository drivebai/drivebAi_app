# Email Configuration Guide

This document explains how to configure email delivery for the DriveBai backend.

## Quick Start

```bash
# 1. Copy the example env file
cp .env.example .env

# 2. Edit .env and add your SendGrid API key (optional for dev)
#    SENDGRID_API_KEY=SG.your-key-here

# 3. Start or restart the containers
make restart   # if already running
make up        # if starting fresh

# 4. Check the logs to verify email configuration
make logs
# Look for: "Using SendGrid for email delivery" or "using console sender"
```

## Overview

The DriveBai backend sends transactional emails for:
- **Password Reset** - Deep link to reset password via mobile app
- **Email Verification** - Verification codes for new accounts (legacy, currently auto-verified)

## Email Modes

### Development Mode (Console Sender)

When `SENDGRID_API_KEY` is not set (empty or missing), the backend falls back to **console sender mode**. Emails are printed to the container logs instead of being delivered.

**Startup log:**
```
WARN SENDGRID_API_KEY not set, using console sender (emails will be logged to console)
```

**Password reset email in console:**
```
╔══════════════════════════════════════════════════════════════════════════════╗
║  📧 PASSWORD RESET EMAIL (SendGrid not configured)                           ║
╠══════════════════════════════════════════════════════════════════════════════╣
║  To: user@example.com                                                        ║
║  Name: John Doe                                                              ║
║  Token: abc123def456...                                                      ║
║  App Link: drivebai://reset-password?token=abc123def456...                   ║
║  Web Link: http://localhost:8080/reset-password?token=abc123def456...        ║
╚══════════════════════════════════════════════════════════════════════════════╝
```

### Production Mode (SendGrid)

When `SENDGRID_API_KEY` is set, emails are sent via SendGrid's API.

**Startup log:**
```
INFO Using SendGrid for email delivery from_email=noreply@drivebai.com from_name=DriveBai deeplink_scheme=drivebai
```

**Success log (token redacted for security):**
```
INFO password reset email sent successfully to=user@example.com status=202 token_prefix=abc123...
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `SENDGRID_API_KEY` | No | (empty) | Your SendGrid API key. Leave empty for dev mode. |
| `SENDGRID_FROM_EMAIL` | Yes* | `noreply@drivebai.com` | Sender email address (must be verified in SendGrid) |
| `SENDGRID_FROM_NAME` | No | `DriveBai` | Sender display name |
| `APP_DEEPLINK_SCHEME` | No | `drivebai` | URL scheme for mobile app deep links |
| `APP_BASE_URL` | No | `http://localhost:8080` | Base URL for API (used in web fallback links) |

*`SENDGRID_FROM_EMAIL` is required when `SENDGRID_API_KEY` is set. If missing, the system falls back to console mode with a warning.

### Config Validation

The email system validates configuration on startup:
- If `SENDGRID_API_KEY` is set but `SENDGRID_FROM_EMAIL` is empty, it logs an error and falls back to console mode
- Look for these log messages to verify your configuration is correct

## Setting Up SendGrid

### Step 1: Create SendGrid Account

1. Sign up at [sendgrid.com](https://sendgrid.com)
2. Complete account verification

### Step 2: Create API Key

1. Go to **Settings → API Keys**
2. Click **Create API Key**
3. Choose **Restricted Access**
4. Enable only **Mail Send → Full Access**
5. Click **Create & View**
6. Copy the key (starts with `SG.`)

### Step 3: Verify Sender Identity

**IMPORTANT:** SendGrid requires sender verification before you can send emails.

#### Option A: Single Sender Verification (Quick, for testing)

1. Go to **Settings → Sender Authentication → Single Sender Verification**
2. Click **Create New Sender**
3. Fill in:
   - From Email: `noreply@drivebai.com` (or your sender email)
   - Reply To: `support@drivebai.com`
   - Company details
4. Click **Create**
5. Check your email for verification link
6. Click the link to verify

#### Option B: Domain Authentication (Recommended for production)

1. Go to **Settings → Sender Authentication → Authenticate Your Domain**
2. Enter your domain (e.g., `drivebai.com`)
3. SendGrid provides DNS records to add:

| Type | Host | Value |
|------|------|-------|
| CNAME | `em1234.yourdomain.com` | `u1234567.wl.sendgrid.net` |
| CNAME | `s1._domainkey.yourdomain.com` | `s1.domainkey.u1234567.wl.sendgrid.net` |
| CNAME | `s2._domainkey.yourdomain.com` | `s2.domainkey.u1234567.wl.sendgrid.net` |

4. Add these records to your DNS provider
5. Click **Verify** in SendGrid

### Step 4: Configure DriveBai

#### For Docker Compose

Create a `.env` file at the project root (next to `docker-compose.yml`):

```bash
SENDGRID_API_KEY=SG.your-api-key-here
SENDGRID_FROM_EMAIL=noreply@drivebai.com
SENDGRID_FROM_NAME=DriveBai
APP_DEEPLINK_SCHEME=drivebai
```

Then restart the API to pick up the new environment variables:

```bash
make restart
# Or if you need to rebuild the container:
make rebuild
```

#### For Local Development

Create `backend/.env`:

```bash
SENDGRID_API_KEY=SG.your-api-key-here
SENDGRID_FROM_EMAIL=noreply@drivebai.com
SENDGRID_FROM_NAME=DriveBai
APP_DEEPLINK_SCHEME=drivebai
```

Then run:

```bash
cd backend && go run cmd/api/main.go
```

## Testing Email Delivery

### 1. Trigger a Password Reset

```bash
curl -X POST http://localhost:8080/api/v1/auth/password/forgot \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com"}'
```

**Expected response:**
```json
{"message": "If an account exists with this email, you will receive a password reset link."}
```

### 2. Check the Logs

**Without SendGrid (dev mode):**
```bash
make logs
# Look for the PASSWORD RESET EMAIL box with the token and deep link
```

**With SendGrid:**
```bash
make logs
# Look for: "password reset email sent successfully"
```

### 3. Check Email Inbox

If using SendGrid, the email should arrive within 1-2 minutes. Check:
- Inbox
- Spam/Junk folder (if sender not verified properly)

### 4. Test Deep Link (iOS Simulator)

Copy the deep link from the email or logs:

```bash
# Open the deep link in iOS Simulator
xcrun simctl openurl booted "drivebai://reset-password?token=YOUR_TOKEN_HERE"
```

The DriveBai app should open to the password reset screen.

## Troubleshooting

### "SendGrid returned error for password reset email"

Check the logs for the full error message. Common issues:

| Status | Cause | Solution |
|--------|-------|----------|
| 401 | Invalid API key | Regenerate your API key in SendGrid |
| 403 | Sender not verified | Complete sender verification (see Step 3) |
| 400 | Invalid email format | Check the recipient email address |
| 413 | Email too large | Unlikely with our templates, but reduce content if needed |

### Emails Going to Spam

1. **Complete Domain Authentication** - This is the #1 factor
2. **Add DMARC record** - Prevents spoofing
3. **Warm up sending** - Start with low volume, gradually increase
4. **Check sender reputation** - Use [mail-tester.com](https://www.mail-tester.com)

### Deep Link Not Working

1. Ensure iOS app has URL scheme registered in `Info.plist`:
   ```xml
   <key>CFBundleURLTypes</key>
   <array>
       <dict>
           <key>CFBundleURLSchemes</key>
           <array>
               <string>drivebai</string>
           </array>
       </dict>
   </array>
   ```

2. Check `APP_DEEPLINK_SCHEME` matches the iOS URL scheme

3. Test with simulator:
   ```bash
   xcrun simctl openurl booted "drivebai://reset-password?token=test123"
   ```

## Email Templates

The backend sends HTML + plain text emails. Templates are defined in `backend/internal/email/sender.go`.

### Password Reset Email

**Subject:** Reset your DriveBai password

**Contains:**
- Personalized greeting
- "Reset Password in App" button with deep link (opens mobile app)
- "Reset via Web" fallback button (for desktop/browser users)
- Plain text web link for copying
- 1-hour expiration notice

### Verification Email

**Subject:** Verify your DriveBai account

**Contains:**
- Personalized greeting
- 6-digit verification code
- 10-minute expiration notice

## Security Considerations

1. **Token Redaction** - In production logs, tokens are redacted (only first 6 chars shown)
2. **No Email Enumeration** - Forgot password always returns 202, even for non-existent emails
3. **Token Expiration** - Reset tokens expire after 1 hour
4. **Single Use** - Tokens are invalidated after successful password reset
5. **Session Invalidation** - All refresh tokens are revoked after password reset

## Monitoring

### Key Metrics to Track

- Email delivery rate (via SendGrid dashboard)
- Bounce rate
- Spam complaints
- Password reset completion rate

### SendGrid Dashboard

Monitor your email performance at:
- https://app.sendgrid.com/statistics
- https://app.sendgrid.com/email_activity
