# Reproduction script: a ROLLING (weekly) lease, tap by tap

Written 2026-09-17 against `main` at `07f9b3d` (backend v103 live, iOS build 40 archived, not on TestFlight).

**Evidence legend.** Every behavioural claim carries a tag:

- **[code]** — read at the cited `file:line`.
- **[ran]** — executed on 2026-09-17 against a LOCAL backend (Stripe test keys) and observed.
- **[prod-ro]** — read-only query or log against production. Nothing was written.
- **[believed]** — inference. Stated as such.

**What this run did NOT do, on purpose.** No payment intent was created for a rolling
lease anywhere, including locally, because `CreatePaymentIntent` writes a consent row as a
side effect (`backend/internal/handlers/lease_request.go:914`) and the rule is that no
consent row is ever manufactured. Everything from "Review weekly terms & pay" onward is
therefore **[code]** plus the Stripe test-mode rehearsal suite
(`backend/internal/handlers/rolling_rehearsal_lines_test.go`, 24/24 green on 2026-09-16),
not **[ran]** by me.

**Read-first gap.** `docs/DRIVEBAI_PROJECT_CONTEXT.md` (§6.2, §7.5–7.7, §8) does not exist
anywhere in the repository, tracked or ignored, nor in the parent directory. The code is the
source of truth below.

---

## 0. The one-paragraph answer

The weekly option is a button on the **driver's** listing-detail screen. It is drawn only
when (a) the driver's build is 35 or later, (b) `GET /api/v1/config` returned
`rolling_rentals_enabled: true` for **that driver**, and (c) the driver taps **"Rent weekly
instead"** rather than **"Request lease"**. If any of those three is false, the request is
created as `fixed_term` with an HTTP 201 and **no log line, no metric, no notification** —
the server cannot tell the difference between "this driver chose fixed-term" and "this
driver's app never offered weekly". I reproduced that silence locally **[ran]**. That is
the Sep 14 shape.

---

## 1. Flow map with evidence (iOS → API → DB → scanners)

### a) Login — step 0

| Fact | Evidence |
|---|---|
| Password login exists and needs no email at all. `POST /api/v1/auth/login {email,password}`. | `backend/cmd/api/main.go:375`; handler `internal/handlers/auth.go:234` **[code]**; returned a token locally with no mail provider configured **[ran]** |
| Registration issues a token immediately; no verification email gates it. `POST /auth/register {email,password,first_name,last_name,phone,role}`. | `main.go:374`; `auth.go:50-57` **[code]**; **[ran]** |
| With `MAILERSEND_API_KEY` unset the backend boots with `"MAILERSEND_API_KEY not set — OTP emails will be printed to console"` — OTP codes go to the server log. | boot log **[ran]** |
| In production the mail key is dead, so OTP login does not work there; password login does. | `MEETING_BRIEF.md` §6 (credentials referenced by name only) |

### b) Where rolling eligibility is decided

| Fact | Evidence |
|---|---|
| `GET /api/v1/config` is inside the authenticated router (401 without a token) and answers **per caller**: `{"rolling_rentals_enabled": leaseHandler.RollingOpenFor(uid)}`. | `main.go:416-426` **[code]**; 401 / true / false observed **[ran]** |
| `RollingOpenFor(userID)` = flag on **and** (allowlist empty **or** userID in allowlist). Keyed by **user id**, not email. | `internal/handlers/lease_request.go:2976-2985` **[code]** |
| `ROLLING_ALLOWLIST_USER_IDS`: comma-separated UUIDs. **Empty ⇒ open to everyone.** Non-empty but unparseable ⇒ pilot of nobody (fail closed). | `internal/config/config.go:103-120, 193-196` **[code]** |
| Boot line proving state: `"weekly rentals: ON" allowlisted_drivers=N rejected_entries=[] open_to_everyone=<bool>` | observed with N=0/true and N=1/false **[ran]** |
| **Until v102 (2026-09-16) there was no allowlist code at all.** v101's `/config` returned the bare global flag to every signed-in user. | `git show ee12d49:backend/cmd/api/main.go` lines 398-404; `RollingAllowlist` symbols in v101 `config.go`: 0 **[code]** |
| `ROLLING_RENTALS_ENABLED=true` in `fly.toml` since v99 (2026-09-11 09:04Z). | commit `49ad73c` **[code]** |
| iOS: the fetch runs inside the listing detail's `.task {}` — every time a detail view appears; **not** on login, foreground or account switch; nothing is cached. | `ios/.../Views/Main/DiscoverView.swift:958-963` **[code]** |
| Build 40: `loadWeeklyAvailability()` tries 3 times with 0.4 s / 0.8 s / 1.6 s back-off, then sets a tri-state `.unknown` which renders a tappable row **"Couldn't check weekly rentals — tap to retry"**. | `DiscoverView.swift:1063-1081, 777-780, 1371-1386` **[code]** |
| Builds 35–39: `if let config = try? await APIClient.shared.fetchAppConfig()` — one attempt, failure silently leaves the CTA hidden for as long as that view lives. | `git show 915dbbd:...DiscoverView.swift` line 1051; same in `ee12d49` **[code]** |
| The CTA: `if weeklyRentalsAvailable { Button(action: requestWeeklyLease) { Text("Rent weekly instead") … Text("Renews every 7 days until you return the car") } }`. The standard CTA is `Text("Request lease")`. | `DiscoverView.swift:1395-1400`, `:1356` **[code]** |
| Build 33 (App Store, in review) has **no** `billing_mode`, **no** `rolling_rentals_enabled`, **no** `RollingConsentSheet` in its binary; its only `/config` string is `https://ppm.stripe.com/config`. | `strings -a` on `DriveBai 04.09.2026, 15.04.xcarchive` (build 33 = commit `0f078c7`) **[ran]** |

