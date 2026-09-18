# Test plan: recurring-only, build 41

Written 2026-09-18 against `main` at `1e6ad03` (backend **v105** live) plus the pending fix
on branch `v106-rent-refusal-notice`. Replaces the happy path in
`docs/REPRO_ROLLING_LEASE_DRIVER_FLOW.md`; that document's §1 flow map and §6 forensics
stay valid and are cited rather than repeated. Design: `docs/DESIGN_RECURRING_BILLING.md` §11.

**Evidence tags.** **[ran]** executed and observed on 2026-09-18. **[code]** read at the
cited `file:line`. **[prod-ro]** read-only against production. **[rehearsal]** covered by a
Stripe test-clock line in `rolling_rehearsal_lines_test.go` / `rolling_rehearsal_monthly_test.go`.

**Rule kept throughout:** no consent row is ever created to prove a step. Where a step
needs one it is marked **[code]** with the rehearsal line that covers it.

---

## 0. The three production requests from the reviewer account

All **[prod-ro]**, nothing modified.

| id | created (UTC) | car / owner | status | billing_mode | User-Agent on the creating call |
|---|---|---|---|---|---|
| `60a61dba` | 2026-09-17 13:57 | Mustang / gilash@gmail.com | **cancelled** | `fixed_term` | not retained (log window starts 2026-09-18 05:05Z) |
| `2494708d` | 2026-09-18 05:05:53 | Transit / ggao290@gmail.com | **cancelled** | `fixed_term` | `DriveBai/41 CFNetwork/3860.600.12 Darwin/25.6.0` |
| `7ace28e3` | 2026-09-18 05:06:01 | Mustang / gilash@gmail.com | **requested — still live**, expires 2026-09-19 05:06 | `fixed_term` | `DriveBai/41 …` |

**Her phone is on build 41, not 40.** The middleware logs `user_agent` as of v105, and the
`POST /listings/…/lease-requests` that created `7ace28e3` carries `DriveBai/41`. The
"Checking weekly rentals…" loader she saw is a **build 40** string (`DiscoverView.swift:1384`
before this batch); build 41 removed it. If that loader appeared, it was an older install
than the one now talking to production.

**Is `d2c72e29` (tzhe@yandex.ru) in the allowlist? No.** `GET /api/v1/config` as that
account returns `{"rolling_rentals_enabled":false}` **[ran]**, and `/config` is answered by
`RollingOpenFor`, the identical predicate that decides creation (`main.go:445-455`,
`lease_request.go:3112-3121`) **[code]**. `fly secrets list` shows `ROLLING_ALLOWLIST_USER_IDS`
present with digest `3150b22…` (names and digests only; no values printed); `GET /admin/config`
reports `rolling_allowlist_size: 4, rolling_allowlist_broken: false` **[ran]**. The boot line
itself has rotated out of the ~20 h log window, so the size comes from the admin endpoint.

**Does `fixed_term` here prove the configuration is correct?** It proves the **non-eligible
path is intact** and nothing more. It cannot prove the recurring path works, because the
recurring branch was never entered: `RollingOpenFor(d2c72e29)` is false, so creation took the
`default` arm (`lease_request.go:426-434`) exactly as it did before v105. A green result on a
non-eligible account is a regression check, not a pilot test.

### Cancelling `7ace28e3` — Aziza's taps

The **driver alone** cancels; no owner action is required. `CancelLeaseRequest` moves
`requested` or `payment_pending` → `cancelled` and releases any car reservation in the same
call (`lease_request_repository.go:477-486`) **[code]**.

1. Sign in as **tzhe@yandex.ru** (the driver).
2. **Chats** → the conversation with *gilash* about the **2017 Ford Mustang**.
3. On the lease-request card tap **"Cancel Request"** (`LeaseRequestCardView.swift:847`) **[code]**.

`60a61dba` and `2494708d` are already `cancelled` — the log shows a `POST …/cancel → 200`
from build 41 for the latter, so she has done this before. Nothing further is needed for them.

### Residue on the reviewer account

Cancelling leaves **nothing that blocks the App Store demo flow**:

- The Today feed lists only `status='requested' AND expires_at > NOW()`
  (`lease_request_repository.go:1002-1003`) **[code]**, so a cancelled row disappears.
