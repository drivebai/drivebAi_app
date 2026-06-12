# Rent-a-Car Lifecycle — Client Demo Script

> **What changed in this release**
> 1. A car gets **hidden from Discovery the instant the owner accepts** a lease request, so two drivers can't race for the same car.
> 2. After payment, the driver gets a **2-hour pickup window** to tap **"I've picked up the car"** in the chat.
> 3. If they don't, the system **automatically refunds the driver via Stripe** and **puts the car back on Discovery** — no manual cleanup needed.
> 4. Both sides see the countdown live; both get push notifications at every transition.

This script walks through the end-to-end flow with two phones (or one phone + the iOS simulator). The whole thing runs in about 10 minutes of clock time using `PICKUP_DEADLINE_MINUTES=2` for the demo.

---

## Prep

### 1. Backend env (deploy or local)

| Variable                              | Demo value | Notes                                                              |
| ------------------------------------- | ---------- | ------------------------------------------------------------------ |
| `PICKUP_DEADLINE_MINUTES`             | `2`        | Production default is `120` (2 hours). Use `2` for the demo.       |
| `PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS` | `15`       | Production default is `60`. Faster ticker lets the refund land quickly on stage. |
| `STRIPE_SECRET_KEY`                   | test key   | Refunds are exercised with the standard `sk_test_…` key.          |
| `STRIPE_WEBHOOK_SECRET`               | test key   | Required so `payment_intent.succeeded` arms the deadline.          |

For local dev, drop into `backend/.env` and `make run`. For Fly: `fly secrets set PICKUP_DEADLINE_MINUTES=2 PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=15 -a drivebai-api`.

### 2. Migrations

```bash
cd backend
make migrate-up   # applies 000024_pickup_deadline_and_reservation
```

Verify:
```bash
psql $DATABASE_URL -c "\d cars"           # should now show reserved_by_lease_request_id
psql $DATABASE_URL -c "\d lease_requests" # should now show pickup_deadline_at, pickup_confirmed_at, refund_*
```

### 3. Test accounts

| Role   | User             | Notes                            |
| ------ | ---------------- | -------------------------------- |
| Owner  | `owner@demo.app` | Has an approved car listed.      |
| Driver | `driver@demo.app`| Logged in on a separate device. |
| Driver | `other@demo.app` | Used for the "race" demo step.   |

Make sure the owner's listing is **Approved** so it actually appears in Discovery.

---

## Scenario A — Happy path (pickup confirmed in time)

1. **Driver A**: open **Discovery**, find the owner's car, send a lease request.
2. **Owner**: accept the request from the chat.
   - **✅ Show in Discovery**: switch to **Driver B**'s phone and refresh Discovery — *the car is gone*. (Backend has set `cars.reserved_by_lease_request_id`.)
3. **Driver A**: tap **Pay Now**, complete Stripe PaymentSheet (use test card `4242 4242 4242 4242`).
4. **Both sides** see the lease card flip to **Paid**.
5. **Driver A** now sees a yellow countdown card: *"Pickup by 1:58 …"* with a **"I've picked up the car"** button.
6. **Owner** sees a yellow mirror card: *"Waiting for driver pickup — 1:58 left"*.
7. Within the window, **Driver A** taps **"I've picked up the car"**.
   - Driver card flips to **"Pickup Confirmed"**.
   - Owner card flips to the same.
   - Both get push notifications: *"Pickup confirmed"*.
8. **✅ Show in Discovery**: the car *stays hidden* (rental is active).

> **What the audience just saw**: end-to-end paid rental, no human-in-the-loop refunds, no race conditions on Discovery.

---

## Scenario B — Driver misses the deadline (refund + relist)

Run this back-to-back with Scenario A using a fresh car/driver combo.

1. **Driver A**: send a lease request, **Owner** accepts.
2. **Driver A** pays with `4242 4242 4242 4242`.
3. **Driver A** does **nothing** — let the 2-minute countdown run out.
4. While you wait, narrate:
   > "There's a background worker on the API polling every 15 seconds. The moment the deadline elapses, it does three things atomically: marks the lease `expired_refunded`, releases the car back to Discovery, and fires a Stripe refund with an idempotency key keyed to the lease ID."