### c) Request creation — the silent default

| Fact | Evidence |
|---|---|
| Route: `POST /api/v1/listings/{listingId}/lease-requests`. | `main.go:554` **[code]** |
| Body field `billing_mode`. Default `fixed_term`. Rolling **only if** `body.BillingMode != nil && *body.BillingMode == "rolling"` **and** `RollingOpenFor(userID)`; otherwise fixed-term with no branch taken and nothing logged. | `lease_request.go:327-339` **[code]** |
| **Reproduced:** an allowlisted driver whose body omitted the field got `201 … status=requested billing_mode=fixed_term`. The only log line was the middleware's `method=POST path=… status=201`. Grepping the log for `billing_mode|fixed_term|rolling|allowlist` returned nothing. | local run 11:09 **[ran]** |
| The request log middleware records method, path, status, duration_ms, ip — **not User-Agent**, so the build number is never logged. | `internal/middleware/logging.go:54-60` **[code]** |
| The iOS User-Agent **does** carry the build: `DriveBai/13 CFNetwork/… Darwin/…`. It is persisted only by the OTP path (`login_otps.user_agent`). Password login stores nothing. | prod `login_otps` rows for George, June **[prod-ro]** |
| `billing_mode` is written at INSERT only; no production code converts a lease to rolling afterwards. | `internal/repository/lease_request_repository.go:77` **[code]** |
| iOS build 40 sends `CreateLeaseRequestAPIRequest(weeks: 1, message: nil, billingMode: billingMode)`; `billingMode` is `"rolling"` only from `requestWeeklyLease()`; the fixed-term path passes `nil`. | `DiscoverView.swift:1110, 1118, 1125` **[code]** |
| Build 33's `LeaseRequestAPIModels.swift` has no `billingMode` at all. | `git show 0f078c7:…/API/LeaseRequestAPIModels.swift` **[code]** |
| The owner's push says only `"<driver> requested N week(s) for <car>"`; the chat system line says `"New lease request: N week(s) at USD X/week"`. Neither says weekly/rolling. | `lease_request.go:418-420`; `lease_request_repository.go:102` **[code]**; Fenix's Sep 14 chat lines **[prod-ro]** |
| The owner's only visual cue is the card badge `"Renews weekly until returned"` — which needs build 35+ to decode `billing_mode`. | `Views/Chat/Components/LeaseRequestCardView.swift:63-73`; `Models/Chat/LeaseRequestModels.swift:157` **[code]** |

### d) Consent — where it really lives

| Fact | Evidence |
|---|---|
| The **weekly tap does not show the consent sheet.** `requestWeeklyLease()` runs the same "What happens next?" explainer as fixed-term, then POSTs the request. | `DiscoverView.swift:1102-1112` **[code]** |
| Consent is at **checkout**. Driver taps **"Review weekly terms & pay"** (rolling) / **"Pay Now"** (fixed). | `LeaseRequestCardView.swift:817-819` **[code]** |
| That calls `POST /lease-requests/{id}/payments/intent`. For a rolling lease the server: refuses `ROLLING_DISABLED` if the flag is off; else **creates an unactivated consent row** holding the exact disclosure text and version it is about to serve, sets `setup_future_usage=off_session`, and returns `disclosure_text` + `terms_version` with the client secret. | `main.go:587`; `lease_request.go:900-931`; `models.RollingDisclosureFor("weekly", totalCents)` **[code]** |
| iOS: if `disclosure_text` is non-empty it presents `RollingConsentSheet` **before** Stripe's PaymentSheet; the sheet renders the **server's** text verbatim and the version line `"Agreement version: <v>"`. Its button reads **"Pay <amount> and start rental"**; footer: **"Returning the car in the app stops the charges — any day, no notice."** Cancel aborts before any card entry. | `Views/Chat/ChatView.swift:738-753, 317-345`; `Views/Billing/RollingConsentSheet.swift:16, 115, 159, 170-171, 181` **[code]** |
| The row stores: `lease_request_id, driver_id, amount_cents, billing_interval, terms_version, disclosure_text, stripe_payment_method_id, card_brand, card_last4, card_fingerprint, activated_at, revoked_at, revoked_reason, created_at`. | `backend/migrations/000055_billing_cycles_consents.up.sql` **[code]** |
| **Activation** happens in the Stripe webhook on `payment_intent.succeeded`: the handler stamps payment method, brand, last4, fingerprint. If the event carries no `payment_method` it **refuses "until redelivery"** — the lease stays unpaid until Stripe resends. | `lease_request.go:1371-1379`; `internal/repository/billing_repository.go:77-82` **[code]** |
| So a rolling lease **cannot** reach `paid` without an activated consent row, and the consent text is pinned to what the server recorded, not what a stale client renders. There is no client-supplied "I agree" field; the ceremony is enforced by the client showing the server's text before the card sheet. | **[code]**, from the above |

### e) Owner Accept