- The uniqueness index is partial — `idx_lease_requests_active_per_driver_listing` covers only
  live statuses **[prod-ro]** — so a cancelled row never blocks a fresh request on the same car.
- A `requested` lease reserves no car; reservation happens at payment
  (`lease_request_repository.go:415-424`) **[code]**. Both test cars read
  `status=available, reserved_by=none` right now **[prod-ro]**.
- The demo car, **2003 Honda Accord** (`9fb68ef4`, zholdas, $20/wk), is `available`,
  approved, unpaused, unreserved, and its old lease `52ad4b42` has a **completed** return **[prod-ro]**.
- The chat thread survives with its system messages. That is cosmetic, and it is the same
  residue every previous cancelled demo request left.

**One thing a reviewer would see, unrelated to this test:** the reviewer account still holds
two July rentals that never completed a return — `5dfcd7d9` and `aaff0301`, holding
*2026 Test Life Activity* and *2026 Test Car sell*, both cars `status=rented` **[prod-ro]**.
They do not touch the Accord and do not block the demo, but they will appear as active
rentals. They are the same stale class the owner-release endpoint exists for.

---

## 1. Open questions settled

### 1a. A monthly listing while `MONTHLY_RENTALS_ENABLED` is off

Production has exactly one monthly listing — *1997 Mercury Grand Marquis*, zholdas,
$350/month, `status=pending` so not discoverable **[prod-ro]**.

| Caller | Before the fix | After `v106-rent-refusal-notice` |
|---|---|---|
| Allowlisted driver, build 41 | Ordinary rent CTA → tap → `409 INTERVAL_NOT_SUPPORTED` | Listing shows a notice, no button: *"This car is priced per month. Monthly rentals aren't available yet — try a car priced per week."* |
| Allowlisted driver, build 33 | Ordinary CTA → tap → same 409, message shown verbatim | unchanged (33 cannot decode the new field) — still a message and an exit |
| Non-allowlisted driver, any build | Ordinary CTA → **fixed-term lease created normally** | unchanged |

**This was a defect of the same shape as the build-33 case** — not a dead end (the 409 carried
a message and "try a car priced per week" is an exit), but a button whose only outcome is a
refusal, which this codebase suppresses everywhere else (`rentUnavailableNotice` already covers
sold, reserved, paused and sale-in-progress, `DiscoverView.swift:1284-1298`). Fixed in §3.
The same notice now covers **daily** listings for eligible drivers (production has none).

The refusal order in `CreateLeaseRequest` is: owner-not-in-pilot fallback → interval refusal →
old-build refusal **[code]**. So an allowlisted build-33 driver on a monthly car gets
`INTERVAL_NOT_SUPPORTED`, not `APP_UPDATE_REQUIRED`.

### 1b. What build 33 actually displays for `409 APP_UPDATE_REQUIRED`

**The server's message, verbatim — not a generic string.** In build 33
(`git show 0f078c7`): the catch assigns `leaseRequestError = apiError.errorDescription`
(`DiscoverView.swift:1086-1087`), `errorDescription` for `.serverError` returns the message
unchanged (`APIClient.swift:82-83`), and it renders in an alert titled **"Lease Request Failed"**
(`DiscoverView.swift:988-994`) **[code]**. So Fenix on build 33 would read exactly:

> Your version of DriveBai can't show the rental terms this car needs, so this request wasn't
> sent and nothing was charged. Ask us for the TestFlight build, or watch for the next App
> Store update.

### 1c. The stray request `60a61dba`

Already `cancelled` **[prod-ro]** — no action needed. Details and the cancel taps are in §0
above; the same three taps apply to the still-live `7ace28e3`.

### 1d. Pilot accounts to add

Current allowlist: **4 ids** — George `0cb935b9`, Fenix `bbe2577f`, Aziza-admin `b0469bf3`,
Knightbridge `eb801d82`.

To test recurring end to end on **her own Mustang**, exactly **one id must be added**:

| Add | Who | Why |
|---|---|---|
| `36c0bf8e-5a8e-4a71-b072-51020fd2ce6b` | `gilash@gmail.com`, owner of the 2017 Ford Mustang | Under a **named** pilot both parties must be listed, or the request falls back to fixed-term with a WARN (`lease_request.go:398-402`) **[code]** |

