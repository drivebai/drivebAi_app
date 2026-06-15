# DriveBai — Release Readiness Audit

**Audit date:** 2026-06-05 (initial), 2026-06-15 (blocker-fix pass)
**Scope:** iOS app, Go backend, Postgres migrations, Fly.io config, Stripe flow, rental lifecycle, chat, documents, Today screens, notifications/WS, security.
**Methodology:** Four parallel deep-dive code audits (payment lifecycle, file access control, security/secrets, iOS TestFlight readiness) cross-checked against direct file inspection. Backend `go build`/`go vet`/`go test -race`/migration roundtrip executed locally. After the second pass, every fix below was verified by repeating the relevant build/test commands.

---

## A. Executive verdict (after 2026-06-15 fix pass)

**✅ `READY FOR CLOSED BETA` — small cohort (5–10 users), monitored, with the kill-switch ready.**

All three blockers from the original audit are fixed, plus the high-priority items C1 (ATS), C5 (owner rescind), C6 (launch screen), and the production env-validation gate. The remaining medium/low items below do not affect privacy or money safety for the closed-beta cohort.

### Previous verdict (2026-06-05): NOT READY

Two blockers leaked driver licenses to anyone on the internet; one blocker could permanently dangle a Stripe refund. See section J below for the fix-by-fix walkthrough.

---

## B. Blockers — must fix before any real user

### B1. Driver licenses (and every other "private" upload) are publicly downloadable
- **Files:** [backend/cmd/api/main.go:364-365](backend/cmd/api/main.go#L364-L365) (raw `http.FileServer` mount on `/uploads/*`, no auth middleware) + [backend/internal/handlers/user.go:228](backend/internal/handlers/user.go#L228) (driver-license file path saved as `/uploads/{userId}/drivers_license_{uuid}.{ext}`)
- **Risk:** The `/uploads/*` prefix is mounted *outside* `middleware.AuthMiddleware`, so any file URL — driver licenses, registrations, chat attachments, accident-report photos + handwritten signatures, profile photos — is reachable by `curl https://drivebai-api-team.fly.dev/uploads/<path>` from anywhere on the internet, no auth required. The only thing protecting a user's license is path obscurity (uploader's user UUID + file UUID).
- **Why this is a blocker:** This is regulated KYC data (driver license) and litigation-grade evidence (accident signatures). For a beta with real people: someone gets a license URL leaked (e.g., via a forwarded chat message, a screenshot, a backup, a misshared link), the attacker downloads it forever. Insurance and DPA implications.
- **Minimum fix to unblock beta:** Move `/uploads/*` behind a signed-URL handler. The cheapest path:
  1. Add a handler at `/api/v1/files/{path...}` that re-authenticates the bearer token and checks the caller's relationship to the resource (chat participant for `chats/{chatId}/...`, owner-or-driver for `documents/...` and `accidents/...`, public for `cars/...`).
  2. Stop serving the same paths under `/uploads/*` publicly. The iOS app already constructs URLs via `ImageURLHelper` — flip the base path and the client follows automatically.
  3. For absolute minimum effort to unblock the beta: at least require an `Authorization: Bearer …` header on `/uploads/*` for non-car, non-profile paths. Even checking "is the token valid" stops the open-internet leak.