| Fact | Evidence |
|---|---|
| Route `POST /lease-requests/{id}/accept`; buttons **"Accept"** / **"Decline"**. | `main.go:557`; `LeaseRequestCardView.swift:683, 673` **[code]** |
| Gate 1 — **owner terms**, rolling only: `409 OWNER_TERMS_REQUIRED` with `details.terms_version` and `details.terms_text`. Fixed-term is never gated. | `internal/handlers/owner_terms.go:47-72` **[code]**; **[ran]** locally: first Accept → 409, accept terms via `POST /me/owner-terms/accept {"terms_version":"owner-rolling-v2 (2026-09-10)"}` → second Accept → `accepted | rolling` |
| Which build satisfies it: the `OwnerTermsSheet` shipped in **build 38** (`b634a36`). Every installed iOS build discards `details` except `missing_types`, so on 33–37 the owner sees a generic error with no way through — except support recording it via `POST /admin/users/{id}/owner-terms` (note required). | `Models/User.swift:527-530`; `main.go:691` **[code]** |
| Gate 2 — **Connect onboarding is NOT on Accept.** A non-onboarded owner accepts normally; their **payout** row parks as `awaiting_onboarding` and pays the moment they onboard. | `internal/handlers/payouts.go:218, 455, 502, 517` **[code]**; Accept succeeded locally with a never-onboarded owner **[ran]** |
| Gate 3 — availability/reservation: standard, unchanged. | `lease_request.go` create path **[code]** |

### f) Payment → active

| Fact | Evidence |
|---|---|
| Webhook: `POST /api/v1/stripe/webhook`, no auth, signature-verified with `STRIPE_WEBHOOK_SECRET`. Locally it must be forwarded (`stripe listen --forward-to localhost:8080/api/v1/stripe/webhook`) or **nothing ever goes paid**. | `main.go:399-400`; `main.go:163-172` **[code]** |
| On success the lease goes `paid`, the consent activates, and the pickup deadline arms (`PICKUP_DEADLINE_MINUTES`, 120 in prod). | `lease_request.go:1330-1380` **[code]** |
| Key handover: owner **"I handed over the keys"** → driver **"I received the keys"** (`POST /key-handovers/{id}/owner-confirm`, `/driver-confirm`). | `Models/KeyHandover/KeyHandoverModels.swift:80-85`; `main.go:593-594` **[code]** |
| Pickup: driver **"I've picked up the car"** → `POST /lease-requests/{id}/pickup-confirm`. | `LeaseRequestCardView.swift:729`; `APIClient.swift:979-980`; `main.go:583` **[code]** |
| "Rolling active" = `status='paid' AND billing_mode='rolling' AND pickup_confirmed_at IS NOT NULL AND vehicle_returned_at IS NULL AND renewal_stopped_at IS NULL AND delinquent_since IS NULL AND renewal_halted_reason IS NULL`; `billing_cycles` row #1 is minted shortly after pickup; `rental_ends_at` is paid-through. | mint predicate `lease_request_repository.go:2142-2155` **[code]**; admin copy "week 1 is cycled shortly after pickup" `admin/src/pages/Rents.vue:610` |
| The engine ticks every **60 s** on the pickup-expiry scanner's ticker (`PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS`). | `lease_request.go:1568-1585`; `billing_engine.go:41` **[code]** |

### g) Pre-charge notice

| Fact | Evidence |
|---|---|
| Lead time `BillingNoticeLead = 48h`. | `internal/models/billing.go:44` **[code]** |
| Channel: `notifHandler.Notify` — **push + in-app**, not email. Copy: **"Your rental renews soon"** / **"$X will be charged to your saved card on <date>. Return the car before then to stop."** | `billing_engine.go:147-150` **[code]** |
| Stamp `renewal_notified_for = rental_ends_at`. | `lease_request_repository.go:2194-2197` **[code]** |
| **The mint does not check the notice.** The due-for-billing predicate has no `renewal_notified_for` clause; a failed notice does not delay the charge. | `lease_request_repository.go:2142-2155` **[code]** |
| MailerSend being dead is irrelevant to this notice. | from the above |

### h) Outstanding-balance gate

| Fact | Evidence |
|---|---|
| At create, before the billing-mode branch: `409 OUTSTANDING_BALANCE "You have an unpaid balance of $X from a previous rental. Clear it to start a new one."` Since v103 it blocks only on a debt pay-now can actually take (`BlockingBalanceFor`). | `lease_request.go:298-316`; `driver_debt_repository.go` **[code]** |
| Driver sees the balance at `GET /me/balance` and in `DebtBalanceCard` (build 36+); pays via `RollingBillingCard` **"Pay now"** → `POST /lease-requests/{id}/billing/pay-now`. | `main.go:430, 573`; `Views/Billing/RollingBillingCard.swift:615`; `a3650df` **[code]** |
| Debt arises only from rolling cycles; production has 0 debts. | **[prod-ro]** 2026-09-17 |

### i) Weekly cycle, arrears payout, mid-cycle return

| Fact | Evidence |
|---|---|
| Cycle length 7 d; retry spacing 24 h; max 4 attempts; delinquent at attempt ≥ 3; hard declines never retried: `stolen_card, lost_card, pickup_card, fraudulent, invalid_account, merchant_blacklist, do_not_honor_forever`. | `models/billing.go:25, 47, 49`; `billing_engine.go:454-455, 392-397` **[code]** |
| Owner payouts accrue per cycle and promote `accruing → pending` only when `period_end <= now` — the owner is paid **one week behind** (week 1 becomes payable after week 1 ends). | `billing_engine.go:584`; `internal/repository/payout_repository.go:563-572` **[code]** |
| Driver return: **"Request to Return the Vehicle"** → `POST /lease-requests/{id}/vehicle-return`; owner **"Confirm return"** → `POST /vehicle-returns/{id}/owner-confirm`. | `LeaseRequestCardView.swift:359`; `Models/VehicleReturn/VehicleReturnModels.swift:210-215`; `main.go:598, 603` **[code]** |
| Prorated refund: `used_days = ceil(elapsed/86400)`, floored at 1, capped at paid days; per-day = paid/paid-days. Post-return refund phase issues `cycle-refund-<cycle>` refunds on Stripe. | `models/vehicle_return.go:155-156, 179, 212-213`; `billing_engine.go:1071, 1100` **[code]** |
| Test clock: the **backend's** clock is the database (`rental_ends_at` + `NOW()`); the rehearsal ages rows by SQL shifts on `lease_requests.rental_ends_at`, `billing_cycles.period_*`, `owner_payouts.period_*`, and advances a Stripe **test clock** in step. There is no injectable clock. | `rolling_rehearsal_test.go:20-24, 403-426, 190-206` **[code]** |