Her **driver** side needs no change: `aziza.gilash.atyrau@gmail.com` (`b0469bf3`) is already
listed and its driver's licence is on file with status `uploaded`, which passes the booking gate
(only `rejected` blocks — `document_repository.go:182-184`) **[code]**, **[prod-ro]**.

**Do not add `tzhe@yandex.ru` (`d2c72e29`).** It is the App Store reviewer demo credential and
build 33 is in review; an allowlisted reviewer on build 33 would be refused at request time.

The invocation Aziza runs herself — the value is the **existing four ids plus the new one**,
comma-separated, which is why it is set wholesale:

```
fly secrets set ROLLING_ALLOWLIST_USER_IDS="<the four current ids>,36c0bf8e-5a8e-4a71-b072-51020fd2ce6b" -a drivebai-api-team
```

Setting a secret **restarts the app** (it triggers a release). The boot log must then read:

```
"weekly rentals: ON"  allowlisted_drivers=5  rejected_entries=null  open_to_everyone=false
```

If `allowlisted_drivers` is not 5, or `rejected_entries` is non-empty, a paste error dropped an
id — and a set-but-unparseable list fails **closed** to a pilot of nobody
(`config.go:115-120`, boot `Error` line at `main.go:313`) **[code]**. Confirm with
`GET /api/v1/admin/config` → `rolling_allowlist_size: 5` **[ran]** (this endpoint is new in v105).

---

## 2. Local environment for build 41

This supersedes §3 of the repro doc. What changed: **migration 65**, three new switches, and
the removed eligibility loader.

### 2.1 Database

```
createdb b41test
cd backend && migrate -path migrations -database "postgres://$(whoami)@localhost:5432/b41test?sslmode=disable" up
```
Last line must read `65/u consent_interval_monthly`. **[ran]** on a scratch DB.

### 2.2 Backend, port 8080

```
cd backend && go build -o /tmp/drivebai-api ./cmd/api
export DATABASE_URL="postgres://$(whoami)@localhost:5432/b41test?sslmode=disable"
export STRIPE_SECRET_KEY=<sk_test_… from MEETING_BRIEF.md>     # TEST key only
export STRIPE_PUBLISHABLE_KEY=<pk_test_…>
export STRIPE_WEBHOOK_SECRET=<whsec_… from `stripe listen`>     # step 2.4
export JWT_SECRET=local-only PORT=8080 ENV=development UPLOAD_DIR=/tmp/drivebai-uploads
export AUTO_APPROVE_CARS=true DEBT_ENFORCEMENT_ENABLED=true
export ROLLING_RENTALS_ENABLED=true
export ROLLING_ALLOWLIST_USER_IDS=""      # empty first boot; set to the local ids in 2.3
export RECURRING_ONLY=false
export MONTHLY_RENTALS_ENABLED=true       # local ONLY, so T-B is testable
/tmp/drivebai-api
```

Boot lines that prove each switch — all four must appear **[ran]**:

```
weekly rentals: ON  allowlisted_drivers=N  rejected_entries=[]  open_to_everyone=<bool>
recurring only: OFF — fixed-term still created for non-eligible drivers
monthly rentals: ON — monthly listings produce 28-day recurring leases
stripe config  secret_key_set=true  publishable_key_set=true  webhook_secret_set=true
```

If the binary exits at boot with `SCHEMA BEHIND BINARY`, migrations were not applied — the line
names the migrate command.

`RECURRING_ONLY=true` is **forced off** unless rolling is open to everyone (empty, well-formed
allowlist); the boot logs `recurring only: REQUESTED BUT FORCED OFF …` at Error **[code]**.
To exercise T-C's last row, set `ROLLING_ALLOWLIST_USER_IDS=""` **and** `RECURRING_ONLY=true`.

### 2.3 Accounts, licence, listings

Create via the API, never SQL, except the one admin bootstrap (there is no API for the first
admin). Full commands in the repro doc §3.3–3.6; unchanged except as noted.

- driver, owner, admin → `POST /auth/register`; promote the admin with one SQL `UPDATE`.
- driver licence → `POST /documents/drivers_license` (multipart field **`file`**), then
  `PATCH /admin/users/{id}/documents/{docId}/status {"status":"verified"}` — valid values are
  `verified` / `rejected`; `approved` returns 400 **[ran]**.