5. ~15 seconds after the countdown hits zero:
   - **Driver A**'s card flips to a grey *"Pickup deadline missed — payment refunded"*.
   - **Owner**'s card flips to *"Driver missed pickup — rental cancelled"*.
   - Both get push notifications.
6. **✅ Show in Discovery**: switch to **Driver B**'s phone and refresh. *The car is back.*
7. **✅ Show in Stripe**: open dashboard.stripe.com → Payments → the test payment now has a *"Refunded"* badge and a refund event with `metadata.idempotency_key = refund-<leaseID>`.

> **What the audience just saw**: zero-touch recovery. No support ticket, no manual unlisting, no chargeback risk.

---

## Scenario C — Owner extends, driver makes it (release 2)

Same prep as Scenario A. The "Add more time" CTA appears in the owner's
pickup card whenever the lease is in the awaiting-pickup state and the
extension cap (120 min total) hasn't been hit.

1. **Driver A**: send request, **Owner** accepts, **Driver A** pays.
2. Both sides see the new **HH/MM countdown** (`01h 58m`, then `01h 57m`, …).
   - **>60 min left**: primary tint, calm.
   - **15–60 min left**: amber, slightly larger digits.
   - **<15 min left**: red, exclamation icon, heavy font.
3. **Owner**: while the timer is running, tap **"+ Add time"** on the right
   side of the countdown card.
4. A confirmation sheet appears with the presets that still fit under the
   120-minute cap: **+15 / +30 / +1 hour**. Choose one.
5. The owner sees a confirmation push *"You added 30 minutes to the pickup
   deadline …"*; the driver sees *"The owner added 30 minutes to your
   pickup deadline …"*.
6. Both timers immediately reflect the new deadline (e.g., `01h 58m` →
   `02h 28m`). No reload needed — `lease_request_updated` WS event drives
   the UI flip.
7. **Driver A** taps **"I've picked up the car"** within the extended
   window → cards flip to **Pickup Confirmed**. Rental is active. Car
   stays hidden from Discovery.

> **What the audience just saw**: real-world flexibility without losing
> the auto-refund safety net. The owner stays in control of timing; the
> driver gets clear visual feedback at every state change.

## Scenario D — Owner extends but driver still misses

1. Set demo env (`PICKUP_DEADLINE_MINUTES=2`,
   `PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=15`).
2. **Driver A** pays.
3. **Owner** extends by **+15 min** before the original 2-minute window
   elapses. Card timer jumps to `17m`.
4. **Driver A** still does nothing. The scanner *does not* fire at the old
   deadline — it sees `pickup_deadline_at > NOW()` and skips.
5. After the **extended** deadline passes, the next scanner tick claims
   the row, issues the Stripe refund, and unreserves the car.
6. Both cards flip to grey **Pickup deadline missed — payment refunded**.
   The car returns to Discovery.

> **Edge case proven**: the worker honours the latest deadline, not the
> original one. There's no scheduler state to invalidate — the SQL guard
> is the contract.

## Scenario E — Race: owner extends while scanner is about to claim

1. Lower `PICKUP_EXPIRY_SCAN_INTERVAL_SECONDS=5` so the scanner is
   hammering.
2. Wait until the countdown is at ~`00h 00m` (under 15 s, deep critical
   tier).
3. **Owner** taps **+15 minutes**.
4. Two possible outcomes, both safe:
   - **Extend wins**: the `UPDATE` lands first. New `pickup_deadline_at`
     committed; the scanner's next tick sees the new deadline > NOW() and
     skips. Both cards refresh to the extended timer.
   - **Scanner wins**: `ClaimForExpiry` lands first; row is already
     `expired_refunded`. The owner's extend returns **409
     PICKUP_DEADLINE_PASSED**, the iOS error banner shows "The pickup
     deadline has already passed; the rental was refunded." The car is
     already back on Discovery.