---

## 2. Verification of `BUILD_OWNERSHIP_MAP.md`

| Row | Verdict | Evidence |
|---|---|---|
| Weekly 1–3: driver, 35+ | **CONFIRMED** | b, c above |
| Weekly 4: owner Accept, 38+ | **CONFIRMED** for rolling; note it is *only* rolling that is gated, and 33–37 show a generic error (details discarded) | e |
| Weekly 5: "Pays week 1 — 33 works" | **MISMATCH.** A rolling first payment requires the consent sheet, which is build 35+ (`ChatView.swift:745-750`). Build 33 cannot reach it anyway (no `billing_mode`), but the row should read **35+**. | d |
| Weekly 6: billing card 35+ | CONFIRMED | `RollingBillingCard` in `915dbbd` |
| Weekly 7: debt card 36+ | CONFIRMED | `a3650df` |
| Weekly 8: amendments 36+ both | CONFIRMED (not exercised) | `5eaa76d`, `a3650df` |
| **Missing rows** | Pre-charge notice (driver, push, any build); arrears **Pay now** (driver, 35+); key handover both sides (33 works); return initiate (driver, 33 works); owner confirm return (owner, 33 works); owner **Connect onboarding** (owner, any build, gates payout not accept). | f, g, h, i |
| Sale rows | Not re-verified in this run (out of scope). | — |

---

## 3. Local environment — steps

All paths relative to the repo root. Nothing here touches production.

### 3.1 Database

```
createdb repro_rolling
cd backend && migrate -path migrations -database "postgres://$(whoami)@localhost:5432/repro_rolling?sslmode=disable" up
```
Expect the last line `64/u driver_debt_escalated_at`. **[ran]** on a scratch DB.

### 3.2 Backend (port 8080 — the app's hard-coded local port)

```
cd backend && go build -o /tmp/drivebai-api ./cmd/api
export DATABASE_URL="postgres://$(whoami)@localhost:5432/repro_rolling?sslmode=disable"
export STRIPE_SECRET_KEY=<sk_test_… from MEETING_BRIEF.md>        # TEST key only
export STRIPE_PUBLISHABLE_KEY=<pk_test_…>
export STRIPE_WEBHOOK_SECRET=<whsec_… printed by `stripe listen`>  # step 3.4
export JWT_SECRET=local-only PORT=8080 ENV=development UPLOAD_DIR=/tmp/drivebai-uploads
export ROLLING_RENTALS_ENABLED=true DEBT_ENFORCEMENT_ENABLED=true AUTO_APPROVE_CARS=true
export ROLLING_ALLOWLIST_USER_IDS=""            # empty = open to everyone (first boot)
/tmp/drivebai-api
```
Boot lines to look for **[ran]**:
```
MAILERSEND_API_KEY not set — OTP emails will be printed to console
stripe config secret_key_set=true publishable_key_set=true webhook_secret_set=true
weekly rentals: ON allowlisted_drivers=0 rejected_entries=[] open_to_everyone=true
```
`platform_fee_bps=500` locally vs 1000 in prod (env default) — amounts in owner shares will differ from prod by design.

### 3.3 Accounts (via the API, never SQL — except the one admin bootstrap)

```
L=http://localhost:8080/api/v1
# driver
curl -s -X POST $L/auth/register -H 'Content-Type: application/json' -d '{"email":"repro_driver@example.test","password":"Repro-Pass-1234","first_name":"Repro","last_name":"Driver","role":"driver","phone":"+15550000001"}'
# owner
curl -s -X POST $L/auth/register -H 'Content-Type: application/json' -d '{"email":"repro_owner@example.test","password":"Repro-Pass-1234","first_name":"Repro","last_name":"Owner","role":"car_owner","phone":"+15550000002"}'
# admin: register as driver, then promote — there is NO API to create the first admin
curl -s -X POST $L/auth/register … role=driver email=repro_admin@example.test phone=+15550000003
psql repro_rolling -c "update users set role='admin' where email='repro_admin@example.test'"
```
Each register returns `access_token` immediately **[ran]**. Save the driver's `user.id`.

**Restart the backend with `ROLLING_ALLOWLIST_USER_IDS=<driver id>`.** Boot line must now read
`allowlisted_drivers=1 … open_to_everyone=false` **[ran]**. Confirm:
```
curl -s $L/config -H "Authorization: Bearer $DRIVER_TOK"   # {"rolling_rentals_enabled":true}
curl -s $L/config -H "Authorization: Bearer $OWNER_TOK"    # {"rolling_rentals_enabled":false}
```
**[ran]** both.

### 3.4 Stripe webhooks to local

```
stripe listen --forward-to localhost:8080/api/v1/stripe/webhook
```
Copy the `whsec_…` it prints into `STRIPE_WEBHOOK_SECRET` and restart. Without this the lease never becomes `paid` and the consent never activates (`lease_request.go:1371-1379`). **[code]**

### 3.5 Driver licence (gate on booking)

