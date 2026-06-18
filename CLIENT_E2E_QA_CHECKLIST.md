# DriveBai — Client End-to-End QA Checklist

> Use this during a live QA session with the client. Tick **PASS / FAIL / Skipped** on each row as you go. Capture screenshots and tester quotes inline so we leave the call with a single document covering every flow.

**Today:** _________  **Build:** _________  **QA lead:** _________  **Client lead:** _________

---

## 0. Pre-QA setup

### Endpoints + builds

| Surface | URL / version |
|---|---|
| Backend API | https://drivebai-api-team.fly.dev |
| Backend health | `curl -i https://drivebai-api-team.fly.dev/health` → 200 |
| Admin panel | https://drivebai-admin-team.fly.dev/admin/login |
| iOS build | TestFlight build _____ (CFBundleVersion `____`, MARKETING_VERSION `____`) |
| Backend Fly app | `drivebai-api-team` |
| Admin Fly app | `drivebai-admin-team` |

### Test accounts

| Role | Account | Notes |
|---|---|---|
| Admin | `knightbridgeworldwide@gmail.com` | Password rotated locally — see `.deploy/admin-password.plaintext` (delete after the session) |
| Driver | `driver-qa+<date>@drivebai.test` | Fresh account; upload license + registration before the session |
| Owner | `owner-qa+<date>@drivebai.test` | Fresh account; create one approved listing before the session |
| Second driver | `driver2-qa+<date>@drivebai.test` | For race / concurrency demos |

### Stripe test mode

| Field | Value |
|---|---|
| Card number | `4242 4242 4242 4242` |
| Expiry | any future date |
| CVC | any 3 digits |
| ZIP | any 5 digits |
| Expected | PaymentIntent → succeeded; webhook arms pickup deadline; refund posts to test card on missed pickup |
| Stripe dashboard | https://dashboard.stripe.com/test/payments — keep open during the session |

### Environment (live values to confirm before the call)

```bash
fly secrets list -a drivebai-api-team | grep -E 'STRIPE|JWT|UPLOAD_URL|CORS|AUTO_APPROVE'
# Expect all DEPLOYED, AUTO_APPROVE_CARS digest matches the "false" value
```

| Knob | Production default | Demo override (if used) |
|---|---|---|
| `PICKUP_DEADLINE_MINUTES` | `120` | `5` for demo cohort (or `2` for the deadline-missed scenario) |
| `PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS` | `60` | `15` for demo |
| `UPLOAD_URL_TTL` | `1h` | unchanged |
| `REQUIRE_PRIVATE_UPLOAD_SIGNATURES` | `true` | **must stay true** |

**If you change `PICKUP_DEADLINE_MINUTES` for the demo:** `fly secrets set PICKUP_DEADLINE_MINUTES=5 -a drivebai-api-team`. Reset to `120` after.

### Devices / simulators

- 2 physical iPhones recommended (owner phone + driver phone) — same Wi-Fi or both on cellular.
- 1 laptop for the admin panel.
- Backup: iOS simulator on the laptop running the driver build.

### Data to prepare BEFORE the client joins