- **weekly** listing → `POST /cars/` with `rent_price_period: "weekly"`, `weekly_rent_price: 150`.
- **monthly** listing → same, `rent_price_period: "monthly"`, `rent_price_amount: 600`,
  `weekly_rent_price: 150` (the derived weekly; one monthly cycle is then exactly $600).
- Pitfalls that cost time **[ran]**: VIN must be 17 characters; `fuel_type` is
  `gas|diesel|electric|hybrid|plug_in_hybrid` (`gasoline` → 500); minimum weekly price 50;
  a car is created `pending` and needs **three** documents (registration, inspection,
  insurance, multipart field `document_type`) then `PATCH /admin/cars/{id}/approve` before it
  is discoverable — `AUTO_APPROVE_CARS` sets `is_approved` only, and Discover filters on
  `status='available'`.
- **Restart the backend with `ROLLING_ALLOWLIST_USER_IDS=<driver id>,<owner id>`.** Both, or the
  owner-fallback rule turns every request fixed-term. Confirm `allowlisted_drivers=2`.

### 2.4 Stripe webhooks

```
stripe listen --forward-to localhost:8080/api/v1/stripe/webhook
```
Copy the `whsec_…` into `STRIPE_WEBHOOK_SECRET` and restart. Without it a lease never reaches
`paid` and the consent never activates (`lease_request.go:1428`) **[code]**. This is the silent
failure in T-C6.

### 2.5 iOS build 41 against local

`AppConfig.swift:11` → `static var current: BackendEnvironment = .local`. No runtime toggle
exists; `Info.plist` already exempts localhost from ATS. Build the `DriveBai` scheme (Debug).
Two simulators, one signed in as driver, one as owner.

### 2.6 Test cards and time

| Purpose | Card |
|---|---|
| Success, saves for off-session | 4242 4242 4242 4242 |
| Attaches, then fails off-session (T-C4) | 4000 0000 0000 0341 |
| Immediate decline | 4000 0000 0000 0002 |
| 3DS / needs-action | 4000 0025 0000 3155 |

Time: the **backend** clock is the database. Age rows exactly as the rehearsal does
(`rolling_rehearsal_test.go:403-426`) — shift `lease_requests.rental_ends_at`,
`billing_cycles.period_*` and `owner_payouts.period_*` by the same interval — and advance a
Stripe **test clock** in step for the card side. Never run these against production, and never
copy the harness's `UPDATE lease_requests SET billing_mode=…` shortcut: a lease's mode and
interval are set once, at creation.

---

## 3. Fixed in this batch

Branch **`v106-rent-refusal-notice`**, commit `c1e3c07`, **not deployed**.

One defect, from §1a: an eligible driver saw an ordinary rent CTA on a listing they could not
rent at all. The server now answers it per viewer — `RentRefusalFor(viewer, owner, period)`
returns the reason or `""` — and it rides on listing items and car detail as
`rent_unavailable_reason`. The `409 INTERVAL_NOT_SUPPORTED` reuses the same function, so the
notice on the listing and the refusal on tap are the same sentence. Build 41 renders it through
the existing `rentUnavailableNotice`. `TestRentRefusalMatchesTheRefusalCopy` asserts the two
agree across period × monthly flag × eligibility: where the listing shows a notice the request
must 409 with identical copy, and where it does not the request must succeed. A second test
asserts the field reaches the listing **JSON**, so deleting the `main.go` wiring cannot ship
green. Suite **438 pass, 0 fail**; all five fixed-term proofs still green **[ran]**.

Two-lens review (money & consent; old-client compatibility) returned **safe to merge** with
eight low/medium findings, six closed in the same commit: the notice now models the
`RECURRING_ONLY` refusal and shares one string with its 409; the 409 can no longer be empty if
the two predicates drift; `RedactForPublic` zeroes the field so the redactor is the enforcement
point; `GetCar` no longer puts driver-facing copy in the owner's own payload; the Swift case
order was swapped so the sale-in-progress notice wins, matching the server's own refusal order
(occupancy before interval); and the guest→sign-in replay re-checks the reason instead of
spending a 409. Left open with reasons: the notice does not model `APP_UPDATE_REQUIRED` or
`ROLLING_DISABLED`, neither of which can bite a client new enough to render a notice.

No fix was needed for §1b — build 33 already renders the server's message verbatim.

---

## 4. The test plan