```
curl -s -X POST $L/documents/drivers_license -H "Authorization: Bearer $DRIVER_TOK" -F "file=@any.jpg;type=image/jpeg"
```
A **pending** licence already passes the booking gate — only `rejected` blocks
(`document_repository.go:182-184`). To mirror prod, verify it as admin:
```
curl -s -X PATCH $L/admin/users/<driver id>/documents/<doc id>/status -H "Authorization: Bearer $ADMIN_TOK" -H 'Content-Type: application/json' -d '{"status":"verified"}'
```
Valid statuses are `verified` / `rejected` (`models/user.go:177-178`); `approved` returns 400 **[ran]**.

### 3.6 Listing with a weekly price

```
curl -s -X POST $L/cars/ -H "Authorization: Bearer $OWNER_TOK" -H 'Content-Type: application/json' \
 -d '{"make":"Honda","model":"CR-V","year":2021,"vin":"2HKRW2H58MH600001","body_type":"suv","fuel_type":"gas","mileage":40000,"is_for_rent":true,"weekly_rent_price":150,"rent_price_period":"weekly","rent_price_amount":150}'
```
Pitfalls hit **[ran]**: VIN must be 17 chars (`INVALID_VIN`); `fuel_type` enum is
`gas|diesel|electric|hybrid|plug_in_hybrid` (`"gasoline"` → 500); min weekly price 50
(`MIN_WEEKLY_RENT_PRICE`, `config.go:188`).

The car is created `status='pending'`. `AUTO_APPROVE_CARS` sets `is_approved` only; Discover
defaults to `status=available` (`car.go:1529-1531`), so it stays **invisible** until:
```
for t in registration inspection insurance; do
  curl -s -X POST $L/cars/<car id>/documents -H "Authorization: Bearer $OWNER_TOK" -F "document_type=$t" -F "file=@any.jpg;type=image/jpeg"; done
curl -s -X PATCH $L/admin/cars/<car id>/approve -H "Authorization: Bearer $ADMIN_TOK" -H 'Content-Type: application/json' -d '{"is_approved":true}'
```
Required docs: `models/car.go:107-109`; approval flips `pending → available`:
`admin_repository.go:540-543` **[code]**; Discover then lists it **[ran]**.

**Connect onboarding.** The owner never needs it to accept or be paid by the driver; their
payout row waits in `awaiting_onboarding` (`payouts.go:455-517`). To test the onboarded
path, complete Stripe Connect test onboarding from Profile → Payouts; to test the
NOT-onboarded path, simply don't — and read `owner_payouts.status`.

### 3.7 iOS pointing at local

`ios/DriveBai/DriveBai/Sources/Utilities/AppConfig.swift:11`:
```swift
static var current: BackendEnvironment = .flyTeam   // change to .local
```
There is no runtime switch, scheme env var or xcconfig (`grep` found none). `.local` =
`http://localhost:8080` (`AppConfig.swift:19, 28, 37`); `Info.plist` already exempts
`localhost` from ATS (`Info.plist:26`). Build the `DriveBai` scheme (Debug) to a simulator.
Build number is 40 in `project.pbxproj` on `main`.

**Two simulators** (or one simulator + one device): one signed in as the driver, one as the
owner. The admin actions are `curl` or the Vue console pointed at local.

### 3.8 Stripe test cards (typed into PaymentSheet)

Stripe-documented numbers, any future expiry, any CVC — **[believed]** (documentation, not run here):

| Purpose | Number |
|---|---|
| Success, saves for off-session | 4242 4242 4242 4242 |
| Attaches fine, **later off-session charge fails** (the declined-week-2 card) | 4000 0000 0000 0341 |
| Immediate decline | 4000 0000 0000 0002 |
| Requires 3DS (`needs_action` path) | 4000 0025 0000 3155 |

The rehearsal harness uses tokens `tok_visa`, `tok_chargeCustomerFail`,
`tok_chargeDeclinedLostCard`, `tok_authenticationRequired` **[code]**.

### 3.9 Advancing time (local only)

Stripe side: a **test clock** on the customer (`rolling_rehearsal_test.go:190-206`).
Backend side: the engine reads the database clock, so age the rows exactly as the harness
does (`rolling_rehearsal_test.go:403-426`):
```sql
UPDATE lease_requests SET rental_ends_at = rental_ends_at - interval '7 days' WHERE id = '<lease>';
UPDATE billing_cycles SET period_start = period_start - interval '7 days', period_end = period_end - interval '7 days' WHERE lease_request_id = '<lease>';
UPDATE owner_payouts SET period_start = period_start - interval '7 days', period_end = period_end - interval '7 days' WHERE lease_request_id = '<lease>' AND period_end IS NOT NULL;
```
Then wait ≤ 60 s for the scanner. **Never** run these on production. And **never** copy the
harness's `UPDATE lease_requests SET billing_mode='rolling'` shortcut (`:368`) — a lease's
mode is set only by the driver's request.

---

## 4. Happy path — the script

Legend: **D** driver phone, **O** owner phone, **A** admin (curl / console), **S** scanner (automatic).
Minimum build for the tapper is given; "33" means the App Store build suffices.
Verification SQL is against the **local** DB. "Silent?" says whether a failure here would be invisible.

