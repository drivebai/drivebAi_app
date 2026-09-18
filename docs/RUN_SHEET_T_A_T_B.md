# Run sheet — T-A (weekly) and T-B (monthly), build 41 against local

One line per step. Exact button label only. Say **"done N"** after each; I verify and tell you
the next tap. On a FAIL I stop and diagnose before you tap again.

**Two phones/simulators.** D = signed in as the driver, O = signed in as the owner.
Credentials and setup in the readiness block.

**Never tap anything on production during this run.** The app must be pointed at local —
Discover shows exactly **two** cars, a Honda CR-V and a Toyota Camry. If you see a Mustang or
a Transit you are on production; stop.

---

## T-A — weekly recurring, 2021 Honda CR-V ($150/week)

| # | Who | Tap |
|---|---|---|
| 1 | D | Sign in — `b41.driver@example.test` / `Repro-Pass-1234` (use **"Use password instead"** if the code screen appears) |
| 2 | D | Discover → **2021 Honda CR-V** |
| 3 | D | **"Rent — renews weekly"** |
| 4 | D | **"Send request"** (if the explainer card appears) |
| 5 | O | Sign in — `b41.owner@example.test` / `Repro-Pass-1234` |
| 6 | O | Chats → CR-V thread → **"Accept"** |
| 7 | O | Owner terms sheet → read → accept |
| 8 | O | **"Accept"** |
| 9 | D | **"Review weekly terms & pay"** |
| 10 | D | **STOP. Screenshot the consent sheet** so the whole body text is readable, then send it to me. Do not tap yet. |
| 11 | D | **"Pay $150.00 and start rental"** → card `4242 4242 4242 4242`, any future expiry, any CVC → pay |
| 12 | O | Today → **"I handed over the keys"** |
| 13 | D | Today → **"I received the keys"** |
| 14 | D | Chat → **"I've picked up the car"** |
| 15 | — | *No tap.* Tell me "done 15" and wait ~60 s; I check the cycle was minted. |
| 16 | — | *No tap.* I advance the clock; you watch for a notification and tell me what arrives. |
| 17 | — | *No tap.* I advance to week 2; I verify the charge. |
| 18 | D | Chat → **"Request to Return the Vehicle"** |
| 19 | O | Today → **"Confirm return"** |
| 20 | — | *No tap.* I verify the pro-rata refund. |

## T-B — monthly recurring, 2022 Toyota Camry ($600/month), return on day 20 of 28

| # | Who | Tap |
|---|---|---|
| 21 | D | Discover → **2022 Toyota Camry** |
| 22 | D | **"Rent — renews monthly"** |
| 23 | D | **"Send request"** |
| 24 | O | Chats → Camry thread → **"Accept"** |
| 25 | D | **"Review monthly terms & pay"** |
| 26 | D | **STOP. Screenshot the consent sheet** and send it. Do not tap yet. |
| 27 | D | **"Pay $600.00 and start rental"** → same card → pay |
| 28 | O | Today → **"I handed over the keys"** |
| 29 | D | Today → **"I received the keys"** |
| 30 | D | Chat → **"I've picked up the car"** |
| 31 | — | *No tap.* I mint cycle 1 and advance to day 20 of 28. |
| 32 | D | Chat → **"Request to Return the Vehicle"** |
| 33 | O | Today → **"Confirm return"** |
| 34 | — | *No tap.* I verify the refund is **$171.60** and the owner kept **$428.40**. |

---

## What I verify after each step

| After | Query | Expect |
|---|---|---|
| 1 | `select role from users where email='b41.driver@example.test'` | `driver` |
| 2 | server log | `GET /api/v1/cars/<id>` — and **no** `/config` call on this screen |
| 4 | `select status,billing_mode,billing_interval from lease_requests order by created_at desc limit 1` | `requested \| rolling \| weekly` |
| 6 | HTTP status of the accept | `409 OWNER_TERMS_REQUIRED` (first time only) |
| 7 | `select terms_version,channel from owner_terms_acceptances` | `owner-rolling-v2 (2026-09-10) \| app` |
| 8 | lease row | `accepted \| rolling` |
| 9 | `select terms_version,billing_interval,amount_cents,activated_at,disclosure_text from lease_billing_consents` | row exists, `weekly`, `15000`, `activated_at` NULL |
| 10 | **your screenshot vs `disclosure_text`** | **byte-identical** |
| 11 | lease + consent | `paid`; `activated_at` set; card fingerprint stored |
| 12–13 | `select status from key_handovers` | `owner_confirmed` → `completed` |
| 14 | lease | `pickup_confirmed_at` set; car reserved; `rental_ends_at` = pickup + 7 d |
| 15 | `select cycle_number,status,period_start,period_end from billing_cycles` | `1 \| paid \| … \| +7 d` |
| 16 | `select renewal_notified_for from lease_requests` + what you saw | stamped; you report the channel |
| 17 | cycles + payouts | cycle 2 `paid`, $150; week-1 payout `pending` |
| 19 | `select status,used_days from vehicle_returns` | `owner_confirmed`/`completed` |
| 20 | cycle + Stripe refunds | `partially_refunded`, exactly one refund |
| 23 | lease row | `requested \| rolling \| monthly` |
| 25 | consent row | `monthly`, `60000`, `rolling-billing-monthly-v2` |
| 26 | **screenshot vs stored text** | **byte-identical**, and it must say **28 days**, not 7 |
| 27 | lease + consent | `paid`; activated |
| 30 | lease | `rental_ends_at` = pickup + **28 d** |
| 34 | cycle + refunds + payout | refund **17160**, kept **42840**, one Stripe refund |

## Also recorded as we go

- Where you first see the word "weekly" / "monthly".
- What the owner's push says when the request arrives.
- What the pre-charge notice says and **through which channel it actually arrives**.
- **Every notification that does not arrive.** Tell me when you expect one and get nothing.