**Who/build:** D = driver phone, O = owner phone, A = admin (curl or console), S = scanner
(automatic, 60 s tick). Verification SQL is against the **local** DB.

### T-A. Weekly recurring, happy path, build 41 both sides

The driver first meets the word **"weekly"** at step 3 — the CTA itself. The owner's push at
step 5 says **"<driver> wants to rent <car> — renews weekly until returned"**
(`lease_request.go:418-421`) **[code]**; the chat system line says **"New rental request: USD
150.00 per week — renews weekly until the car is returned"**
(`lease_request_repository.go:102, 2452`) **[code]**.

| # | Who/build | Screen → exact label (file:line) | Call | You should see | Verify → expect | What can go wrong |
|---|---|---|---|---|---|---|
| 1 | D 41 | Login → **"Use password instead"** (`OTPLoginView.swift:139`) | `POST /auth/login` | Discover | `select role from users where email=…` → `driver` | Visible only. OTP codes print to the server log locally. |
| 2 | D 41 | Discover → tap the weekly car | `GET /cars/{id}` | Detail page, no loader | server log: no `GET /config` on this screen | **Changed from build 40:** there is no eligibility fetch here any more. |
| 3 | D 41 | Detail → **"Rent — renews weekly"** with **"Charged every week until you return the car"** (`DiscoverView.swift:1313-1318`) | — | The explainer card | — | **Silent:** if the CTA reads plain "Request lease", the server said this pair is not eligible — check `allowlisted_drivers` and that BOTH ids are listed. |
| 4 | D | Explainer → **"Send request"** | `POST /listings/{id}/lease-requests` (no `billing_mode`) | Chat opens; card badge **"Renews weekly until returned"** | `select status,billing_mode,billing_interval from lease_requests order by created_at desc limit 1` → `requested \| rolling \| weekly` **[ran]** | Visible: 409 `DRIVER_LICENSE_INVALID`, `OUTSTANDING_BALANCE`, `DUPLICATE_LEASE_REQUEST`, `APP_UPDATE_REQUIRED`. **Silent:** none — a downgrade to fixed-term now WARN-logs with the build. |
| 5 | O 41 | Chats/Today → **"Accept"** (`LeaseRequestCardView.swift:683`) | `POST /lease-requests/{id}/accept` | First time on a recurring request: the owner-terms sheet | on refusal: `409 OWNER_TERMS_REQUIRED` **[ran]** | **Dead end on builds 33–37** (sheet shipped in 38): generic error, no way through except the admin record endpoint. Pilot owners must be on 38+. |
| 6 | O 41 | Terms sheet → read → accept | `GET /me/owner-terms`, `POST /me/owner-terms/accept` | Sheet dismisses | `select terms_version,channel from owner_terms_acceptances` → `owner-rolling-v2 (2026-09-10) \| app` **[ran]** | Visible: 409 on a stale version. |
| 7 | O | **"Accept"** again | same | Card: accepted | lease → `accepted \| rolling` **[ran]**; `owner_payouts` → 0 rows yet | Connect onboarding does **not** gate here **[ran]**. |
| 8 | D 41 | Card → **"Review weekly terms & pay"** (`LeaseRequestCardView.swift:818`) | `POST /lease-requests/{id}/payments/intent` | **Consent sheet**, server's text, "Agreement version: rolling-billing-v3 …", title **"Weekly rental"** | consent row exists, `activated_at` NULL | Visible: 503 `ROLLING_DISABLED`; 409 `APP_UPDATE_REQUIRED` if the client cannot prove build ≥ 35. **[code]** — writes a consent row; covered by rehearsal Line 1. |
| 9 | D | Sheet → **"Pay $150.00 and start rental"** (`RollingConsentSheet.swift:170`) → Stripe sheet → 4242… | Stripe confirm → webhook | Card: paid | lease `paid`; consent `activated_at` NOT NULL, fingerprint set | **Silent if `stripe listen` is not running** — see T-C6. **[code]** / rehearsal Line 1. |
| 10 | O 41 | Today → **"I handed over the keys"** (`KeyHandoverModels.swift:83`) | `POST /key-handovers/{id}/owner-confirm` | Waiting for driver | `key_handovers` → `owner_confirmed` | Visible only. |
| 11 | D 41 | Today → **"I received the keys"** (`:85`) | `…/driver-confirm` | — | → `completed` | Visible only. |
| 12 | D 41 | Chat card → **"I've picked up the car"** (`LeaseRequestCardView.swift:729`) | `POST /lease-requests/{id}/pickup-confirm` | "Pickup Confirmed" | `pickup_confirmed_at` set; car reserved; `rental_ends_at` = pickup + 7 d | Visible: deadline lapsed → auto-refund. |
| 13 | S | ≤ 60 s | bootstrap phase | Billing card shows the next charge | `billing_cycles` → `1 \| paid \| period_end = pickup+7d`; `owner_payouts` → one `accruing` | **Silent:** kill switch off ⇒ no mint, no message. **[code]** / rehearsal Line 1. |
| 14 | A | Console → Rents → the rent → **"Weekly billing"** | `GET /admin/rents/{id}/billing-cycles` | Cycle table, "Mandate … /week" | — | Section hidden ⇒ the rent is fixed-term. |
| 15 | you | Age 5 days, wait | notice phase | Push **"Your rental renews soon"** | `renewal_notified_for = rental_ends_at` | **By design:** a failed notice does not stop the charge. **[code]** |
| 16 | you | Age 2 more days + advance the test clock | mint phase | Week 2 charged | cycle 2 `paid`; week-1 payout `accruing` → `pending` | **Silent:** if the webhook forwarder dropped, cycle 2 sits `charging`. **[rehearsal]** Line 1. |
| 17 | A | Console → Payouts | — | Week 1 `pending`, or `awaiting_onboarding` | `select period_end,status from owner_payouts` | Not-onboarded owner parks, never lost. |
| 18 | D 41 | Chat card → **"Request to Return the Vehicle"** (`:359`) mid-cycle 3 | `POST /lease-requests/{id}/vehicle-return` | Return requested | `vehicle_returns` → `driver_initiated`; halt `return_initiated` | Visible only. |
| 19 | O 41 | Today → **"Confirm return"** (`VehicleReturnModels.swift:215`) | `…/owner-confirm` | Return confirmed | `vehicle_returned_at` set | Visible: dispute path. |
| 20 | S | ≤ 60 s | post-return refund | Pro-rata refund | cycle 3 `partially_refunded`; Stripe refund `cycle-refund-…` | **Silent:** a failed refund parks `refund_status` — check tickets. **[rehearsal]** Line 4. |
| 21 | S | after cycle 3's period_end | promote phase | Final payout | cycle-3 payout row `pending` | — |