| # | Who / build | Screen → exact tap | Calls | You should see | Verify (local SQL) → expect | What can go wrong |
|---|---|---|---|---|---|---|
| 1 | D, 33 | Login → **"Use password instead"** (if the OTP screen shows) → email + password | `POST /auth/login` | Discover tab | `select role from users where email='repro_driver@…'` → `driver` | Visible: wrong password. **Silent: none.** |
| 2 | D, 35+ | Discover → tap the CR-V (opens listing detail) | `GET /listings/{id}` then `GET /config` (in `.task`, `DiscoverView.swift:958-963`) | Detail page; after ≤ 3 s either the weekly button or the retry row | server log: `GET /api/v1/config status=200` | **Silent on 35–39:** `try?` → button just absent, no message. **Visible on 40:** "Couldn't check weekly rentals — tap to retry". On any build, if the driver is not allowlisted the button is absent with no message (F2). |
| 3 | D, 35+ | Listing detail → **"Rent weekly instead"** (`:1398`, subtitle "Renews every 7 days until you return the car") — **not** "Request lease" (`:1356`) | — | The "What happens next?" explainer → **"Send request"** | — | **Silent:** tapping "Request lease" produces a fixed-term request with no warning (F1/F2 shape). |
| 4 | D | Explainer → **"Send request"** | `POST /listings/{id}/lease-requests {"weeks":1,"billing_mode":"rolling"}` | Chat opens; card shows **"Renews weekly until returned"** | `select status,billing_mode from lease_requests where driver_id='<d>' order by created_at desc limit 1` → `requested \| rolling` **[ran]** | Visible: 409 `DRIVER_LICENSE_INVALID`, `OUTSTANDING_BALANCE`, `DUPLICATE_LEASE_REQUEST` (one active request per listing **[ran]**). **Silent:** if the body lacked `billing_mode` you get `requested \| fixed_term` and nothing else **[ran]**. |
| 5 | O, 38+ | Chat/Today → request card → **"Accept"** (`:683`) | `POST /lease-requests/{id}/accept` | First time on a rolling request: **owner terms sheet** (build 38+) | on refusal: response `409 OWNER_TERMS_REQUIRED` **[ran]** | Visible on 38+: the sheet. **Dead end on 33–37:** generic error, `details` discarded (`User.swift:527-530`) — admin must record via `POST /admin/users/{id}/owner-terms` with a note. |
| 6 | O, 38+ | Owner terms sheet → read → accept | `GET /me/owner-terms` then `POST /me/owner-terms/accept {"terms_version":"owner-rolling-v2 (2026-09-10)"}` | Sheet dismisses | `select terms_version,channel from owner_terms_acceptances where owner_id='<o>'` → `owner-rolling-v2 (2026-09-10) \| app` **[ran]** | Visible: 409 `OWNER_TERMS_VERSION_MISMATCH` if the client showed stale text. |
| 7 | O | Request card → **"Accept"** again | `POST /lease-requests/{id}/accept` | Card: accepted, waiting for payment | `… → accepted \| rolling` **[ran]**; `select count(*) from owner_payouts where lease_request_id='<l>'` → 0 (nothing accrues yet) | Visible: availability conflicts. Connect onboarding does **not** gate here (`payouts.go`). |
| 8 | D, 35+ | Card → **"Review weekly terms & pay"** (`:818`) | `POST /lease-requests/{id}/payments/intent` | **RollingConsentSheet** with the server's disclosure text and "Agreement version: rolling-billing-v3…" | `select terms_version, activated_at from lease_billing_consents where lease_request_id='<l>'` → row exists, `activated_at` NULL | Visible: 503 `ROLLING_DISABLED` if the flag is off. **Not run by me** (writes a consent row). |
| 9 | D | Sheet → **"Pay <amount> and start rental"** (`:170-171`) → Stripe PaymentSheet → 4242… → Pay | Stripe confirm; then webhook `payment_intent.succeeded` → `/stripe/webhook` | Card: paid; pickup deadline countdown | `select status,pickup_deadline_at from lease_requests …` → `paid`, deadline set; consent `activated_at` NOT NULL, `card_fingerprint` set | **Silent if `stripe listen` is not running:** the app shows paid (Stripe said so) but the DB never does; nothing arms. Watch the server log for `payment succeeded`. If the event lacks `payment_method` the handler logs `refusing until redelivery` (`:1373`). |
| 10 | O, 33 | Today → Key handover card → **"I handed over the keys"** | `POST /key-handovers/{id}/owner-confirm` | Card flips to waiting for driver | `select status from key_handovers where lease_request_id='<l>'` → `owner_confirmed` | Visible only. |
| 11 | D, 33 | Today → **"I received the keys"** | `POST /key-handovers/{id}/driver-confirm` | — | → `completed` (`models`: `pending → owner_confirmed → completed`) | Visible only. |
| 12 | D, 33 | Chat card → **"I've picked up the car"** (`:729`) | `POST /lease-requests/{id}/pickup-confirm` | Card: "Pickup Confirmed" (`:381`) | `pickup_confirmed_at` NOT NULL; car `reserved_by_lease_request_id='<l>'` | Visible: deadline lapsed → auto-refund path. |
| 13 | S | ≤ 60 s later | `runBillingSweep` → bootstrap phase mints cycle 1 | Driver: `RollingBillingCard` shows next charge date | `select cycle_number,status,period_end from billing_cycles where lease_request_id='<l>'` → `1 \| paid \| pickup+7d`; `owner_payouts` → one `accruing` row | **Silent:** engine phases log at INFO; watch `billing bootstrap`. Kill switch off ⇒ no mint, no message (`billing_engine.go:63-71`). |
| 14 | A | Console → Rents → open the rent → **"Weekly billing"** section | `GET /admin/rents/{id}/billing-cycles` | Cycle table (`Rents.vue:565-589`) | — | If the section is hidden the rent is `fixed_term` (`Rents.vue:50, 72`). |
| 15 | you | Age the rows by **5 days** (§3.9), wait ≤ 60 s | notice phase | Driver push/in-app **"Your rental renews soon"** | `renewal_notified_for = rental_ends_at` | **Silent by design:** notice failure does not stop the charge (§1g). |
| 16 | you | Age by **2 more days** (week boundary) + advance the Stripe test clock; wait | mint phase → off-session charge | Driver: week 2 charged | `billing_cycles` → `2 \| paid`, `stripe_payment_intent_id` set; `owner_payouts` → week 1 row now `pending` (promoted because `period_end <= now`, `payout_repository.go:571`) | Visible: decline ladder (F5). **Silent:** if `stripe listen` dropped, cycle 2 sits `charging`/`retrying`. |
| 17 | A | Console → Payouts | `GET /admin/payouts…` | Week-1 payout `pending` → `paid` once the owner is Connect-onboarded, else `awaiting_onboarding` | `select period_end,status from owner_payouts where lease_request_id='<l>' order by period_end` | Not-onboarded owner: row parks — **not** a dead end; pays on onboarding. |
| 18 | D, 33 | Chat card → **"Request to Return the Vehicle"** (`:359`) mid-cycle 3 | `POST /lease-requests/{id}/vehicle-return` | Card: return requested | `select status from vehicle_returns where lease_request_id='<l>'` → `driver_initiated`; `renewal_halted_reason='return_initiated'` | Visible only. |
| 19 | O, 33 | Today → **"Confirm return"** | `POST /vehicle-returns/{id}/owner-confirm` | Return confirmed | `vehicle_returned_at` NOT NULL; `vehicle_returns.status` → `owner_confirmed`, then `completed` once settlement runs (`models`: `driver_initiated → owner_confirmed → completed`) | Visible: dispute path (`disputed`). |
| 20 | S | ≤ 60 s | post-return refund phase | Driver: prorated refund of the unused days of cycle 3 | `billing_cycles` cycle 3 → `partially_refunded`, `refunded_cents` = per-day × unused days (`vehicle_return.go:212-213`); Stripe refund `cycle-refund-<id>` | **Silent:** refund failure parks `refund_status` — check `support_tickets`. |
| 21 | S | after cycle 3's `period_end` | promote phase | Owner: final payout | `owner_payouts` cycle-3 row `pending` with the **used**-days share | — |

