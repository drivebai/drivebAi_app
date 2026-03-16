# DriveBai - Car Marketplace & Rental App

A mobile-first car marketplace and rental application with three user roles:
- **DRIVER** - Users looking for vehicles to rent/drive
- **CAR_OWNER** - Users who own cars and want to find drivers
- **ADMIN** - Administrative users (backend-ready RBAC)

## Architecture Overview

```
DriveBai/
├── backend/           # Go API server
│   ├── cmd/api/       # Application entry point
│   ├── internal/      # Internal packages
│   │   ├── auth/      # JWT, password, OTP handling
│   │   ├── config/    # Configuration management
│   │   ├── database/  # PostgreSQL connection
│   │   ├── email/     # SendGrid + console fallback
│   │   ├── handlers/  # HTTP handlers
│   │   ├── middleware/# Auth, logging middleware
│   │   ├── models/    # Domain models
│   │   └── repository/# Database operations
│   └── migrations/    # SQL migrations
├── ios/               # iOS Swift app
│   └── DriveBai/      # Xcode project
└── docker-compose.yml # Infrastructure
```

## Quick Start

### Prerequisites

- Go 1.22+
- Docker & Docker Compose
- Xcode 15+ (for iOS development)
- Make

### 1. Start the Backend

```bash
# Clone and enter directory
cd "DrivaBai Project"

# Start PostgreSQL and run migrations
make up

# API will be available at:
# - http://localhost:8080        (redirects to docs)
# - http://localhost:8080/docs   (Swagger UI)
# - http://localhost:8080/health (health check)
```

### 2. Run the iOS App

```bash
# Open Xcode project
make ios-open

# Or manually:
open ios/DriveBai/DriveBai.xcodeproj
```

Select a simulator and run (⌘+R).

**Note:** The iOS app connects to `http://localhost:8080` by default. If running on a physical device, update the `baseURL` in `APIClient.swift` to your machine's local IP.

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/auth/register` | Register new user |
| POST | `/api/v1/auth/verify-email` | Verify email with OTP |
| POST | `/api/v1/auth/login` | Login with email/password |
| POST | `/api/v1/auth/token/refresh` | Refresh access token |
| POST | `/api/v1/auth/password/forgot` | Request password reset |
| POST | `/api/v1/auth/password/reset` | Reset password with OTP |
| POST | `/api/v1/auth/logout` | Logout (revoke refresh token) |
| POST | `/api/v1/auth/resend-otp` | Resend OTP code |
| GET | `/api/v1/me` | Get current user profile |

## Environment Configuration

Copy the example environment file:

```bash
cp backend/.env.example backend/.env
```

### Required Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | Server port | `8080` |
| `ENV` | Environment (development/production) | `development` |
| `DATABASE_URL` | PostgreSQL connection string | See .env.example |
| `JWT_SECRET` | Secret for JWT signing | **Change in production!** |
| `JWT_ACCESS_TOKEN_TTL` | Access token lifetime | `15m` |
| `JWT_REFRESH_TOKEN_TTL` | Refresh token lifetime | `720h` (30 days) |

### Email Configuration (SendGrid)

| Variable | Description |
|----------|-------------|
| `SENDGRID_API_KEY` | Your SendGrid API key (optional for dev) |
| `SENDGRID_FROM_EMAIL` | Sender email address |
| `SENDGRID_FROM_NAME` | Sender display name |
| `APP_DEEPLINK_SCHEME` | URL scheme for mobile deep links (default: `drivebai`) |
| `APP_BASE_URL` | Base URL for the API (default: `http://localhost:8080`) |

For detailed email setup and troubleshooting, see [docs/email.md](docs/email.md).

**Development Mode:** If `SENDGRID_API_KEY` is not set, emails are logged to the console instead of being delivered. Look for the banner in your terminal:

```
╔════════════════════════════════════════════════════════════╗
║  📧 VERIFICATION EMAIL (SendGrid not configured)           ║
╠════════════════════════════════════════════════════════════╣
║  To: user@example.com                                      ║
║  Name: John Doe                                            ║
║  Code: 123456                                              ║
╚════════════════════════════════════════════════════════════╝
```

## Email Deliverability Setup (Production)

To ensure emails don't land in spam, you need to properly configure SendGrid with domain authentication.

### Step 1: Create SendGrid Account