### T-B. Monthly recurring (local only, `MONTHLY_RENTALS_ENABLED=true`)

Same chain against the **monthly** listing ($600/month, derived weekly 150). Differences:

- CTA reads **"Rent — renews monthly"** / "Charged every month until you return the car".
- Chat line: *"New rental request: USD 600.00 per month — renews monthly until the car is returned."*
- Consent sheet title **"Monthly rental"**, "renews every 28 days", checkbox *"I authorize these
  monthly charges."*, text `rolling-billing-monthly-v2`, amount **$600.00**.
- Cycle 1 is **28 days** from pickup; notice fires at **T−144 h**, charge at **T−72 h**.
- **Mid-cycle return on day 20 of 28** — per-day is `floor(60000/28) = 2142`; used 20 days;
  refund `8 × 2142 + (60000 mod 28) = 17160` (**$171.60**); owner keeps **$428.40**;
  exactly one Stripe refund. **[ran]** — `TestBatch5_MonthlyLine3_MidCycleReturnDay20`
  reported `used=20 refund=$171.60 kept=$428.40`.
- Owner payout promotes **once per 28-day cycle**, not weekly (`payout_repository.go:558-590`) **[code]**.

Monthly lines proven against Stripe test mode **[ran]**: recurring charge, decline ladder,
day-20 return, dispute — all four pass.

### T-C. Refusals and edges