---

## 5. Failure reproductions

### F1 — driver on an OLD build → request becomes fixed-term

*Setup:* check out `0f078c7` (build 33) or `915dbbd` (build 35) in a second worktree, set
`AppConfig.current = .local`, run on a second simulator as the same allowlisted driver.

*Taps:* open the CR-V detail → build 33 shows only **"Request lease"** (no weekly button exists in
that binary — `strings` on the archive **[ran]**) → tap it → "Send request".

*Expected:* `201`, `requested | fixed_term`. Chat says "New lease request: 1 week(s) at …".
Server log: only the middleware line. Nothing distinguishes this from a deliberate fixed-term
choice. **Reproduced by API with the field omitted [ran]** — the wire shape build 33 sends.

*Evidence:* `lease_requests.billing_mode='fixed_term'`; grep the log for `rolling` → nothing.

### F2 — driver NOT on the allowlist → CTA hidden → fixed-term

*Setup:* backend with `ROLLING_ALLOWLIST_USER_IDS=<someone else>`; driver on build 40.

*Taps:* open a listing → `GET /config` returns `{"rolling_rentals_enabled":false}` **[ran]** → the
weekly button is simply absent (`weeklyAvailability = .unavailable`, `DiscoverView.swift:1070`)
→ only "Request lease" → fixed-term.

*Expected:* same silence as F1. Even if the driver hand-crafted `billing_mode=rolling`, the server
answers `503 ROLLING_DISABLED "Weekly rentals aren't available right now"`
(`lease_request.go:332-336`) — the one **visible** refusal in this family.

### F3 — config call fails at launch, then the backend recovers

*Setup:* driver on build 40 signed in; stop the backend; open a listing detail; start the backend.

*Expected (build 40):* three attempts over ~2.8 s, then the grey row **"Couldn't check weekly
rentals — tap to retry"** (`:1386`). The CTA stays hidden **until** the driver taps that row
(`:1376`) or leaves and re-enters the detail (the `.task` re-runs, `:958`). Nothing refetches on
foreground or login.

*Expected (builds 35–39):* one attempt, `try?`, no row — the button is absent for the life of that
detail view and reappears only on re-entry. **This is the silent case.**

*Evidence:* server log shows no `GET /api/v1/config` during the outage; then one 200 on retry.

### F4 — owner never accepted owner terms / not Connect-onboarded → Accept

*Reproduced [ran]:* fresh owner, rolling request → **Accept** → `409 OWNER_TERMS_REQUIRED` carrying
`terms_text` + `terms_version` → accept terms → Accept → `accepted | rolling`.

*Dead end?* On **build 38+**: no — the sheet renders from `details`. On **33–37**: yes for the owner
alone — the app discards `details`, shows a generic error, and the only exit is support recording
acceptance through `POST /admin/users/{id}/owner-terms` (note required, `main.go:691`). In
production this is reachable only by an allowlisted driver's rolling request landing on an owner
below 38 — i.e. George on 39 is fine; a future pilot owner on the App Store build is not.

*Connect:* not a gate on Accept **[ran]**. The owner's money parks `awaiting_onboarding`
(`payouts.go:455-517`) and releases on onboarding — slow, not stranded.

### F5 — declined weekly charge (week 2)

*Setup:* pay week 1 with **4000 0000 0000 0341** (attaches, fails off-session). Age to the week
boundary; advance the Stripe clock.