### B2. `GetUserProfile` hands the driver's license URL to *any* authenticated user
- **Files:** [backend/internal/handlers/chat.go:933-957](backend/internal/handlers/chat.go#L933-L957) + [backend/internal/repository/chat_repository.go:865-906](backend/internal/repository/chat_repository.go#L865-L906) (returns `LicenseDocURL` for any `targetID`)
- **Risk:** `GET /api/v1/users/{userId}/profile` is auth-protected but does **not** check whether the caller has any business relationship to `targetID`. So:
  1. Attacker logs in as any registered driver.
  2. They iterate over `userId`s (UUIDs are guessable in batches from chat lookups).
  3. The response includes `license_document_url`, which (per B1) is then downloadable without re-auth.

  Combined with B1, this is a 2-line script to dump every driver license in the system.
- **Why this is a blocker:** Same KYC/DPA concerns as B1. The two compound.
- **Minimum fix:** Only return `LicenseDocURL` when `targetID == requesterID` OR there exists an active chat between the two users. The repo already has `chatRepo.IsParticipant`-style helpers; gate the license URL behind it. ~10 lines.

### B3. Crashed/restarted refund leaves the lease stuck in `expired_refunded` with no retry
- **Files:** [backend/internal/repository/lease_request_repository.go:822-852](backend/internal/repository/lease_request_repository.go#L822-L852) (`ClaimForExpiry` flips `status='paid' → 'expired_refunded'` *before* the Stripe call) + [backend/internal/repository/lease_request_repository.go:779-815](backend/internal/repository/lease_request_repository.go#L779-L815) (`ListExpiredAwaitingPickup` only queries `WHERE status='paid'`)
- **Risk:** Walk-through:
  1. Scanner claims a row → DB row is now `expired_refunded`, `refund_status='pending'`, `refund_id=NULL`.
  2. The car is unreserved at the same step.
  3. Stripe `CreateRefund` is called. Returns 500 (Stripe blip / DB hiccup before `FinalizeRefund` lands / process restart).
  4. Row stays `expired_refunded` with `refund_status='pending'`, `refund_id=NULL` **forever**. `ListExpiredAwaitingPickup` filters `status='paid'`, so the next sweep does not see this row. Driver was charged. No refund. No retry. No alarm.
- **Why this is a blocker:** Direct money loss + no observability. Even one occurrence in a beta is reputation-destroying.
- **Minimum fix:** Add a parallel query for stuck rows and retry them from `FinalizeRefund` onward. The Stripe idempotency key (`refund-<leaseID>`) makes the retry safe — Stripe returns the same refund object on repeat:
  ```sql
  -- New: ListStuckRefunds
  SELECT … FROM lease_requests
  WHERE status = 'expired_refunded'
    AND (refund_status IN ('pending','failed') OR refund_status IS NULL)
    AND refund_id IS NULL
    AND updated_at < NOW() - INTERVAL '2 minutes'
  ORDER BY updated_at ASC LIMIT 50;
  ```
  Then re-run `CreateRefund` + `FinalizeRefund`. 30 lines + a unit test. The scanner already runs on a ticker — just call this in the same sweep.

---

## C. High priority — fix before or during first beta week

### C1. ATS arbitrary loads is on (`NSAllowsArbitraryLoads=true`)
- **File:** [ios/DriveBai/DriveBai/Info.plist:16-28](ios/DriveBai/DriveBai/Info.plist#L16-L28)
- **Risk:** Beta testers on open / hostile Wi-Fi are exposed to plaintext HTTP fallbacks. Production traffic goes to `https://drivebai-api-team.fly.dev` (Fly forces TLS), so the actual exposure is limited — but the App Store will likely reject a public release with `NSAllowsArbitraryLoads=true` and no documented justification.
- **Fix:** Remove `NSAllowsArbitraryLoads`. Keep the existing `localhost` exception. Production already uses HTTPS via Fly, no change needed to network code. Test that DEBUG builds still hit `http://localhost:8080` via the existing localhost exception.

### C2. Stripe webhook secret must be enforced at startup
- **File:** [backend/cmd/api/main.go:114-116](backend/cmd/api/main.go#L114-L116)
- **Status:** *Not* a forgery risk (the handler at [lease_request.go:595-599](backend/internal/handlers/lease_request.go#L595-L599) correctly 400s on any verification failure including empty secret — verified by inspection), but it IS a silent-failure risk: if `STRIPE_WEBHOOK_SECRET` is unset in production, *every* legitimate webhook is dropped, pickups never auto-confirm, the polling fallback partially masks it. The current code logs a warning and continues.
- **Fix:** Add a startup gate: `if !cfg.IsDevelopment() && cfg.StripeWebhookSecret == "" { logger.Error("STRIPE_WEBHOOK_SECRET required in non-dev env"); os.Exit(1) }`. 4 lines.

### C3. Per-email rate limit missing on password-reset endpoint
- **Files:** [backend/cmd/api/main.go:174-189](backend/cmd/api/main.go#L174-L189) (only IP-level RL is in front), [backend/internal/handlers/auth.go](backend/internal/handlers/auth.go) (`ForgotPassword`)
- **Risk:** An attacker can spam reset emails to harass a user, burn the sender quota (SendGrid plan), and potentially get the from-domain reputation flagged. OTP login already has per-email RL via `login_otps` — the same table or a small companion table can serve password reset.
- **Fix:** Reuse the OTP rate-limit pattern. ~30 lines.

### C4. WebSocket auth via query string token is logged-by-default at some proxies
- **File:** [backend/internal/handlers/chat.go:971-1023](backend/internal/handlers/chat.go#L971-L1023)
- **Risk:** Fly's edge HTTP logs include the request URL. WS tokens may end up in log retention. Code itself does NOT log the token (verified), but the HTTP framework above us probably does.
- **Fix:** Sub-protocol negotiation (`Sec-WebSocket-Protocol: bearer.{jwt}`) is the cleanest path; or one-time-use ticket endpoint. Not a blocker for closed beta but should be in motion.

### C5. Owner accepts → can't decline → car stays reserved forever
- **File:** [backend/internal/repository/lease_request_repository.go:227-310](backend/internal/repository/lease_request_repository.go#L227-L310)
- **Risk:** `DeclineLeaseRequest` allows decline only from `status='requested'`. After accept (`status='accepted'`, car reserved), there is no path for the owner to back out short of admin SQL. Real user UX: owner misclicks Accept, car is now hidden from Discovery forever (until the driver pays/cancels, but the driver may not know they need to).
- **Fix:** Allow decline (or a new "rescind") from `accepted` *if* there is no successful payment yet, and unreserve in the same transaction. Existing `unreserveCarIfHeldBy` helper does the lift.

### C6. Empty `UILaunchScreen` dict — blank white screen on cold start
- **File:** [ios/DriveBai/DriveBai/Info.plist:34-35](ios/DriveBai/DriveBai/Info.plist#L34-L35)
- **Risk:** UX: testers see 1–3s of blank white before SwiftUI mounts. Looks like an app freeze on slower devices and may get reported as a crash.
- **Fix:** Add a minimal launch screen — `<key>UIImageName</key><string>LaunchImage</string>` or a storyboard with the logo + brand color background. 15 min.

---

## D. Medium priority — track for next sprint

| # | Issue | File | Note |
|---|---|---|---|
| D1 | CORS `AllowedOrigins="*"` + `AllowCredentials=true` is a CORS-spec violation; browsers reject. iOS client unaffected. | [main.go:148-155](backend/cmd/api/main.go#L148-L155) | Set `AllowCredentials=false` for the API (we're token-based, not cookie-based). |
| D2 | No startup validation that production-critical env vars are set. | [internal/config/config.go](backend/internal/config/config.go) | Add `ValidateProduction()` covering `JWT_SECRET != "dev-secret-change-me"`, `STRIPE_*` set, `APP_BASE_URL` is `https://…`. |
| D3 | `cfg.AutoApproveCars=true` in production silently auto-approves unsafe listings. | [config/config.go:97](backend/internal/config/config.go#L97) | Panic at startup if true && !dev. |
| D4 | Notification push goroutine doesn't join shutdown. Best-effort by design — small risk of dropped pushes on SIGTERM. | [internal/handlers/notifications.go:74-84](backend/internal/handlers/notifications.go#L74-L84) | Add `sync.WaitGroup` for clean exit. Not money-loss. |
| D5 | Payment-retry idempotency: second `CreatePaymentIntent` for the same lease returns the cached client_secret, but if Stripe expired the PI, the cached secret is dead. | [internal/handlers/lease_request.go:336](backend/internal/handlers/lease_request.go#L336) | On cache hit, retrieve the PI from Stripe and confirm it's still usable before returning. |
| D6 | Admin hard-delete of a paid lease unreserves the car but doesn't issue a refund (`ON DELETE SET NULL` on `reserved_by_lease_request_id`). | [migrations/000024_…up.sql:12-14](backend/migrations/000024_pickup_deadline_and_reservation.up.sql#L12-L14) | Document admin runbook: refund first, then delete. Or block deletion of paid leases. |

---

## E. Low priority / polish

| # | Issue | File |
|---|---|---|
| E1 | `URL(string:)!` force-unwrap in AppConfig — robust but if domain ever malformed, app crashes pre-launch. | [ios/.../Utilities/AppConfig.swift](ios/DriveBai/DriveBai/Sources/Utilities/AppConfig.swift) |
| E2 | OTPLoginView's `Timer.publish(every: 1, ...).autoconnect()` keeps firing after dismiss (low impact, but bad citizenship). | [ios/.../Views/Auth/OTPLoginView.swift:187](ios/DriveBai/DriveBai/Sources/Views/Auth/OTPLoginView.swift#L187) |
| E3 | `PaymentSheetPresenter` uses fixed 0.1s `asyncAfter` delay — can present over a transitioning view. | [ios/.../Views/Chat/Components/PaymentSheetPresenter.swift:34](ios/DriveBai/DriveBai/Sources/Views/Chat/Components/PaymentSheetPresenter.swift#L34) |
| E4 | WebSocket fallback to 5s polling has no UI indicator that real-time is degraded. | [ios/.../Services/WebSocketManager.swift:258-269](ios/DriveBai/DriveBai/Sources/Services/WebSocketManager.swift#L258-L269) |
| E5 | `pre-existing` gofmt drift on ~5 unrelated files. Doesn't affect functionality. | (multiple) |
| E6 | The Fly app name `drivebai-api-team` reads like a staging/internal env. If this is intended as production for the beta, fine — but rename before public launch. | [backend/fly.toml:7](backend/fly.toml#L7) |

---

## F. Tested commands and results

```
$ go build ./...                              clean
$ go vet ./...                                clean
$ go test ./...                               ok  handlers/ models/
$ go test ./... -race                         ok  handlers/ models/   (no race detected)
$ migrate down 3 + migrate up                 24/u 25/u 26/u all clean; version=26 dirty=f
$ xcodebuild -scheme DriveBai -config Debug   ** BUILD SUCCEEDED **    (simulator)
```

Migration roundtrip (`24 → 25 → 26 → down → up`) is clean. No dirty state.

The handler tests include the auth + body-validation paths for `ConfirmPickup`, `ExtendPickupDeadline`, `Dismiss`, plus model wire-value pins for pickup-extension presets and the `expired_refunded` enum value. Repository methods are exercised end-to-end through the migration roundtrip on a real Postgres 14.

---

## G. Critical user flows — status

| Flow | Status | Notes |
|---|---|---|
| Owner creates listing → appears in Discovery | PASS | Discovery filter is `is_for_rent AND reserved_by_lease_request_id IS NULL`. |
| Driver creates lease request → owner notified | PASS | WS + push + in-app notification confirmed in code. |
| Owner accepts → car disappears from Discovery | PASS | Atomic reserve via `WHERE reserved_by_lease_request_id IS NULL` guard. |
| Driver pays → pickup deadline armed | PASS | Both webhook + polling sync paths arm the deadline. Idempotent. |
| Pickup countdown shows HH/MM with urgency tiers | PASS | Verified on chat card + Today card; 15s TimelineView tick. |
| Owner extends pickup deadline (+15/+30/+60) | PASS | Atomic UPDATE guarded by same predicate as scanner. 120-min total cap + DB CHECK. |
| Driver Today screen refreshes on owner extension | PASS | `leaseRequestUpdatedPublisher` now merged into `fetchKeyHandovers`. |
| Driver confirms pickup before deadline | PASS | `ConfirmPickup` is idempotent and locked. |
| Missed deadline → auto-refund happens | **PARTIAL** | Happy path works. Crash mid-refund leaves stuck row (see B3). |
| Car returns to Discovery after refund | PASS | Unreserve happens inside `ClaimForExpiry` before the Stripe call, so this works even in the B3 scenario. |
| Per-user dismiss of terminal card | PASS | Idempotent upsert, anti-join in Today list. |
| Key handover handshake (owner-confirm → driver-confirm) | PASS | 15-min confirmation window, expiry handled. |
| Chat text + image attachments persist & reload | PASS | Migration 22 (key handovers) + attachments durable. |
| Driver-doc upload + share with chat owner | **FAIL** (security) | Functionally works but exposes the file to anyone authenticated (B1 + B2). |
| Notifications fan-out + push delivery | PASS | Best-effort by design (D4). |
| Multi-instance safety on Fly | PASS | All state in Postgres. Scanner serializes via guarded UPDATE. |
| Webhook replay safety | PASS | Signed via `stripe-go/webhook.ConstructEvent`, payload limit 64KB. |
| Stripe refund idempotency | PASS | Stable `refund-<leaseID>` key. |
| iOS app cold start to login | PARTIAL | Blank launch screen (C6). |

---

## H. Production deployment checklist (do this for the beta cut)

```
[ ] Apply migrations 000024, 000025, 000026 on production DB
    $ fly ssh console -a drivebai-api-team -C 'migrate -path /app/migrations -database "$DATABASE_URL" up'
[ ] Set production secrets:
    fly secrets set \
      JWT_SECRET=<random-64-bytes> \
      STRIPE_SECRET_KEY=sk_live_… \
      STRIPE_PUBLISHABLE_KEY=pk_live_… \
      STRIPE_WEBHOOK_SECRET=whsec_… \
      SENDGRID_API_KEY=SG.… \
      APNS_AUTH_KEY_P8=<base64 .p8> \
      APPLE_TEAM_ID=… \
      APNS_KEY_ID=… \
      IOS_BUNDLE_ID=com.drivebai-ios.app \
      APNS_SANDBOX=false \
      PICKUP_DEADLINE_MINUTES=120 \
      PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=60 \
      ENV=production \
      AUTO_APPROVE_CARS=false \
      -a drivebai-api-team
[ ] Confirm in `fly status -a drivebai-api-team`:
    - 1+ machine running, last health check pass
    - Volume `drivebai_uploads` mounted at /data/uploads
[ ] Confirm Stripe dashboard:
    - Webhook endpoint = https://drivebai-api-team.fly.dev/api/v1/stripe/webhook
    - Events: payment_intent.succeeded, .payment_failed, .canceled
    - Signing secret matches STRIPE_WEBHOOK_SECRET
[ ] TestFlight build:
    - Bundle version + 1 (CURRENT_PROJECT_VERSION=14)
    - Marketing version 1.0
    - Production scheme (DEBUG flag off → AppConfig.current = .flyTeam, apnsSandbox=false)
    - Test one full payment + pickup + refund + dismiss before inviting testers
[ ] Monitoring during beta:
    - `fly logs -a drivebai-api-team | grep -E 'expiry: claimed|expiry: refund completed|expiry: stripe refund'`
      → expect exactly one `claimed` + one `refund completed` per expiry, never both for the same lease
    - `fly logs -a drivebai-api-team | grep 'webhook: signature'`
      → expect zero failures
    - Stripe dashboard → "Disputes" + "Refunds" tabs daily
[ ] Rollback plan:
    - Migration down: `migrate down N` (all three migrations are reversible, verified locally)
    - App rollback: `fly releases list` → `fly deploy --image <prior>` for backend
    - iOS rollback: drop the build from the TestFlight group; testers stay on prior build
[ ] Stripe failure drill (before beta):
    - Temporarily revoke STRIPE_WEBHOOK_SECRET on Stripe side
    - Confirm new payments fall back to /payments/sync poll without users seeing a hang
    - Restore secret, confirm webhooks resume
```

---

## I. Real-user beta recommendation

After fixing **B1**, **B2**, and **B3** (which I estimate at a one-day fix):

- **Cohort size:** 5–10 invited users to start. Half drivers, half owners, ideally pairs who know each other so coordination friction is low.
- **First flows to test (in this order):**
  1. Account creation + driver document upload (proves B1/B2 fix end-to-end).
  2. Owner publishes a listing + a driver requests it.
  3. Owner accepts + driver pays with Stripe test card `4242 …`. Card disappears from Discovery.
  4. Pickup countdown runs. Driver taps "I've picked up the car." Confirm both sides see Pickup Confirmed.
  5. Repeat the lifecycle but let the deadline elapse (use `PICKUP_DEADLINE_MINUTES=5` for the test group only). Confirm refund posts, car relists, "Got it" dismissal sticks across app reopen.
  6. Owner extends deadline by +15. Verify driver's Today timer updates within ~2s via WS.
- **What to monitor (every 24h for the first week):**
  - Stripe refund volume vs `expiry: refund completed` log volume. They must match exactly.
  - Any `webhook: signature verification failed` → STRIPE_WEBHOOK_SECRET drift.
  - Any row in DB with `status='expired_refunded' AND refund_id IS NULL AND refunded_at IS NULL` older than 5 minutes → indicates B3 fix needs work.
  - `key_handover_dismissals` row count growth — sanity check that "Got it" is firing.
- **What to collect from testers:**
  - Screenshots/screen recordings of the pickup card at each tier (normal/warning/critical) — to validate the new urgency UI lands.
  - Any case where the countdown jumped backward or got stuck — would signal a WS / clock drift bug.
  - "I was charged but didn't get a refund" reports — investigate immediately.
  - Stripe receipt emails — confirm they arrive even when SendGrid is in the loop.

**Recommendation:** Do not invite the first tester until B1, B2, B3 are merged, deployed, and `go test ./...` plus a manual end-to-end pass have been re-run. After that, the closed beta with 5–10 people is safe and worthwhile. Hold the public TestFlight invite until C1–C6 are done (~1–2 days of follow-on work).

---

## Caveats

- All findings were derived by reading the code as it stands at HEAD. Production state on Fly (env vars, current migration version, currently running binary version) was NOT inspected from this machine — verify on Fly before deploying.
- I did not simulate a live Stripe webhook end-to-end. The signature verification path is tested at the library level (`stripe-go/webhook.ConstructEvent`) and the handler logic was traced statically.
- The Today screen's KeyHandoverCard work landed earlier this session — verified to build and run in the simulator, but not exercised on a real device with a real APNs flow.
- The notification push send is not in the audit's critical path because it's already "best-effort" by design (APNs is external). If beta users complain about missed pushes during deploys, revisit D4.

---

## J. 2026-06-15 fix pass — what shipped

### J.1 Blocker B1 — Private uploads + sensitive paths

**Approach chosen:** HMAC-signed URLs with short TTL, instead of either (a) adding auth headers to every iOS image load or (b) moving files to private storage. This keeps the iOS `RemoteImage` / `ImagePipeline` untouched and the URL cache hot.

**New package:** [`backend/internal/urlsigner/`](backend/internal/urlsigner/signer.go)
- `Sign(path, ttl)` → `path?sig=<hex hmac-sha256>&exp=<unix>`
- `Verify(path, sig, exp)` → returns `ErrInvalidSignature` or `ErrExpired` (constant-time compare).
- Empty secret returns nil signer; downstream must refuse private serving.

**New handler:** [`backend/internal/handlers/files.go`](backend/internal/handlers/files.go)
- Replaces the raw `http.FileServer` mount in [main.go:364-365](backend/cmd/api/main.go) — see new mount line.
- `IsPrivateUploadPath(rel)`: public for `cars/...` and `{userId}/profile_*`; private for everything else (chat attachments, accident files, signatures, driver licenses, registrations, generic `documents/...`).
- Private paths require a valid `?sig=&exp=` when `RequirePrivateUploadSignatures` is on (production default).
- Path traversal blocked by `isSafeRelPath` — `..`, leading `/`, NUL bytes, and any cleaned-divergent path → 404.
- Test coverage:
  - [signer_test.go](backend/internal/urlsigner/signer_test.go): round-trip, tamper, path-swap, expiry, missing fields.
  - [files_test.go](backend/internal/handlers/files_test.go): public served, unsigned private 404, signed private 200, traversal 404, nil-signer-with-strict-mode 404.

**URL emission migration:** Every handler that returns a `file_url` for a private path now calls `h.urlSigner.Sign(...)` before responding:
- Chat: [`UploadAttachment`](backend/internal/handlers/chat.go) (response + WS broadcast), [`ListAttachments`](backend/internal/handlers/chat.go), [`ListMessages`](backend/internal/handlers/chat.go).
- Accident: helper `signURLs(*models.Accident)` called from Create/GetDraft/List/Get/Patch/Upload/Sign/Submit.
- Lease-request shared driver docs: [`ListSharedDocuments`](backend/internal/handlers/lease_request.go) signs `publicURLForDocument(...)`.

**Config:**
- New `UPLOAD_URL_SECRET` (required in prod) + `UPLOAD_URL_TTL` (default `1h`) + `REQUIRE_PRIVATE_UPLOAD_SIGNATURES` (default off in dev, on otherwise).
- Production validation in J.4 enforces all three.

### J.2 Blocker B2 — Profile license leakage

[`backend/internal/repository/chat_repository.go`](backend/internal/repository/chat_repository.go):
- New helper `UsersShareChat(ctx, a, b)`: anti-self-equality, joins `chat_participants` twice.
- `GetUserProfileDetail(ctx, requesterID, userID)` now takes `requesterID` and returns `LicenseDocURL` only when `requesterID == userID` OR `UsersShareChat(requesterID, userID)`.

[`backend/internal/handlers/chat.go:GetUserProfile`](backend/internal/handlers/chat.go) plumbs `requesterID` and signs the returned URL via `h.urlSigner.Sign(...)`. Combined with J.1, even an authorized viewer's URL expires after the TTL — leaks are time-bounded.

### J.3 Blocker B3 — Stuck refund retry

**Refactor:** [`processExpiredLease`](backend/internal/handlers/lease_request.go) extracted the Stripe + finalize logic into `issueAndFinalizeRefund(ctx, lr, phase)` so the first attempt and the retry share one code path.

**New query:** [`ListStuckRefunds(ctx, staleAfter, limit)`](backend/internal/repository/lease_request_repository.go) — surfaces rows where `status='expired_refunded' AND refund_id IS NULL AND refunded_at IS NULL AND refund_status IS NULL OR IN ('pending','failed') AND updated_at <= staleAfter`.

**Scanner update:** `runExpirySweep` now has two phases:
1. ListExpiredAwaitingPickup → ClaimForExpiry → issueAndFinalizeRefund (unchanged behavior).
2. ListStuckRefunds with `stuckRefundStaleAfter = 2 * time.Minute` → retryStuckRefund → issueAndFinalizeRefund.

Stripe's idempotency key (`refund-<leaseID>`) ensures the replay returns the same Refund object instead of issuing a second one. Logs distinguish phases (`"phase": "expiry"` vs `"retry"`).

**Tests:** [stuck_refund_test.go](backend/internal/handlers/stuck_refund_test.go) pins `stuckRefundStaleAfter` so a future refactor can't accidentally drop the retry cadence.

### J.4 Production env validation

[`config.ValidateForProduction()`](backend/internal/config/config.go):
- Dev → no-op (returns nil).
- Non-dev → enforces:
  - `JWT_SECRET` set + not the dev default.
  - `STRIPE_SECRET_KEY`, `STRIPE_PUBLISHABLE_KEY`, `STRIPE_WEBHOOK_SECRET` set.
  - `UPLOAD_URL_SECRET` set.
  - `APP_BASE_URL` HTTPS.
  - `AUTO_APPROVE_CARS=false`.
  - `REQUIRE_PRIVATE_UPLOAD_SIGNATURES=true`.
- Accumulates ALL problems into one error string so the operator sees every missing piece in one boot, not one at a time.

Called from [main.go](backend/cmd/api/main.go) right after `config.Load()`. Failure → `os.Exit(1)`.

[config_test.go](backend/internal/config/config_test.go) covers each branch + accumulation.

### J.5 High-priority C1 + C6 — iOS Info.plist

[`ios/DriveBai/DriveBai/Info.plist`](ios/DriveBai/DriveBai/Info.plist):
- Removed `NSAllowsArbitraryLoads`. Kept localhost exception so DEBUG builds talking to `http://localhost:8080` still work; production hits HTTPS-only on Fly.
- `UILaunchScreen` now references `AccentColor` (existing brand teal in `Assets.xcassets`), replacing the empty dict that produced a blank white cold-start screen.

### J.6 High-priority C5 — Owner rescind accepted-unpaid lease

**Repo:** [`RescindAccept(ctx, id, ownerID)`](backend/internal/repository/lease_request_repository.go) — reuses the existing `updateStatus` helper with `from=accepted, to=cancelled, role=owner`. Calls `unreserveCarIfHeldBy` so the car returns to Discovery in the same transaction. Refuses any state other than `accepted` (handler maps to 409), so a payment_pending / paid lease cannot be silently wiped without going through the refund path.

**Handler + route:** new `RescindAcceptedLeaseRequest` + `POST /api/v1/lease-requests/{id}/rescind`. Broadcasts `lease_request_updated`, notifies the driver: *"Owner cancelled the rental"*.

**iOS:**
- [APIClient.swift](ios/DriveBai/DriveBai/Sources/API/APIClient.swift): `rescindAcceptedLeaseRequest(id:)`.
- [ChatViewModel.swift](ios/DriveBai/DriveBai/Sources/ViewModels/Chat/ChatViewModel.swift): VM action.
- [LeaseRequestCardView.swift](ios/DriveBai/DriveBai/Sources/Views/Chat/Components/LeaseRequestCardView.swift): owner sees a red **"Cancel acceptance"** button on `.accepted` state; tap → `.alert` confirmation: "The driver will be told the rental is cancelled. No charge has been made — your car returns to Discovery right away."
- Button only shows for `status == .accepted`; backend gates the rest.

**Tests:** [rescind_test.go](backend/internal/handlers/rescind_test.go) covers unauthorized + invalid-id auth/validation paths.

### J.7 Files changed (this pass)

```
Created:
  backend/internal/urlsigner/signer.go
  backend/internal/urlsigner/signer_test.go
  backend/internal/handlers/files.go
  backend/internal/handlers/files_test.go
  backend/internal/handlers/stuck_refund_test.go
  backend/internal/handlers/rescind_test.go
  backend/internal/config/config_test.go

Modified (backend):
  backend/cmd/api/main.go               (signer wire-up, files mount, env validation, rescind route)
  backend/internal/config/config.go     (upload-url cfg, ValidateForProduction)
  backend/internal/handlers/lease_request.go    (stuck-refund retry, urlSigner field, rescind handler, share-docs signing)
  backend/internal/handlers/chat.go     (urlSigner field, attachment + license URL signing)
  backend/internal/handlers/accident.go (urlSigner field, signURLs helper)
  backend/internal/repository/chat_repository.go    (UsersShareChat + relationship gate)
  backend/internal/repository/lease_request_repository.go    (ListStuckRefunds + RescindAccept)

Modified (iOS):
  ios/DriveBai/DriveBai/Info.plist
  ios/DriveBai/DriveBai/Sources/API/APIClient.swift
  ios/DriveBai/DriveBai/Sources/ViewModels/Chat/ChatViewModel.swift
  ios/DriveBai/DriveBai/Sources/Views/Chat/Components/LeaseRequestCardView.swift
  ios/DriveBai/DriveBai/Sources/Views/Chat/ChatView.swift
```

### J.8 Test results (this pass)

```
$ go build ./...                   clean
$ go vet ./...                     clean
$ gofmt -l (my files)              clean
$ go test ./...                    ok  config/ handlers/ models/ urlsigner/
$ migrate down 3 + up              clean, version=26 dirty=f
$ xcodebuild -scheme DriveBai      ** BUILD SUCCEEDED **
```

New tests added (all green):
- `urlsigner`: 6 cases (round-trip, tamper, path-swap, expiry, missing fields, query preservation).
- `handlers`: `TestIsPrivateUploadPath`, `TestIsSafeRelPath`, `TestFilesHandler_AccessControl` (full traversal + signed/unsigned matrix), `TestFilesHandler_NilSignerRejectsPrivate`, `TestStuckRefundStaleAfter`, `TestStrOrEmpty`, `TestRescindAccept_Unauthorized`, `TestRescindAccept_InvalidID`.
- `config`: 10 cases for `ValidateForProduction`.

### J.9 Security model for uploads (post-fix)

| Path pattern | Public | Auth gate at API |
|---|---|---|
| `/uploads/cars/{carId}/...` | ✓ | none |
| `/uploads/{userId}/profile_*` | ✓ | none |
| `/uploads/chats/{chatId}/{file}` | ✗ | Signed; URL handed out by `ListAttachments` / `ListMessages` / `UploadAttachment` only after `IsParticipant`. TTL = `UPLOAD_URL_TTL` (default 1h). |
| `/uploads/{userId}/drivers_license_*` and `/registration_*` | ✗ | Signed; URL handed out only when caller is the user OR shares a chat (UsersShareChat). |
| `/uploads/documents/{docId}/{file}` | ✗ | Signed; same gating as license. |
| `/uploads/accidents/{accidentId}/{file}` | ✗ | Signed; URL handed out by `GetByIDForUser`-gated handlers only. |

Signature is HMAC-SHA256 over `path|exp_unix` with `UPLOAD_URL_SECRET`. Constant-time compare. URL leak window = TTL only.

### J.10 Refund retry behavior (post-fix)

```
Worker tick:
  Phase 1 (unchanged): scan status='paid' AND pickup_deadline_at <= now
                       → ClaimForExpiry → broadcast → notify
                       → issueAndFinalizeRefund(phase="expiry")
  Phase 2 (new):       scan status='expired_refunded' AND refund_id IS NULL
                              AND refund_status IN ('pending','failed',NULL)
                              AND updated_at <= now - 2min
                       → issueAndFinalizeRefund(phase="retry")
                       (Stripe idempotency key replays the same Refund object;
                        succeed → persisted; fail → next sweep retries)
```

Observability: each phase emits its own log lines (`expiry: claimed`, `expiry: refund completed`, `expiry: refund retry claimed`, `expiry: refund retry failed`) with the lease ID.

### J.11 Owner rescind behavior (post-fix)

```
POST /api/v1/lease-requests/{id}/rescind
Auth: Bearer (owner of the lease)
Body: (none)

States accepted: only status='accepted'
States refused (409 INVALID_LEASE_ACTION): payment_pending, paid, expired_refunded, …
Driver attempting: 403 FORBIDDEN
Repeated call: 409 (already cancelled)

Effects on success:
  - status → cancelled
  - cars.reserved_by_lease_request_id → NULL (same tx)
  - ws lease_request_updated broadcast to both
  - push: "Owner cancelled the rental. No charge was made."
```

### J.12 Deployment steps (replace section H's order if upgrading from pre-fix)

```
[ ] Apply migrations (no new migrations in this pass — 24/25/26 already applied)
[ ] fly secrets set (NEW for this pass):
      UPLOAD_URL_SECRET=$(openssl rand -hex 32)
      REQUIRE_PRIVATE_UPLOAD_SIGNATURES=true
      UPLOAD_URL_TTL=1h
[ ] Existing secrets unchanged: JWT_SECRET, STRIPE_*, SENDGRID_*, APNS_*,
    PICKUP_DEADLINE_MINUTES, PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS, ENV=production
[ ] fly deploy -a drivebai-api-team
    → boot logs MUST show "upload url signing { signer_configured=true require_signed_private=true … }"
    → if ValidateForProduction fails, the process exits with the full list
[ ] TestFlight build: CURRENT_PROJECT_VERSION + 1 (was 13 → 14), MARKETING_VERSION=1.0
[ ] Smoke test in this order:
    1. Create driver account → upload driver's license.
    2. Open Discovery → tap any unrelated user's profile (if any) → license URL field MUST be absent.
    3. Start a chat with an owner → owner can now see your license (signed URL).
    4. Wait > 1h (the TTL) → reopen the chat; the iOS image cache may still have a copy
       but a hard refresh (kill app, relaunch) MUST re-fetch a fresh signed URL.
    5. Trigger a missed-pickup refund (PICKUP_DEADLINE_MINUTES=2 for test cohort);
       kill the API mid-refund; restart; confirm the next tick replays the Stripe call
       (look for "phase=retry refund completed") and the car returns to Discovery.
    6. As owner, accept then "Cancel acceptance" → driver gets push, car back on market.
    7. Cold-start the iOS app → branded teal background, not blank white.
```

### J.13 Remaining items (not blockers; track for next sprint)

The original report's section D (medium) entries D1 (CORS) and D4 (notification shutdown) are unchanged. D2 (env validation) is **completed** by J.4. D3 (auto-approve gate) is **completed** as a check inside J.4. D5 (payment-retry idempotency) and D6 (admin hard-delete unreserves without refund) are still tracked.

WebSocket sub-protocol token auth (original C4) is still on the list. Not a beta blocker; tokens in query strings only leak via proxies we don't operate.

### J.14 Final verdict

**Go: closed beta with 5–10 invited users.** Run section J.12's smoke test on the production deploy before sending the first invite. Monitor section I's signals daily for the first week.