1. Sign up at [sendgrid.com](https://sendgrid.com)
2. Create an API Key with "Mail Send" permissions
3. Add to your `.env`: `SENDGRID_API_KEY=SG.xxxxx`

### Step 2: Domain Authentication (Critical for Deliverability)

This is **required** to avoid spam filters.

1. In SendGrid, go to **Settings → Sender Authentication**
2. Click **"Authenticate Your Domain"**
3. Enter your domain (e.g., `drivebai.com`)
4. SendGrid will provide DNS records to add:

#### Required DNS Records

| Type | Host | Value |
|------|------|-------|
| CNAME | `em1234.drivebai.com` | `u1234567.wl.sendgrid.net` |
| CNAME | `s1._domainkey.drivebai.com` | `s1.domainkey.u1234567.wl.sendgrid.net` |
| CNAME | `s2._domainkey.drivebai.com` | `s2.domainkey.u1234567.wl.sendgrid.net` |

5. Add a **DMARC record** for additional security:
   ```
   Type: TXT
   Host: _dmarc.drivebai.com
   Value: v=DMARC1; p=quarantine; rua=mailto:dmarc@drivebai.com
   ```

6. Verify in SendGrid that authentication is complete

### Step 3: Verify Sender Identity

1. Go to **Settings → Sender Authentication → Single Sender Verification**
2. Add your sender email (e.g., `noreply@drivebai.com`)
3. Verify via the confirmation email

### Troubleshooting Emails in Spam

If emails still go to spam:

1. **Check SPF/DKIM** - Use [mxtoolbox.com](https://mxtoolbox.com) to verify
2. **Warm up your IP** - SendGrid gradually increases sending reputation
3. **Use a subdomain** - e.g., `mail.drivebai.com` to protect main domain
4. **Monitor reputation** - Check SendGrid's Reputation Dashboard
5. **Avoid spam triggers** - Don't use ALL CAPS, excessive punctuation, or spammy words

---

## Quality Control Testing Checklist

Use this checklist to verify all authentication flows work correctly.

### ✅ Sign Up Flow

- [ ] Navigate to Sign Up from Login screen
- [ ] Fill in legal name, email, phone (optional), password
- [ ] Email validation shows error for invalid emails
- [ ] Email validation shows checkmark for valid emails
- [ ] Password validation requires 8+ characters
- [ ] Confirm password must match
- [ ] Terms checkbox required
- [ ] Role selection screen shows Driver and Car Owner options
- [ ] Selecting a role and tapping Continue triggers registration
- [ ] Verify email screen appears with OTP input
- [ ] OTP can be entered (6 digits)
- [ ] Invalid OTP shows error message
- [ ] Expired OTP shows specific error message
- [ ] Valid OTP verification succeeds
- [ ] "Resend" button sends new OTP
- [ ] After verification, success screen appears
- [ ] Can navigate to Login from success screen

### ✅ Email Verification

- [ ] In development: OTP appears in server console logs
- [ ] In production: Email arrives in inbox (not spam)
- [ ] Email contains correct 6-digit code
- [ ] Code expires after 10 minutes
- [ ] Multiple rapid requests are rate-limited

### ✅ Login Flow

- [ ] Email and password fields work
- [ ] Empty fields show validation
- [ ] Wrong password shows "Invalid credentials" error
- [ ] Unverified email shows "Please verify your email first" error
- [ ] Successful login stores tokens and shows home screen
- [ ] User profile is loaded correctly
- [ ] Role badge shows correct role (Driver/Car Owner)

### ✅ Forgot Password Flow

- [ ] "Forgot password?" link navigates to reset screen
- [ ] Entering email sends reset code (or logs to console in dev)
- [ ] Non-existent email still shows success (prevents enumeration)
- [ ] OTP entry screen appears
- [ ] Valid OTP proceeds to new password screen
- [ ] Invalid/expired OTP shows error
- [ ] New password requires 8+ characters
- [ ] Confirm password must match
- [ ] Successful reset shows confirmation
- [ ] Can login with new password
- [ ] Old sessions are invalidated after password reset

### ✅ Session Management

- [ ] Access token expires after 15 minutes
- [ ] Expired token triggers automatic refresh
- [ ] Refresh token rotates on use (old token invalidated)
- [ ] Logout clears tokens from Keychain
- [ ] After logout, protected screens require re-authentication

### ✅ Auth Gating (QC Bug Fix)

This specifically addresses the navigation bug noted in QC:

- [ ] Open app (not logged in)
- [ ] Navigate to Discover tab - content shows "Sign in to browse"
- [ ] Navigate to Profile tab - shows "Sign in to view your profile"
- [ ] Tap "Sign In" button - auth flow appears as a **modal sheet**
- [ ] Tap X (close button) - modal dismisses
- [ ] User returns to Profile tab (not stuck)
- [ ] Can freely navigate between tabs while logged out
- [ ] After login, Profile shows user info
- [ ] After logout, returns to logged-out state cleanly

### ✅ Error Handling

- [ ] Network errors show user-friendly message
- [ ] Server errors show appropriate message
- [ ] Rate limiting shows "Too many requests" message
- [ ] All error codes map to readable messages

---

## Development Commands

```bash
# Infrastructure
make up          # Start PostgreSQL + API + run migrations
make up-db       # Start only PostgreSQL
make down        # Stop all containers
make logs        # View API logs

# Database
make migrate     # Run pending migrations
make migrate-down # Rollback last migration

# Development
make run         # Run API locally (requires PostgreSQL)
make build       # Build Go binary
make test        # Run Go tests
make clean       # Clean build artifacts + volumes

# iOS
make ios-open    # Open Xcode project
```

## Security Notes

- **JWT Secret:** Change `JWT_SECRET` in production! Use a cryptographically random string.
- **Password Hashing:** Uses bcrypt with cost factor 12
- **OTP Storage:** OTPs are stored as SHA-256 hashes, never plaintext
- **Refresh Token Rotation:** Each refresh rotates the token, invalidating the old one
- **Rate Limiting:** OTP sends limited to 5 per email per hour
- **HTTPS:** In production, always use HTTPS (configure in your reverse proxy)

## TODOs for Production

- [ ] Set up Redis for distributed rate limiting (currently in-memory)
- [ ] Add Google/Facebook OAuth providers
- [ ] Implement email verification link as alternative to OTP
- [ ] Add phone number verification (SMS OTP)
- [ ] Set up proper logging aggregation (e.g., Datadog, Splunk)
- [ ] Configure HTTPS with Let's Encrypt
- [ ] Add request signing for API security
- [ ] Implement WebSocket for real-time messaging
- [ ] Add proper error tracking (e.g., Sentry)

## License

Proprietary - DriveBai Inc.