*Expected ladder (`billing_engine.go`, `models/billing.go:47-49`):* attempt 1 fails →
`retrying`, `next_attempt_at = +24h`; attempt 2 → still `retrying`; attempt 3 → `delinquent_since`
set, `renewal_halted_reason='delinquent'` (`:454-455`); attempt 4 → `failed_final`. Driver gets a
payment notification each time and the `RollingBillingCard` offers **"Pay now"** (rescues the same
intent on-session). The lease stays `paid` — the driver keeps the car; renewals are halted, so no
further mints (`lease_request_repository.go:2148-2149`).

*Debt:* opens only at **return** (`SettleArrearsProRata` → `openDriverDebt`,
`billing_engine.go:552-553`), for the used days of the unpaid week; then the
outstanding-balance gate applies to new bookings and the arrears **Pay now** clears it.

*Owner guarantee:* `OWNER_GUARANTEE_ENABLED` defaults **false** (`config.go:198`) — dark; the
owner's week-2 payout simply never promotes.

*Evidence:* `billing_cycles` row 2 `attempt_count`, `last_decline_code`, `status`;
`lease_requests.delinquent_since`; `support_tickets` (arrears ticket at return).

### F6 — pre-charge notice send fails

The notice is push + in-app via `notifHandler.Notify` (`billing_engine.go:147`); with APNs
unconfigured locally it is in-app only and the log says `push: APNs not configured` at boot.
The mint predicate has no notice clause (`lease_request_repository.go:2142-2155`), so **the charge
fires on schedule regardless**. Reproduce by killing APNs config (already the local default) and
aging to the boundary: cycle 2 charges; `renewal_notified_for` is still stamped by the notice
phase because the in-app write succeeds.

---

## 6. Sep 14 forensics (production, read-only)

### What the rows say

Fenix (`bbe2577f`) created two requests on the CR-V, five minutes apart, both `fixed_term`:

| id | created (UTC) | status | offered price | notes |
|---|---|---|---|---|
| `ab50be0a` | 01:37:29 | cancelled | $100.03 → $70 | accepted a price change 01:38:18, declined a second 01:42:00 |
| `f0eeeefd` | 01:42:22 | paid → returned 09-16 | $100.03 → **$50** | accepted 01:42:44, pickup 01:44:00, refunded 09-16 |

`lease_billing_consents` and `billing_cycles` for Fenix: **0 rows**. His chat lines are all
system-generated ("New lease request…", "Driver accepted the new price…"). He has **no**
`support_tickets` rows at any date. **[prod-ro]**

### Is the client version recorded anywhere?

**No.** The only column in the schema that could carry it is `login_otps.user_agent`
(format `DriveBai/<build> CFNetwork/… Darwin/…`, seen for George in June). Fenix has **zero**
OTP rows — he uses password login, which stores nothing. The request-log middleware does not
log User-Agent (`logging.go:54-60`). `device_tokens` has `platform` and `sandbox` only. **This is
itself a finding:** the one header that names the build is discarded on every request.

### Was Fenix eligible server-side?

Yes. On Sep 14 the running binary was **v101** (`ee12d49`, deployed Sep 11 09:27Z, replaced
Sep 16 ~10:30Z). Its `/config` returned the global `ROLLING_RENTALS_ENABLED=true` to every
signed-in user (`git show ee12d49:backend/cmd/api/main.go` 398-404) and it contained no allowlist
code at all (0 `RollingAllowlist` symbols). `ROLLING_RENTALS_ENABLED` has been `true` since v99
(commit `49ad73c`, Sep 11 09:04Z). The allowlist secret was created on Sep 16 and is read only by
v102+. So the server would have said **yes** — the refusal could not have come from the backend.

### Do the logs still cover Sep 14?

**No.** `fly logs` retention now starts at **2026-09-16T10:32Z**; zero lines from Sep 14 remain.
Even if they did, they would show `GET /api/v1/config` hits without a User-Agent.

### Conclusion

**Most likely cause (moderate confidence, ~70%):** Fenix's app did not send `billing_mode`
because the weekly button was never drawn on his phone. Two sub-causes fit every fact and cannot
be separated with the data that exists:

1. **He was on a build below 35** (App Store 33 or older). No `billing_mode` field exists in that
   binary. Supporting: a brand-new APNs device token registered at **2026-09-14 23:49:47Z**, 22 h
   after the lease, which is what a fresh install/update produces; his previous token dates from
   Aug 29, before builds 35+ existed (Sep 10–11).
2. **He was on 35–39 and the single `try?` config call failed** (network blip, expired token at
   that instant), hiding the button for that session.

A third possibility — he saw "Rent weekly instead" and tapped "Request lease" — is consistent with
the rows but not with him going to support afterwards; **~15%**.

**What would settle it:** App Store Connect → TestFlight → Testers → Fenix → installed builds and
dates; or Fenix reading *Settings → DriveBai* (or the TestFlight app) for the build number. From
our side, nothing recorded on Sep 14 can.

---

## 7. What I could not verify, and why

- The full payment → active → cycle → return chain **by running**: it requires creating a
  rolling payment intent, which writes a consent row (`lease_request.go:914`). Forbidden here.
  The chain is covered by the rehearsal suite (24 lines, Stripe test mode, green 2026-09-16).
- `docs/DRIVEBAI_PROJECT_CONTEXT.md` — not present in the repository.
- Fenix's actual build — not recorded anywhere in production (see §6).
- Stripe test card numbers in §3.8 — Stripe documentation values, not exercised in this run.
- The sale rows of `BUILD_OWNERSHIP_MAP.md` — out of scope for this run.