- [ ] One approved car listing on the owner account, with at least 2 photos and a pickup location set
- [ ] Driver onboarding complete (license + registration uploaded; onboarding_status = `complete`)
- [ ] One historical chat thread between owner and driver (so the chat list isn't empty)
- [ ] One car listing with car documents uploaded (insurance + registration) — for admin/private doc tests
- [ ] Admin panel logged out in a fresh incognito window
- [ ] Stripe dashboard logged in on a second tab
- [ ] Fly logs tailing in a terminal: `fly logs -a drivebai-api-team | grep -E 'expiry|webhook|refund|cors'`

---

## 1. Authentication flow

| # | Test | Steps | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|---|
| 1.1 | Driver signup | Email + password → confirm OTP → choose role "driver" | Lands on Discovery, role = driver |  |  |
| 1.2 | Owner signup | Same flow, role "owner" | Lands on Today, role = owner |  |  |
| 1.3 | Admin login | Open admin panel → email + password | Lands on `/admin/users`, role = admin |  |  |
| 1.4 | Invalid credentials | Login with wrong password | Red error "Invalid credentials"; no token stored |  |  |
| 1.5 | Token persists across restart | Login → force-quit app → relaunch | App opens to Today/Discovery without re-login |  |  |
| 1.6 | Logout | Profile → Logout | Returns to login screen; token removed |  |  |
| 1.7 | Password reset (if exposed) | "Forgot password" → enter email | Reset email arrives; new password accepted |  |  |
| 1.8 | OTP login (if exposed) | Email-only sign-in | OTP code arrives; code grants session |  |  |
| 1.9 | Rate limiting on auth | Wrong password 11+ times in a minute from same IP | 429 after 10 attempts |  |  |
| 1.10 | Admin role enforcement | Login on admin panel with a driver/owner account | Banner: "This account is not an admin." Token cleared. |  |  |

---

## 2. Driver onboarding + documents

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 2.1 | Upload driver license | File saves, thumbnail shows |  |  |
| 2.2 | Upload registration | Same as 2.1 |  |  |
| 2.3 | Onboarding completes when all required docs uploaded | Status flips to `complete`; Discovery unlocked |  |  |
| 2.4 | Missing-docs gate blocks lease request | Try lease request without docs → `DRIVER_DOCS_REQUIRED` error |  |  |
| 2.5 | License URL is signed | Open profile, inspect `license_document_url` → ends with `?sig=&exp=` |  |  |
| 2.6 | Unrelated user cannot fetch license URL | `GET /users/{otherDriverId}/profile` from another driver → `license_document_url` field is absent |  |  |
| 2.7 | Related owner CAN see license | Owner who accepted a lease from driver → license URL present + opens |  |  |
| 2.8 | Raw `/uploads/{userId}/drivers_license_*` 404s without sig | See section 15 curl |  |  |

---

## 3. Owner listing flow

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 3.1 | Create new car listing | Wizard completes; status = `pending` (or `available` if auto-approved staging) |  |  |
| 3.2 | Add 2+ photos | Photos upload, thumbnails render |  |  |
| 3.3 | Add price + weekly rate | Validation rejects below `MIN_WEEKLY_RENT_PRICE` |  |  |
| 3.4 | Add pickup location | Map pin saved; lat/lng visible in chat handover card later |  |  |
| 3.5 | Listing appears in owner's My Cars / Today | Shows correct status badge |  |  |
| 3.6 | Listing appears in Discovery after admin approval | Approve via admin panel → reappears on driver side |  |  |
| 3.7 | Edit listing (if supported) | Save persists; Discovery updates |  |  |
| 3.8 | Pause / unpause listing | Status reflects; hidden from Discovery while paused |  |  |
| 3.9 | Upload car documents (insurance, registration) | Stored, only owner sees them |  |  |
| 3.10 | Car photo URLs are public | `curl -I` on the car photo URL → 200, no sig needed |  |  |
| 3.11 | Car documents are PRIVATE | `curl -I` without sig on `/uploads/cars/<id>/documents/<file>` → 404 |  |  |

---

## 4. Discovery / marketplace

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 4.1 | Driver sees approved listings | Owner's car shows in the list |  |  |
| 4.2 | Filter / search (if implemented) | Results narrow correctly |  |  |
| 4.3 | Open listing detail | Photos, price, location, owner name |  |  |
| 4.4 | Photos load within ~1s on Wi-Fi | No spinner > 2s |  |  |
| 4.5 | Reserved car hidden | After owner accepts a lease, refresh Discovery on second driver → car gone |  |  |
| 4.6 | Relisted after cancel/refund | Owner rescinds OR pickup deadline elapses → refresh Discovery → car back |  |  |
| 4.7 | Map / location view (if implemented) | Pin renders at the right area |  |  |

---

## 5. Lease request flow

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 5.1 | Driver creates lease request | Card appears in chat (driver side); push to owner |  |  |
| 5.2 | Owner sees request in Today + Chat | Request card visible with Accept/Decline + Adjust Price |  |  |
| 5.3 | Owner accepts | Status → `accepted`; car disappears from Discovery |  |  |
| 5.4 | Owner declines | Status → `declined`; driver gets push |  |  |
| 5.5 | Owner rescinds an accepted-but-unpaid lease | Tap "Cancel acceptance" → confirm → status `cancelled`; car back on market |  |  |
| 5.6 | Driver can cancel pending request | Status → `cancelled` |  |  |
| 5.7 | Paid lease CANNOT be rescinded | After payment, owner sees no rescind button; backend returns 409 on direct call |  |  |
| 5.8 | Cannot create two requests for the same car at once | Second request from same driver returns `DUPLICATE_LEASE_REQUEST` |  |  |

---

## 6. Payment flow

Use Stripe test card `4242 4242 4242 4242`, any future expiry, any CVC.

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 6.1 | Driver taps "Pay Now" after acceptance | Stripe PaymentSheet opens with correct amount |  |  |
| 6.2 | Successful payment | Sheet dismisses; lease card flips to "Paid"; status `paid` |  |  |
| 6.3 | Card declined (`4000 0000 0000 0002`) | Sheet shows error, can retry; lease stays `payment_pending` |  |  |
| 6.4 | Cancel PaymentSheet mid-payment | No charge; can re-open PaymentSheet |  |  |
| 6.5 | Webhook arrives within ~5s | Fly logs: `webhook: event received type=payment_intent.succeeded` |  |  |
| 6.6 | Polling fallback also flips status | Sync endpoint also moves lease to `paid` even with webhook disabled (don't do this in client demo) |  |  |
| 6.7 | Pickup deadline armed on success | Inspect lease via admin / API → `pickup_deadline_at` set, 120 min in the future |  |  |
| 6.8 | Idempotent retry | Tap "Pay" twice rapidly → only one PaymentIntent in Stripe dashboard |  |  |
| 6.9 | Logs do not leak card data | `fly logs` grep for `4242` or `cvc` → 0 matches |  |  |

---

## 7. Pickup deadline + reservation flow

> For client demo, set `PICKUP_DEADLINE_MINUTES=5` and `PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=15` BEFORE this section. Restore to 120/60 after.

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 7.1 | Driver sees pickup countdown on chat lease card | HH:MM format (e.g. `00h 04m`), no seconds |  |  |
| 7.2 | Driver sees pickup countdown on Today key-handover card | Same value as 7.1 |  |  |
| 7.3 | Owner sees mirror countdown on Today card | "Driver pickup deadline 00h 04m" + "Add time" pill |  |  |
| 7.4 | Urgency tier: normal (>60m left) | Calm primary tint, light weight |  |  |
| 7.5 | Urgency tier: warning (15–60m) | Amber/orange |  |  |
| 7.6 | Urgency tier: critical (<15m) | Red, bold, exclamation icon |  |  |
| 7.7 | Owner taps "Add time" → preset sheet | Shows +15 / +30 / +1h options that fit the remaining cap |  |  |
| 7.8 | +15 min extension | Both timers bump immediately (WS event), no full reload |  |  |
| 7.9 | Extension cap (120 total) enforced | After multiple extensions, "Add time" hides; subline reads "Maximum extra pickup time has already been added." |  |  |
| 7.10 | Driver cannot extend | No button on driver side; direct API call → 403 |  |  |
| 7.11 | Pickup deadline expiry triggers refund | After timer hits 0, within scan interval: lease → `expired_refunded`; Stripe refund posted |  |  |
| 7.12 | Car relists after refund | Discovery shows the car again on second driver's device |  |  |
| 7.13 | Terminal card shows "Pickup deadline missed — payment refunded" + "Got it" | Both sides see correct copy + button |  |  |
| 7.14 | "Got it" dismisses card | Card disappears on that user's Today; does not reappear after app restart |  |  |
| 7.15 | Owner and driver dismiss independently | Owner dismissing leaves driver card visible until driver dismisses |  |  |
| 7.16 | Race: extend AND timer expires at same instant | Either extend wins (timer bumps) OR refund wins (terminal card). Never both. Stripe dashboard: 0 or 1 refund, never 2. |  |  |

After this section: **`fly secrets set PICKUP_DEADLINE_MINUTES=120 PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=60 -a drivebai-api-team`**

---

## 8. Key handover flow

> Run a fresh lease for this section (re-pay the test card).

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 8.1 | Owner taps "I handed over the keys" | Status → `owner_confirmed`; 15-min driver confirmation window opens |  |  |
| 8.2 | Pickup countdown DISAPPEARS once owner confirms | Only the 15-min handover confirmation timer renders (no double countdown) |  |  |
| 8.3 | Driver taps "I received the keys" | Status → `completed`; rental is active |  |  |
| 8.4 | Both Today screens update via WebSocket | No manual refresh needed |  |  |
| 8.5 | Driver doesn't confirm within 15 min | Status → `expired`; both parties notified; admin can intervene |  |  |
| 8.6 | After successful handover, car stays hidden from Discovery | Reserved while rental is active |  |  |

---

## 9. Chat flow

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 9.1 | Owner sends text message | Bubble appears on driver phone within 1s |  |  |
| 9.2 | Driver replies | Same — instant |  |  |
| 9.3 | Messages persist | Kill app, reopen → history loads from server |  |  |
| 9.4 | Chat list shows latest message + timestamp | Sort by most recent |  |  |
| 9.5 | Unread badge on chat row | Increments when message arrives while chat closed |  |  |
| 9.6 | WebSocket drops + reconnects | Toggle Wi-Fi; message sent during outage arrives once reconnected |  |  |
| 9.7 | Polling fallback (after 10 reconnect failures) | Chat still updates within 5s |  |  |
| 9.8 | Open counterparty profile | Name, avatar, member since, license link (if related) |  |  |
| 9.9 | Profile license link follows access rules | Driver viewing owner: no license. Owner viewing driver they're chatting with: license present + signed |  |  |
| 9.10 | Typing indicator (if implemented) | Appears within 1s |  |  |

---

## 10. Attachments + files in chat

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 10.1 | Send a photo (camera or library) | Upload progress → bubble with image |  |  |
| 10.2 | Receiver sees image | Loads from signed URL |  |  |
| 10.3 | Image persists after app restart | History reload shows image |  |  |
| 10.4 | Send multiple photos in one batch | All upload, ordered correctly |  |  |
| 10.5 | Send PDF | Document bubble with file name + size |  |  |
| 10.6 | Tap PDF | Opens in in-app QuickLook viewer |  |  |
| 10.7 | Tap image | Full-screen viewer with pinch-zoom + dismiss |  |  |
| 10.8 | Long-press save to Photos (if implemented) | Permission prompt → saves to library |  |  |
| 10.9 | Raw `/uploads/chats/<chat>/file.jpg` without sig | `curl -I` → 404 |  |  |
| 10.10 | Tampered sig → 404 | Append `?sig=deadbeef&exp=9999999999` → 404 |  |  |
| 10.11 | Expired sig → 404 / app refreshes JSON | After TTL elapses (~1h), reloading the chat re-fetches a fresh URL |  |  |

---

## 11. Driver documents visibility in request / chat details

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 11.1 | Owner viewing an open lease request sees driver docs | License + registration tiles render |  |  |
| 11.2 | Tap document opens full view | Image / PDF loads cleanly |  |  |
| 11.3 | All doc URLs include `?sig=&exp=` | Inspect via the API response payload |  |  |
| 11.4 | Unrelated authenticated user calls `/users/{driverId}/profile` | `license_document_url` absent from payload |  |  |
| 11.5 | Same call AFTER opening a chat with that driver | `license_document_url` now present + signed |  |  |
| 11.6 | Driver can always see own license | Profile → docs → license loads |  |  |

---

## 12. Accident report flow

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 12.1 | Start a new accident report from a chat / car context | Wizard opens with step indicator |  |  |
| 12.2 | Fill required fields | Driver 1 info, driver 2 info, vehicle damage |  |  |
| 12.3 | Pick collision diagram | Correct icon shown |  |  |
| 12.4 | Upload accident photos (multiple slots) | Each slot shows the uploaded file |  |  |
| 12.5 | Draw + save handwritten signature | Signature saved; `signature_signed_at` set |  |  |
| 12.6 | Save draft | Close + reopen → draft restored at the right step |  |  |
| 12.7 | Submit final report | Status → `submitted`; admin gets WS event |  |  |
| 12.8 | Accident appears in admin panel | Admin sees the new submission |  |  |
| 12.9 | All accident files require signed URLs | Curl raw path without sig → 404 |  |  |
| 12.10 | Related reporter can fetch signature + photos | App displays them |  |  |
| 12.11 | Unrelated user cannot fetch report | `GET /accidents/{id}` → 404 |  |  |

---

## 13. Notifications + Today screens

### 13.A Driver Today

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 13.A.1 | "Your rental" card shows active rental | Car title + pickup status |  |  |
| 13.A.2 | Key handover card appears post-payment | With pickup countdown |  |  |
| 13.A.3 | Pickup timer ticks (HH:MM) | Updates within ~15s |  |  |
| 13.A.4 | Expired/refunded "Got it" dismisses | Card removed; stays removed after refresh |  |  |
| 13.A.5 | Bell badge shows unread notification count | Updates on new notification |  |  |
| 13.A.6 | Pull-to-refresh | All sections re-fetch |  |  |
| 13.A.7 | Quick actions / Today actions appear | Lease requests, payments due |  |  |

### 13.B Owner Today

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 13.B.1 | Active listings render | Each card shows price + rented weeks + total |  |  |
| 13.B.2 | Key handover card appears post-driver-payment | Has "I handed over the keys" CTA |  |  |
| 13.B.3 | "Add time" pill works | Surfaces preset sheet, applies extension |  |  |
| 13.B.4 | Expired/refunded "Got it" dismisses | Same as driver |  |  |
| 13.B.5 | Bell badge updates | Increments on new lease request |  |  |
| 13.B.6 | Pull-to-refresh | Re-fetches actions + handovers |  |  |
| 13.B.7 | "Actions to take" appears for pending lease requests | Inline Accept / Decline buttons work |  |  |

### 13.C Notifications

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 13.C.1 | New lease request creates in-app notification | Bell badge + entry in notification list |  |  |
| 13.C.2 | Tapping notification deep-links to chat / today card | Lands on the relevant screen |  |  |
| 13.C.3 | APNs push delivered when app backgrounded | Push banner appears on lock screen |  |  |
| 13.C.4 | Tap notification → mark as read | Badge decrements |  |  |
| 13.C.5 | "Mark all read" | Clears badge |  |  |

---

## 14. Admin panel QA

> Open in a fresh **Incognito** window so cached state doesn't mask issues.

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 14.1 | Open https://drivebai-admin-team.fly.dev | Redirects to `/admin/login` |  |  |
| 14.2 | Login with `knightbridgeworldwide@gmail.com` | Lands on `/admin/users` |  |  |
| 14.3 | Users table loads | First page of users renders |  |  |
| 14.4 | Open a user | Details page shows role, status, documents |  |  |
| 14.5 | Block / unblock user (if implemented) | `is_blocked` toggles |  |  |
| 14.6 | Vehicles / Cars page | List of all cars |  |  |
| 14.7 | Approve / pause car (if implemented) | Status flips; reflected in Discovery |  |  |
| 14.8 | Chats page | Conversation list + messages |  |  |
| 14.9 | Rents page | Lease requests + payment status visible |  |  |
| 14.10 | Support page | Support chats work (admin can reply) |  |  |
| 14.11 | Accidents page | Submitted accident reports render with attachments |  |  |
| 14.12 | Network tab inspection | Requests go to `https://drivebai-api-team.fly.dev/api/v1/...` (NOT localhost, NOT same-origin) |  |  |
| 14.13 | Refresh a deep link (`/admin/users`) | Loads cleanly, no 404 |  |  |
| 14.14 | Logout | Token cleared, redirected to `/admin/login` |  |  |
| 14.15 | JS bundle does not contain `JWT_SECRET`, `STRIPE_SECRET`, `UPLOAD_URL_SECRET`, or `localhost:8080` | `curl -s https://drivebai-admin-team.fly.dev/assets/index-*.js \| grep -cE 'localhost\|JWT_SECRET\|STRIPE_SECRET\|UPLOAD_URL_SECRET'` → `0` |  |  |

---

## 15. Security smoke tests

Run from a terminal. Replace `<token>`, `<chatId>`, `<driverId>`, `<carId>`, `<accidentId>` with real IDs from the QA session. All "→ N" values are the expected HTTP status.

### 15.1 Private files reject unsigned access

```bash
# Chat attachment
curl -s -o /dev/null -w "chat: %{http_code}\n" \
  https://drivebai-api-team.fly.dev/uploads/chats/<chatId>/sample.jpg
# Driver license
curl -s -o /dev/null -w "license: %{http_code}\n" \
  https://drivebai-api-team.fly.dev/uploads/<driverId>/drivers_license_x.jpg
# Accident attachment
curl -s -o /dev/null -w "accident: %{http_code}\n" \
  https://drivebai-api-team.fly.dev/uploads/accidents/<accidentId>/photo_x.jpg
# Car document
curl -s -o /dev/null -w "car-doc: %{http_code}\n" \
  https://drivebai-api-team.fly.dev/uploads/cars/<carId>/documents/insurance.pdf
# Expected: every line → 404
```

### 15.2 Public car photo passes through

```bash
curl -s -o /dev/null -w "car-photo: %{http_code}\n" \
  https://drivebai-api-team.fly.dev/uploads/cars/<carId>/<photoFilename>
# Expected: 200
```

### 15.3 Profile license URL gating

```bash
TOKEN=<unrelated authenticated user's bearer token>
# Unrelated user should NOT see license_document_url
curl -s -H "Authorization: Bearer $TOKEN" \
  https://drivebai-api-team.fly.dev/api/v1/users/<driverId>/profile \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); print("license present:", "license_document_url" in d)'
# Expected: license present: False

TOKEN=<owner who shares a chat with the driver>
curl -s -H "Authorization: Bearer $TOKEN" \
  https://drivebai-api-team.fly.dev/api/v1/users/<driverId>/profile \
  | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("license_document_url",""))'
# Expected: a URL ending in ?sig=...&exp=...
```

### 15.4 Tampered + expired signatures

```bash
# Get a real signed URL (e.g. from a chat attachment list), then mutate it
SIGNED=<paste signed URL here>

# Tamper with sig
curl -s -o /dev/null -w "tampered: %{http_code}\n" \
  "${SIGNED/sig=*&/sig=deadbeef&}"
# Expected: 404

# Expire
curl -s -o /dev/null -w "expired:  %{http_code}\n" \
  "${SIGNED/exp=*/exp=1}"
# Expected: 404
```

### 15.5 Traversal blocked

```bash
curl -s -o /dev/null -w "traversal: %{http_code}\n" \
  "https://drivebai-api-team.fly.dev/uploads/../etc/passwd"
# Expected: 404
```

---

## 16. Refund + money safety tests

| # | Test | Expected | PASS/FAIL | Notes |
|---|---|---|---|---|
| 16.1 | Missed pickup triggers refund | Within scan interval, lease → `expired_refunded`, `refund_id` set, `refunded_at` set |  |  |
| 16.2 | Car relisted | Discovery shows car again |  |  |
| 16.3 | Terminal card on both sides | "Pickup deadline missed — payment refunded" + Got it |  |  |
| 16.4 | Got it dismissal sticky across restart | Reopen app → card gone |  |  |
| 16.5 | Stripe dashboard shows EXACTLY ONE refund | Search PaymentIntent → Refunds tab |  |  |
| 16.6 | Logs show exactly one completed refund | See command below |  |  |
| 16.7 | Stuck-refund query is empty | See SQL below |  |  |
| 16.8 | Simulated mid-refund crash → next sweep recovers | Manual setup; see "Crash recovery drill" |  |  |

### Log check (run on operator laptop while testing)

```bash
fly logs -a drivebai-api-team \
  | grep -E 'expiry: claimed|expiry: refund completed|expiry: refund retry'
# For each missed-pickup lease, expect:
#   one "expiry: claimed lease_request_id=<id>"
#   one "expiry: refund completed lease_request_id=<id> refund_id=re_... persisted_status=succeeded"
# Should never see: two "refund completed" lines for the same lease_request_id
```

### SQL check — stuck refunds (run via fly ssh)

```bash
fly ssh console -a drivebai-api-team -C 'psql $DATABASE_URL'
```

Then in psql:

```sql
SELECT id, status, refund_status, refund_id, refunded_at, updated_at
FROM lease_requests
WHERE status = 'expired_refunded'
  AND refund_id IS NULL
  AND refunded_at IS NULL
  AND updated_at < NOW() - INTERVAL '5 minutes';
-- Expected: 0 rows.
-- Any row here is a dangling refund — investigate immediately (driver was
-- charged but not refunded). The scanner's stuck-refund retry phase
-- should have caught it within 2 minutes.
```

### Crash recovery drill (optional, advanced)

```bash
# 1. Take note of a paid lease's id
fly ssh console -a drivebai-api-team -C 'psql $DATABASE_URL -c "
  SELECT id FROM lease_requests WHERE status=''paid''
  ORDER BY created_at DESC LIMIT 1;
"'

# 2. Simulate a stuck-refund row
LEASE_ID=<paste id>
fly ssh console -a drivebai-api-team -C "psql \$DATABASE_URL -c \"
  UPDATE lease_requests
  SET status='expired_refunded', refund_status='pending',
      refund_id=NULL, refunded_at=NULL,
      updated_at=NOW() - INTERVAL '5 minutes'
  WHERE id='$LEASE_ID';
\""

# 3. Within ~2 min, expect retry log:
fly logs -a drivebai-api-team | grep -E "phase=retry.*$LEASE_ID|refund retry claimed"
# 4. After retry succeeds, refund_id should be populated:
fly ssh console -a drivebai-api-team -C "psql \$DATABASE_URL -c \"
  SELECT refund_id, refunded_at FROM lease_requests WHERE id='$LEASE_ID';
\""
# Expected: refund_id set, refunded_at recent.
# Stripe dashboard: still EXACTLY ONE refund for the PI (idempotency key worked).
```

---

## Post-QA wrap-up

- [ ] Reset `PICKUP_DEADLINE_MINUTES=120` and `PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=60` if changed for the demo
- [ ] Delete `.deploy/admin-password.plaintext` (admin must rotate their password from inside the app)
- [ ] File any FAIL items as issues with: tester quote, screenshot, reproduction steps, severity (P0/P1/P2)
- [ ] Capture Stripe dashboard refund count + Fly log tail for the session as evidence
- [ ] Schedule the next QA round only after all P0 + P1 items are closed

---

## Severity legend (for the issues filed at the end)

| Severity | Definition |
|---|---|
| **P0** | User loses money, leaks PII, or app crashes. Blocks beta launch. |
| **P1** | Core flow broken or misleading (timer wrong, payment status stuck, refund delayed > 5 min). Fix before inviting next testers. |
| **P2** | UX polish, copy, minor inconsistency. Track and fix in normal cadence. |
| **Skipped** | Feature not in scope for this release. Document why. |