| # | Case | Expected copy / outcome | Evidence |
|---|---|---|---|
| C1 | Allowlisted driver, build 33, pilot owner's weekly car | `409 APP_UPDATE_REQUIRED`, alert **"Lease Request Failed"** with the message quoted in §1b | **[code]** + matrix row **[ran]** |
| C2 | Monthly listing, `MONTHLY_RENTALS_ENABLED=false`, eligible driver | Build 41: notice on the listing, no button. Builds 33–40: CTA → `409 INTERVAL_NOT_SUPPORTED` with the same sentence | **[ran]** `TestRentRefusalMatchesTheRefusalCopy` |
| C3 | Non-allowlisted driver, any build, any period | Fixed-term end to end, unchanged — the regression check | **[ran]** matrix + fixed-term fingerprint identical pre/post |
| C4 | Declined card on cycle 2 (4000 0000 0000 0341) | Ladder 4 × 24 h → `retrying` ×2 → delinquent at attempt 3 → `failed_final`; lease stays `paid` (driver keeps the car); renewals halt; **exactly one** Stripe intent for the failing cycle; debt opens at **return**, not now | **[ran]** monthly Line 2; **[rehearsal]** weekly Line 2 |
| C5 | Owner not Connect-onboarded | Accept succeeds; payout row parks `awaiting_onboarding` and releases on onboarding — money never lost | **[ran]** |
| C6 | `stripe listen` stopped during pay | **The silent one.** The app shows success (Stripe said so) but the lease never leaves `accepted` and no consent activates. Watch for `payment succeeded` in the server log; if the event lacks `payment_method` the handler logs *"refusing until redelivery"* | **[code]** `lease_request.go:1371-1379` |
| C7 | `RECURRING_ONLY=true` locally, empty allowlist | Every non-eligible driver refused `409 RENTALS_PAUSED` — a message that never says "update". Existing fixed-term leases still pay, return, refund and pay out to completion | **[ran]** matrix rows |

---

## 5. Production readiness, before the first real rolling lease with Fenix

Ordered. Each item read-only.

1. **Build 41 on TestFlight and on Fenix's phone.** Until then an allowlisted driver on build 33
   is refused at request. Verify after he opens the app:
   `fly logs -a drivebai-api-team | grep user_agent | grep lease-requests` → `DriveBai/41`.
2. **Allowlist contains both parties.** `GET /api/v1/admin/config` → `rolling_allowlist_size`
   matches the count you set, `rolling_allowlist_broken: false`; boot line
   `"weekly rentals: ON" allowlisted_drivers=N open_to_everyone=false`.
3. **Flags are as intended.** Same endpoint: `recurring_only: false`,
   `monthly_rentals_enabled: false`.
4. **Fenix's licence is not rejected.**
   `select status from documents where user_id='bbe2577f…' and type='drivers_license'` →
   currently `verified` **[prod-ro]**.
5. **He owes nothing.** `select count(*) from driver_debts where driver_id='bbe2577f…' and status='open'` → 0 **[prod-ro]**.
6. **The car is rentable and its owner is in the pilot.**
   `select status, reserved_by_lease_request_id, rent_price_period from cars where id=…` →
   `available`, `NULL`, `weekly`.
7. **The owner has accepted the weekly owner terms**, or will be shown the sheet on build 38+.
   `select count(*) from owner_terms_acceptances where owner_id=…` — do **not** create this row
   on anyone's behalf; the admin path requires a note saying how agreement was obtained.
8. **Stripe webhook is live.** `fly logs | grep "webhook: event received"` shows recent
   `verified:true` events.
9. **No stale holds.** `GET /api/v1/admin/stale-car-holds` → `{"holds":null}`.

### The notice promise, stated as a risk

The weekly consent text says **"We'll remind you before every charge"** and the engine sends
that reminder 48 h ahead as a **push and in-app notification** — not email
(`billing_engine.go:147-150`) **[code]**. So the dead MailerSend key does **not** break the
promise. But two things follow, and both are live today:

- A driver who **denies push** gets only the in-app card. There is no second channel.
- The charge does **not** wait for the notice: the mint predicate has no
  `renewal_notified_for` clause (`lease_request_repository.go:2142-2155`) **[code]**. A failed
  notice means the money still moves on schedule.

**Status: accepted risk, unchanged by this batch.** Rotating the mail key would let a future
version add email as a second channel; it is not a blocker for the weekly pilot.

---

## 6. What this plan does not cover

- Uploading build 41 to TestFlight — blocked on the App Store Connect Issuer ID.
- Monthly in production — `MONTHLY_RENTALS_ENABLED` stays off until the owner package, the
  amendment package and the sheet chrome are done (§11 checklist in the design doc).
- The two stale July rentals on the reviewer account, which the owner-release endpoint can
  clear when someone decides to.