5. Verify in `fly logs -a drivebai-api | grep -E 'expiry: claimed|pickup deadline extended'`
   — exactly one of these lines per lease, never both.

> **What the audience just saw**: a single SQL guard (`status='paid' AND
> pickup_deadline_at > NOW() …`) is the serialization point. No locks,
> no queues, no in-memory schedulers.

## Scenario F — Cap reached, button disappears

1. Sequence: Owner extends `+60`, then `+60`. Total is now 120 min — the
   cap.
2. The "Add time" button no longer renders in the owner's card; the
   subline reads *"Pickup deadline already extended by 120 min — no more
   time can be added."*
3. Try forcing the API anyway (e.g., curl from a paired phone):
   ```
   curl -X POST -H "Authorization: Bearer $OWNER_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"minutes": 15}' \
     "$API/api/v1/lease-requests/$LEASE/pickup-deadline/extend"
   ```
   Server returns **409 PICKUP_EXTENSION_CAP_REACHED**.

## Scenario G — Concurrency guard rails (optional, for technical buyers)

Goal: prove the system is multi-instance-safe.

1. Pick a paid lease that's about to expire.
2. From two terminals, race the refund:
   ```bash
   psql $DATABASE_URL -c "UPDATE lease_requests SET pickup_deadline_at = now() WHERE id = '<lease-id>';"
   # Wait one ticker interval (15s)
   ```
3. Tail the API logs:
   ```bash
   fly logs -a drivebai-api | grep expiry
   ```
   Expect exactly one `expiry: claimed` line followed by `expiry: refund completed` for that lease ID — even if you have multiple machines running. Losers of the race log nothing (the `UPDATE…RETURNING` guarded on `status='paid'` returns zero rows).
4. **Stripe dashboard**: still exactly one refund for that PaymentIntent.

> **What the audience just saw**: it's safe to scale horizontally — the database is the only source of truth, and the refund call is idempotency-keyed.

---

## Rollback story (for the security/ops question)

- **Disable the new flow without a deploy**: `fly secrets set PICKUP_DEADLINE_MINUTES=0 -a drivebai-api`. With `0`, the handler skips arming deadlines and the scanner has nothing to find — the lease stays in `paid` indefinitely and the car stays reserved. (Operators can manually unreserve via SQL if needed.)
- **Full migration rollback**: `make migrate-down` removes the new columns + reverts the status enum. The down migration first remaps any `expired_refunded` rows to `expired` so the ENUM cast doesn't fail.

---

## What's instrumented

| Signal               | Where                                                                                                                |
| -------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `pickup expiry scanner started` | API logs at boot — confirms the worker is running with the configured interval & deadline.                |
| `expiry: claimed`               | One log per lease the worker took ownership of — useful to prove only one instance claims a given row.    |
| `expiry: refund completed`      | One log per successful Stripe refund, with `refund_id`, `stripe_status`, `persisted_status`.              |
| `expiry: stripe refund` (Error) | Surfaces any Stripe-side failure; the row is parked at `refund_status='failed'` for a human to look at.  |
| WS event `lease_request_updated`| Drives the UI flip on both phones immediately, regardless of refresh.                                     |

---

## API surface (for the integration team)

- **New endpoint**: `POST /api/v1/lease-requests/{id}/pickup-confirm` — driver only. Returns the updated `LeaseRequest`. 409 with `PICKUP_DEADLINE_PASSED` if the worker already claimed it.
- **Extended schema**: `LeaseRequest` now carries `pickup_deadline_at`, `pickup_confirmed_at`, `refund_id`, `refunded_at`, `refund_status`, and the new terminal `status = "expired_refunded"`.
- **Discovery (`GET /api/v1/listings`)**: filters out any car with `reserved_by_lease_request_id IS NOT NULL`.

Full spec lives in `backend/cmd/api/static/openapi.yaml`, browsable at `https://api.drivebai.com/docs`.
