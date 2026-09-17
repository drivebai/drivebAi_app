# Rolling weekly billing — normative design

Status: DESIGN — awaiting client go. Synthesized 2026-09-07 from a six-section
parallel design plus two adversarial critiques (money-safety, state-machine);
every cross-section contradiction the critics found is resolved here, once.
This document is the single authority; where a critique quoted an earlier
section saying otherwise, THIS text wins.

Baseline: checkpoint-pre-recurring-billing (backend v89, migration 000052).

---

## 0. Shape in one paragraph

A rolling lease bills **one week at a time, indefinitely**: the first week is
charged customer-present in today's PaymentSheet (now saving the card with
`setup_future_usage=off_session` under explicit recurring consent); every
later week is ONE PaymentIntent per cycle, created once and confirmed
off-session at **T−24h** before paid-through lapses, retried by re-confirming
the SAME intent. A successful charge atomically advances `rental_ends_at`
(paid-through) by 7 days and re-arms the notice flags. The owner is paid
**per cycle in arrears**: the cycle's `owner_payouts` row accrues at charge
time and transfers when the week is CONSUMED — weekly cadence, one week
trailing, and the entire mid-week-refund class becomes clawback-free. The
term scanner stays the safety net: a stopped, delinquent, or
consent-revoked rolling lease degenerates into a fixed-term tail and the
existing overdue/T+72h-ticket machinery drives it to the return flow.
Fixed-term leases are untouched — every new predicate keys on
`billing_mode='rolling'`, whose default is `'fixed_term'`.

## 1. Vocabulary and schema ownership (resolves critics: 3 ledgers, 3 enums, 5 migrations)

- `billing_mode`: `'fixed_term' | 'rolling'`, DEFAULT `'fixed_term'`,
  immutable after insert. No other spelling exists.
- ONE per-cycle ledger: **`billing_cycles`**. The `rental_invoices` and both
  earlier `billing_cycles` variants are dead; `blocked_by_arrears` does not
  exist (see §4 — arrears stacking is structurally impossible).
- ONE consent store: **`lease_billing_consents`** — the ONLY source of the
  charge amount and payment method for every off-session charge.
- Migration numbering: **000053** lease columns (term section's sketch +
  `renewal_halted_reason`), **000054** `billing_cycles` +
  `lease_billing_consents`, **000055** `owner_payouts` extension.
  `payments` is NOT touched — renewals never write payments rows; the
  orphaned-payment sweep and SyncPaymentStatus keep their invariants.

## 2. Consent and mandate (section a, adopted with two amendments)

- Cycle 1 rides the existing PaymentSheet with `setup_future_usage=off_session`
  (no SetupIntent for new bookings). Consent row is written BEFORE the PI:
  amount_cents, interval `weekly`, terms_version, disclosure copy shown
  ("$X/week, charged every {weekday}; first charge today; continues until you
  return the vehicle; end anytime in-app; mid-week returns settle pro-rata").
  The `payment_intent.succeeded` webhook stamps `stripe_payment_method_id` +
  card meta onto the row (claimed-once `activated_at`).
- Renewals: `off_session=true, confirm=true`, `payment_method` and amount
  READ FROM THE ACTIVE CONSENT ROW at every attempt (resolves the
  consent-split-brain finding). The executor hard-asserts
  `cycle.amount_cents == consent.amount_cents` before any Stripe call.
- Mandate objects: skipped for US cards; our consent rows + receipts +
  enrollment email are the record. Statement descriptor suffix on renewals.
- `authentication_required`: on-session rescue (PaymentSheet against the SAME
  PI); does not consume the retry budget.
- Price change on a live rolling lease: **v1 forbids it.** The owner's lever
  is terminate-renewal + offer a continuation at the new price (existing
  offered-price machinery on the continuation lease). This deletes the
  supersession/re-price deadlock entirely. (v2 may add supersession with
  driver re-consent + scheduled-cycle re-price in one transaction.)
- Card update mid-lease (the ONE SetupIntent use): SetupIntent
  (`usage=off_session`) via PaymentSheet setup mode; success writes a NEW
  consent row (same amount/terms), retiring the old. Card-BRAND change via
  network updater revokes consent per network rules →
  `renewal_halted_reason='consent_revoked'` (in the CHECK, resolves the
  black-hole finding), both parties notified, paid-through lapses naturally
  and the term scanner's overdue machinery drives the exit; driver un-halts
  by completing the card-update ceremony.
- Amendments vs section (a): (1) its stable CREATE-key-per-week retry
  mechanism is deleted — one PI per cycle, confirm-retries (Stripe
  idempotency keys expire ~24h; re-creating on day 3 double-charges);
  (2) its mid-term SetupIntent migration of live fixed-term leases is
  deleted — conversion is continuation-only (§7).

## 3. The ledgers (migrations 000054/000055)

```sql
-- 000054
CREATE TABLE lease_billing_consents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id UUID NOT NULL REFERENCES lease_requests(id),
    driver_id UUID NOT NULL REFERENCES users(id),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    billing_interval TEXT NOT NULL DEFAULT 'weekly' CHECK (billing_interval = 'weekly'),
    terms_version TEXT NOT NULL,
    disclosure_text TEXT NOT NULL,
    stripe_payment_method_id TEXT,
    card_brand TEXT, card_last4 TEXT, card_fingerprint TEXT,
    activated_at TIMESTAMPTZ,           -- claimed-once, stamped by webhook
    revoked_at TIMESTAMPTZ, revoked_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_billing_consents_active
    ON lease_billing_consents(lease_request_id) WHERE revoked_at IS NULL;

CREATE TABLE billing_cycles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id UUID NOT NULL REFERENCES lease_requests(id),
    cycle_number INT NOT NULL,                    -- 1 = first week
    period_start TIMESTAMPTZ NOT NULL,
    period_end   TIMESTAMPTZ NOT NULL,            -- = period_start + 7d
    amount_cents BIGINT NOT NULL,
    status TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN
        ('scheduled',      -- minted at T-24h, not yet charged
         'charging',       -- PI confirm in flight
         'needs_action',   -- authentication_required; driver rescue, 72h TTL
         'retrying',       -- declined, ladder not exhausted
         'paid',
         'failed_final',   -- ladder exhausted; lease delinquent
         'arrears_due',    -- unpaid at return; on-session collection only
         'waived',         -- admin write-off (note required)
         'refunded',       -- fully refunded (overshoot at return / pickup no-show)
         'partially_refunded')),                  -- final short cycle, pro-rata
    stripe_payment_intent_id TEXT,
    attempt_count INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ,
    last_decline_code TEXT,                       -- support only, never shown
    needs_action_since TIMESTAMPTZ,
    refunded_cents BIGINT NOT NULL DEFAULT 0,
    refund_id TEXT,
    -- claimed-once notification stamps, per cycle (per-episode by construction)
    renewal_notice_sent_at TIMESTAMPTZ,
    failure_notified_at   TIMESTAMPTZ,
    delinquent_notified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX uq_billing_cycles_lease_cycle ON billing_cycles(lease_request_id, cycle_number);
CREATE INDEX idx_billing_cycles_due ON billing_cycles(next_attempt_at)
    WHERE status IN ('scheduled','retrying');

-- 000055 (owner_payouts: legacy settle-once preserved EXACTLY)
ALTER TABLE owner_payouts
    ADD COLUMN billing_cycle_id UUID REFERENCES billing_cycles(id),
    ADD COLUMN period_start TIMESTAMPTZ, ADD COLUMN period_end TIMESTAMPTZ,
    ADD COLUMN consumed_at TIMESTAMPTZ,
    ADD COLUMN stripe_reversal_id TEXT,
    ADD COLUMN reversed_amount_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN reversed_at TIMESTAMPTZ, ADD COLUMN reversal_reason TEXT;
ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_lease_request_id_key;
CREATE UNIQUE INDEX uq_owner_payouts_legacy ON owner_payouts(lease_request_id)
    WHERE billing_cycle_id IS NULL;               -- fixed-term: settle once, as today
CREATE UNIQUE INDEX uq_owner_payouts_cycle ON owner_payouts(billing_cycle_id)
    WHERE billing_cycle_id IS NOT NULL;           -- rolling: settle once per cycle
-- status CHECK gains 'accruing','voided','reversed'; source gains 'cycle_consumed'
```

`transfer_group = "lease-{id}-cycle-{N}"` for cycle rows; legacy rows keep
`"lease-{id}"` — FindTransferByGroup reconciliation exact for both.

## 4. The billing engine (one scanner, one clock)

T = current `rental_ends_at` (paid-through). Weekly anchor fixed at pickup.

| When | What |
|---|---|
| T−48h | Renewal notice to driver (claimed-once on the CYCLE row): "Renews {day} — $X charged to your saved card {day-1} at {time}. Return before then to stop." Compliance surface (auto-renewal laws), not garnish. |
| T−24h | Mint cycle N+1 (`scheduled`, period [T, T+7d]) and charge: create PI once (idem `cycle-{cycle_id}`), confirm off-session. Success → §4a. Decline → `retrying`. |
| T, T+24h, T+48h | Confirm-retries of the SAME PI (idem `cycle-{id}-confirm-{n}`), gated on the decline-code map (hard codes — stolen/fraud/invalid — skip straight to failed_final). Max 4 attempts total; within Visa/Stripe budgets. |
| T+24h failure | `delinquent_since` stamped on the lease; driver blocked from NEW bookings (continuation exempt-by-arrears-settled only); owner notified plainly ("Week N payment failed — the rental is ending unless it recovers; you can end it now"). |
| T+48h failure | `failed_final`; renewals halt (`renewal_halted_reason='delinquent'`). |
| T, T+72h | The EXISTING term scanner phases 2–3 fire on the lapsed paid-through (mode gate below): overdue copy variants by cause, support ticket at T+72h. Never auto-releases. |

**§4a — success is one transaction** (resolves the crash-loses-advance and
free-week findings): claim `charging→paid` (status-scoped) + advance
`rental_ends_at += 7d` (anchor arithmetic, never NOW()) + clear
`term_ending_notified_at`, `overdue_notified_at`, `overdue_escalated_at`,
`delinquent_since` — one COMMIT. The webhook 500s if any part fails (H2 rule)
and the redelivery re-runs the whole transaction.

**Arrears cannot stack, structurally:** cycle N+1 is minted only at
(paid-through − 24h), and paid-through advances only when the current cycle
pays. One unpaid cycle max, ever. No blocked_by_arrears state is needed.

**needs_action:** 72h TTL to `failed_final` via scanner; a LATE 3DS success
webhook may legally transition `failed_final → paid` (added transition,
resolves the race) and reinstates exactly as §4a.

**Every state's exit:** scheduled/charging/retrying → engine; needs_action →
driver rescue or TTL; failed_final → driver Pay-now (on-session, §5) /
return flow / T+72h ticket; arrears_due → driver pays / admin waives;
paid/waived/refunded/partially_refunded terminal.

**Recovery:** driver "Pay now" (on-session confirm of the same PI) at any
point before return → §4a runs, delinquency clears, notifications reset
per-cycle (fresh cycle rows = fresh stamps — the claimed-forever regression
the critics found cannot occur).

## 5. Money paths, per cent ($150/wk, fee_bps 1000)

**Weekly happy path**
```
T−24h  charge 15000¢ (off-session, consent-row amount)   → platform +14535¢ (Stripe ~465¢)
       owner_payouts row: status=accruing, kept=15000, fee=1500, owner=13500
T+7d   promotion scanner: accruing→pending, GUARDED:
         pickup_confirmed_at NOT NULL AND no live vehicle_return
         AND no open dispute on the cycle's charge AND refunded_cents=0
       payout sweep → Transfer 13500¢ (source_transaction = cycle's charge,
         group lease-{id}-cycle-{N}) → paid; both parties notified
```
Owner receives $135.00 every week, one week in arrears. Platform nets
1035¢/wk. Per-cycle fee flooring is authoritative (totals may differ from
term-level flooring by cents; remainder to the owner, house rule).

**Mid-cycle return (day 3 of week N)** — settlement AT RETURN COMPLETION:
- Cycle N is `accruing` → still on-platform. Refund pro-rata via
  ComputeReturnRefund AGAINST THE CYCLE (weeks=1, pickup:=period_start):
  8574¢ back to driver; row → `partially_refunded`; payout row rewritten
  kept=6426/fee=642/owner=5784, promoted immediately (lease over).
- Unconsumed overshoot cycle N+1 (charged at T−24h, period untouched):
  full 15000¢ refund (idem `cycle-refund-{cycle_id}`), row → `refunded`,
  payout row → `voided`. Refund fires at COMPLETION, never initiation —
  a cancelled/disputed return refunds nothing (resolves the free-week and
  stranded-money findings; a late-reconciled paid overshoot on a terminal
  lease is caught by a terminal-lease sweep that refunds any cycle whose
  period_start ≥ vehicle_returned_at).
- Stop-vs-return arbitrage: gone. "Stop auto-renew" only halts minting;
  ALL settlement happens through the return with the same two rules.
**Full-cycle return** → refund exactly 0 (the just-shipped fix), owner gets
the full 13500¢. **Pickup no-show** → cycle 1 fully refunded by the existing
2h machinery (money still on-platform — this is WHY arrears is load-bearing).

**Delinquent return (owes 3 unpaid days):** return is NEVER blocked on debt.
Cycle → `arrears_due` (pro-rata 6426¢); collection is ON-SESSION ONLY
(in-app Pay button; silent MIT of un-consented amounts is a card-network
violation — resolves the critics' consent finding). Unpaid 7d → support
ticket; admin waive = write-off. Owner's share settles from collected cents
only: if never collected, the owner eats the unpaid days (flagged to client
— the alternative is the platform underwriting owner income).

**Chargeback on week N (the honest tail):** each weekly charge has its own
~120-day dispute window → ~16–17 transferred charges disputable per lease at
steady state (~$2,160 owner share). Policy: `charge.dispute.created` →
withhold any UNPAID payout rows for that charge + halt renewals
(`renewal_halted_reason='dispute'`); NEVER reverse on open.
`charge.dispute.closed` lost → partial Transfer Reversal of exactly that
cycle's 13500¢ against its stored transfer id (negative connected balances
auto-repay from future cycle transfers; Stripe bank-debit as fallback;
platform worst-case absorption ~15465¢/lost dispute). Won → withheld rows
release and `renewal_halted_reason` clears (webhook actor — resolves the
vindicated-driver trap). These webhooks are BATCH-1 PREREQUISITES.

## 6. Term model on the existing machine (section d, adopted minus d.8)

- `rental_ends_at` = paid-through for rolling; NOT NULL always; advanced by
  §4a. weeks frozen at 1 (`CHECK (billing_mode='fixed_term' OR weeks=1)`).
- Lease columns (000053): billing_mode, renewal_stopped_at/by,
  delinquent_since, renewal_halted_reason
  (`CHECK IN ('delinquent','dispute','consent_revoked','return_initiated')` —
  every value has a setting AND clearing actor), continues_lease_id.
- Term scanner mode gate appended to phases 1–3:
  `(billing_mode='fixed_term' OR renewal_stopped_at IS NOT NULL OR
    delinquent_since IS NOT NULL OR renewal_halted_reason IS NOT NULL)` —
  a rolling lease in good standing never reaches T (charged at T−24h);
  one that stops/breaks becomes a fixed-term tail and reuses phases 1–3
  verbatim, with cause-specific copy. Existing fixed-term rows: gate's first
  disjunct true → truth table identical to today (proven per-predicate in
  the design's migration table).
- Driver card: "Renews {day} · $150.00/wk" + Stop-before-{cutoff}; stopped →
  today's countdown; delinquent → "Payment failed — pay now or return the
  car" (during T→T+24h the card must show the truth: "payment failed", not
  "renews {past date}" — server sends delinquency-in-progress explicitly).
  Owner card mirrors. Owner notification at charge success says
  "$135.00 for week N — transfers when the week completes {date}" (never
  "on the way" — resolves the false-notification finding).
- Driver stops: Stop auto-renew (claimed-once, cutoff = T−24h charge moment,
  UI shows exact timestamp) or just return. Owner stops: End rental
  (terminate-renewal; driver keeps every paid hour; return-by = paid-through,
  floor 24h ceiling ~8d). On stop/terminate, any in-flight PI for an
  unconsumed overshoot cycle is neutralized via the existing
  proven-neutralize helper; if it already succeeded, the overshoot-refund
  rule at return settles it (resolves the terminated-but-charged finding).
- Billing predicate carries the NOT EXISTS live-return shield (same as term
  scanner); `renewal_halted_reason='return_initiated'` set/cleared by the
  return flow's initiate/cancel.

## 7. Existing leases & conversion

- Prod today: ZERO active leases (CR-V completed Sep 7; Transit expired).
  Nothing migrates. Fenix re-books the CR-V as a ROLLING lease when the flag
  is on — that new booking IS the consent ceremony.
- Future fixed-term leases convert only via **continuation-as-new-lease**
  (immutable billing_mode): "Continue weekly" from T−72h → new rolling
  lease_request (continues_lease_id, one-live-continuation partial index) →
  owner accepts (may re-price) → driver pays week 1 with sfu=off_session →
  ONE transaction closes the predecessor (virtual return at boundary, full
  kept, refund 0 — the fixed rule) and pre-confirms the continuation.
  Payment-time guard RE-CHECKS the predecessor (paid, picked-up, unreturned,
  no live return) — resolves the 96h-window double-settlement interleaving.

## 8. Pricing periods (g) and R2O (f)

- **v1 bills weekly, period.** Daily listing → weekly cycle at the canonical
  weekly price (daily×7, already computed); monthly listing → weekly cycle
  at the canonical weekly (monthly/4 exactly — the pricing model's own
  invariant). Request screen: "$60/day · billed weekly at $420". 28-day
  cycles are v2 (every consent row, dunning bound, exposure figure, and
  anchor step is derived for 7d; the critics showed 28d silently breaks all
  four).
- **R2O = configuration of this engine**: bounded schedule (50 cycles minted
  upfront for the amortization display vs rolling's lazy one-ahead), a
  completion action at cycle 50, and an `r2o_agreements` table carrying the
  lawyer-shaped terms. Still needed on top (blockers, client-side):
  equity fraction per payment, early-buyout formula, late fees/grace,
  cycles-missed-to-default + repossession + accrued-equity disposition
  (state RTO statutes), title/lien/sales-tax mechanics, dealer-licensing
  question, insurance-lapse policy, down-payment handling, and the FEE BASE
  (10% of a payment that is part principal taxes the car's price — needs an
  explicit decision). ACH (us_bank_account with sfu) recommended for R2O
  (~$487 cheaper per completed agreement, fewer expiries) — note ACH needs
  Stripe mandates (unlike cards). Completion touches the disabled car-sales
  surface; R2O cannot ship while sales are off.

## 9. Transition safety & rollout

- Legacy path byte-identical: renewals never touch `payments`; webhook
  routes `metadata[kind]='cycle'` to the engine, everything else to the
  legacy handler; all new scanner predicates require billing_mode='rolling'
  (matches zero existing rows); owner_payouts legacy unique preserved via
  partial index.
- `ROLLING_RENTALS_ENABLED` env flag, default off; rolling offered on new
  bookings only; fixed-term stays available (client may later decide).
- Pre-launch rehearsal on Stripe test clocks: first charge → two renewals →
  mid-week return → dispute on week 2 → delinquency episode → recovery,
  killing the process inside each crash window; every idempotency key
  verified.
- iOS surface: consent screen + billed-weekly labels, Stop/End buttons with
  exact cutoff, dunning banners + Pay-now, card-update (SetupIntent sheet),
  owner weekly-earnings copy.

## 10. Build plan (on go)

1. **Batch 1 — dispute/refund webhooks + reversal ledger columns** (also
   closes audit finding M2 for the EXISTING product). ~1.5d.
2. **Batch 2 — migrations 000053–55, consent store, billing engine, arrears
   payouts, term-scanner gate, DB-gated tests per state/claim.** ~4d.
3. **Batch 3 — return-flow integration (pro-rata cycle + overshoot),
   dunning notifications, admin surfaces (cycles view, waive, dispute
   panel).** ~2d.
4. **Batch 4 — iOS.** ~2.5d.
5. **Batch 5 — test-clock E2E, flag-on, live pilot (Fenix re-booking).** ~1d.

~11 working days end to end, each batch independently deployable behind the
flag.

## 10. Kill switch (v98)

`ROLLING_RENTALS_ENABLED` is read once at boot; flipping it is a Fly
redeploy (`fly secrets set ROLLING_RENTALS_ENABLED=false -a drivebai-api-team`).

**Off stops:** rolling lease creation; bootstrap of week 1 into the cycle
ledger; renewal notices; minting and the first off-session charge; the
retry ladder; promotion of paid cycles into owner payouts.

**Off does not stop:** post-return pro-rata refunds; the needs-action TTL;
closing out returned leases (waive/arrears); the debt-ledger reconcile;
amendment expiry; reconciliation of a charge already in flight
(stuck-`charging`); the stale paid-through escalation below. Webhooks,
Pay-now, card update and return finalize keep working. A driver who returns
the car during an incident still gets their refund and closure.

**Safe-off duration:** `BillingMaxCatchUp` (14 days) minus the charge lead.
A live lease whose paid-through lapses further than that while the switch
is off falls below the floor in `ListRollingDueForBilling` and is never
billed again automatically. It is not silent: `billingStaleEscalationPhase`
(runs with the switch off) opens one payments ticket per such lease. Recovery
is a human decision — close the rental from the Rents page, or move
paid-through forward before turning the switch back on.

**Resumption after a halt** (`ClearRenewalHaltReporting`): a lease whose
paid-through already lapsed is re-anchored to `NOW() + BillingNoticeLead`,
so the driver gets the promised notice and the charge fires a day later —
never within sixty seconds of the halt lifting, never quoting a date already
gone. The lapsed days are not back-billed; they are put in front of a human
as an "unbilled rental days" ticket, because the consent says used days are
owed and forgiving them is a decision.

## 11. Recurring-only (decision 2026-09-17)

### Decision (Aziza, recorded in spirit)

Every new rental is recurring. It renews automatically at the listing's period until the
car is returned. The driver is not offered a "fixed-term or weekly" choice. Supported
charge intervals in this batch are **week** (existing) and **month** (engine generalised);
**daily is deliberately not built**. Owner payouts stay **in arrears, one row per billing cycle** (weekly cycles pay weekly, as today). Fixed-term **creation** ends now for rolling-eligible drivers and for
everyone when `RECURRING_ONLY` is turned on; code that **services** existing fixed-term
leases (return, refund, payout, disputes) stays until its deletion trigger (§11.6).

### Reasoning

- Two modes, one of them silent, is how 2026-09-14 happened: a request intended as weekly
  arrived as fixed-term with a 201 and no signal (see
  `docs/REPRO_ROLLING_LEASE_DRIVER_FLOW.md` §1c, reproduced by running). Removing the
  choice removes the class of bug, not one instance of it.
- Daily is refused for three reasons: a Stripe fee per charge on a small amount; a decline
  and dispute surface seven times larger per rental; and the consent text promises a notice
  before **every** charge — a per-day notice is not something we can keep while the mail
  provider is dead (the notice is push and in-app today, and drivers who deny push would
  get nothing).
- Owner payouts stay in arrears, one row per cycle: the payout ledger and the promotion sweep
  are cycle-denominated and verified. For weekly cycles that is weekly in arrears, as today.
  A monthly cycle would pay the owner once per 28 days — one of the reasons monthly ships off
  until the owner package says so (§11.6 checklist).

### What changed from the signed-off design (§9 "fixed-term stays available")

§9 said "rolling offered on new bookings only; fixed-term stays available (client may
later decide)". The client has now decided. Concretely:

| Was | Now |
|---|---|
| Driver chooses via a second CTA ("Rent weekly instead") | One primary CTA; the **server** sets the mode |
| `billing_mode` absent ⇒ `fixed_term`, silently | Rolling-eligible driver ⇒ `rolling` regardless of the field; explicit `fixed_term` from an eligible driver ⇒ `409 APP_UPDATE_REQUIRED` |
| Client build irrelevant to the server | `User-Agent: DriveBai/<build>` is logged and, below the first consent-sheet build (35), refused for an eligible driver |
| Interval hard-coded `weekly` at intent creation | Interval comes from the **listing's period**; week and month only; anything else refused at request creation |
| One fixed-term path for everyone not on the pilot | Same, until `RECURRING_ONLY=true`; then fixed-term creation is refused for everyone |

### The rule-12 constraint (no unreleased build required for a core flow)

Build 33 is the App Store build and has no consent sheet, no `billing_mode` field and no
`/config` call (`REPRO…` §1b). A recurring lease legally requires the consent ceremony at
checkout (§2 of this document), so **a build-33 driver cannot be given a recurring lease**.
Therefore:

- While `RECURRING_ONLY` is **off**: a non-eligible driver (not on the allowlist) still
  gets fixed-term; an eligible driver on a build below 35 is **refused with a message in
  the top-level `error.message`** (old builds render exactly that field —
  `ios/.../API/APIClient.swift:1439-1444` — and discard `details`). Being refused with an
  instruction is an exit; being silently downgraded is not.
- `RECURRING_ONLY` may be turned **on** only once the App Store build is ≥ 41. Until then
  it would strand every App Store driver at request time. This is the gate in §11.6.

### Interval model

| | week | month |
|---|---|---|
| Source | listing `rent_price_period='weekly'` | `rent_price_period='monthly'` |
| Cycle length | 7 days (`BillingCycleLength`) | 28 days (`BillingIntervalLength`, = 7 × `RentMonthWeeks`) |
| Charge amount | the consent's `amount_cents` = listing weekly (or the agreed offer) | the consent's `amount_cents` = listing monthly amount; an agreed weekly offer converts via `ConvertRentCents(weekly→monthly)` |
| Notice / charge lead | 48 h / 48 h | 72 h / 72 h |
| Retry ladder | 4 attempts, 24 h apart (unchanged) | same ladder |
| Consent text | `rolling-billing-v3` (signed, untouched) | `rolling-billing-monthly-v2` — new text because v1 promised retries "over the next two days" and the 72-hour lead makes that three |
| Proration on return | per-day over `DaysInPeriod(period_start, period_end)` (already period-derived) | same |
| Owner payout | weekly in arrears | **one payout row per cycle**, promoted when the cycle ends — for a 28-day cycle that is monthly in arrears. The owner package (v2) says "after each rental week"; this is one of the reasons monthly ships **off** (below). |

`daily` listings are refused at request creation with `INTERVAL_NOT_SUPPORTED` and a
plain message. There is one monthly listing in production today and it is not live.

**Monthly ships built but OFF** (`MONTHLY_RENTALS_ENABLED`, default false; review
2026-09-17). The engine, migration 000065, the cycle-1 bootstrap, the pickup anchor, both
catch-up floors, the notice lead and four Stripe test-clock rehearsal lines are done and
green. What is *not* done, and why the flag stays off until it is:

| Gap | Where | Needed before ON |
|---|---|---|
| Owner package describes weekly collection and a one-week cap | `models/terms.go` RollingOwnerTermsV2; `OwnerTermsSheet` | George signs a monthly owner package; `requireOwnerTermsForRollingAccept` refuses monthly until then (`OWNER_TERMS_MONTHLY_PENDING`) |
| Price-amendment disclosure is weekly-worded | `rolling_amendments.go` | monthly amendment package; refused `AMENDMENT_INTERVAL_UNSUPPORTED` until then |
| Engine notification copy says "week" | `billing_engine.go` Notify bodies; iOS `RollingBillingCard`; admin waive copy | one interval-aware pass |
| Listing shows the typed monthly amount, the charge is weekly×4 (≤2¢ drift on amounts not divisible by 4¢) | `IntervalAmountCents` | carry the typed cents onto the lease at INSERT |
| Return preview before cycle 1 is minted prorates over 7 days | `vehicle_return.go` preview | interval-aware preview |
| A parked monthly lease resumed after 14–56 days would be back-billed with a notice quoting a past date | `lease_request_repository.go` due-lister floor (56 d monthly) | re-anchor forward on resume instead of back-billing |
| Owner of a daily listing is never told eligible drivers cannot rent it | `lease_request.go` `INTERVAL_NOT_SUPPORTED` (WARN only) | one notification per listing + a period column in admin Vehicles (0 daily listings today) |
| A rolling lease refused at the pay step (client cannot prove build ≥ 35) tells the driver but not the owner why payment stalls | `lease_request.go` pay-step gate (WARN only; the accept TTL notifies both at 48 h / 72 h) | owner notice on the refusal |

With the flag off an eligible driver on a monthly listing is refused with a plain message
and the owner is WARN-logged; nothing is charged.

Note for George's sign-off: the **weekly** v3 text also says "over the next two days"
while the ladder's fourth attempt lands 24 h after period end. That wording is signed and
is not being edited (old versions are never edited); if it is to change it becomes v4 and
needs his sign-off alongside the recurring-only owner terms.

### Flags and their boot lines

| Flag | Default | Boot line |
|---|---|---|
| `ROLLING_RENTALS_ENABLED` | off | `"weekly rentals: ON" allowlisted_drivers=… open_to_everyone=…` |
| `ROLLING_ALLOWLIST_USER_IDS` | empty = everyone | same line |
| `RECURRING_ONLY` | **off** | `"recurring only: ON — fixed-term creation refused for everyone"` / `"recurring only: OFF — fixed-term still created for non-eligible drivers"` |
| `MONTHLY_RENTALS_ENABLED` | **off** | `"monthly rentals: ON …"` / `"monthly rentals: OFF — monthly listings are refused for recurring-eligible drivers (weekly only)"` |

`RECURRING_ONLY` is **forced off at boot** unless `ROLLING_RENTALS_ENABLED=true` with an
empty, well-formed allowlist — with a named pilot it would refuse every non-pilot driver on
the latest build (logged at Error: `"recurring only: REQUESTED BUT FORCED OFF …"`). The
boot also probes `SELECT billing_interval FROM lease_requests` and exits if the schema is
behind the binary. `GET /api/v1/admin/config` returns the switches and pilot sizes.

Refusal codes a driver can meet at request time: `APP_UPDATE_REQUIRED` (eligible, build
< 35 or explicit fixed-term, **only under a named pilot or RECURRING_ONLY** — with rolling
open to everyone and RECURRING_ONLY off, such a driver still gets the historical fixed-term
lease, WARN-logged), `RENTALS_PAUSED` (RECURRING_ONLY on, driver not eligible),
`INTERVAL_NOT_SUPPORTED` (daily, or monthly while the flag is off). At **pay** time a rolling
lease is refused `APP_UPDATE_REQUIRED` before any Stripe call unless the User-Agent proves a
build ≥ 35 — unknown is not proven capable. Under a named pilot the **owner** must be in it
too; otherwise the request falls to fixed-term with a WARN rather than stranding an owner at
the terms gate.

A fixed-term request created while any rolling is enabled is logged at WARN with the
driver id and User-Agent, so the 2026-09-14 shape is never silent again.

### 11.6 Deletion ledger

Classification of every fixed-term-related symbol. **DEAD NOW** is deleted in this batch.
**SERVICING** is kept until its trigger. **CREATION** is the server fallback kept behind
the flag.

| Symbol / surface | Class | Trigger to delete |
|---|---|---|
| iOS `requestWeeklyLease()`, "Rent weekly instead" CTA, `weeklyAvailability` tri-state, `loadWeeklyAvailability()`, retry row, `fetchAppConfig()` gating of the CTA (`DiscoverView.swift:777-780, 1063-1082, 1371-1400`) | DEAD NOW (build 41) | — |
| iOS `CreateLeaseRequestAPIRequest.billingMode` (request side) | DEAD NOW (build 41 sends none; server decides) | — |
| Backend `models.CreateLeaseRequestBody.BillingMode` | CREATION | RECURRING_ONLY on **and** App Store build ≥ 41 |
| Backend fixed-term default at `lease_request.go:349` and `lease_request_repository.go:74` | CREATION | same |
| `GET /api/v1/config` `rolling_rentals_enabled` | SERVICING (builds 35–40 read it) | App Store build ≥ 41 |
| `SettleRentalPayout` fixed-term payout, `computeRefundOverDays` fixed-term caller in `vehicle_return.go`, `payment_reconciliation.go` fixed-term branch, `lease_rental_term_repository.go` fixed-term scanner predicates (`:65, :149`) | SERVICING | RECURRING_ONLY on for 1 week **and** zero non-terminal `fixed_term` rows **and** last fixed-term charge > 120 days ago |
| iOS `LeaseRequestCardView` fixed-term rendering ("Pay Now", "per week — no further charges") | SERVICING | same |
| Admin `Rents.vue` fixed-term sections | SERVICING | same |
| `models.BillingModeFixedTerm` constant, DB `CHECK (billing_mode IN ('fixed_term','rolling'))` | SERVICING (rows exist) | same, plus a migration that leaves history readable |

The trigger query, run read-only against production:

```sql
select
  (select count(*) from lease_requests l
    where l.billing_mode='fixed_term'
      and l.status in ('requested','accepted','payment_pending','paid')
      and not exists (select 1 from vehicle_returns vr
                      where vr.lease_request_id=l.id and vr.status='completed')) as non_terminal_fixed_term,
  (select max(p.created_at)::date from payments p
    join lease_requests l on l.id=p.lease_request_id
    where l.billing_mode='fixed_term' and p.status='succeeded')      as last_fixed_term_charge,
  current_date - (select max(p.created_at)::date from payments p
    join lease_requests l on l.id=p.lease_request_id
    where l.billing_mode='fixed_term' and p.status='succeeded')      as days_since;
-- delete SERVICING when: non_terminal_fixed_term = 0 AND days_since > 120
-- AND RECURRING_ONLY has been on for >= 7 days (fly releases + secrets history).
```

On 2026-09-17 this returns 7 non-terminal rows and a last charge of 2026-09-14, so the
earliest SERVICING deletion date is 2027-01-12, and only if those 7 rows close.

### 11.7 What this batch actually deleted (2026-09-17)

Tooling: `go vet` clean; `staticcheck -checks U1000 ./...` on the backend; Swift by
reference-grep (no `periphery` installed); `vue-tsc --noEmit` clean.

| Deleted now | Proof it was dead |
|---|---|
| iOS `WeeklyAvailability` tri-state, `loadWeeklyAvailability()`, `requestWeeklyLease()`, the retry row and the "Rent weekly instead" button (`DiscoverView.swift`) | every reference was inside that one file; build 41 compiles and archives without them |
| iOS `CreateLeaseRequestAPIRequest.billingMode` (request side) | the only writer was `requestWeeklyLease()`; the server now ignores the field for eligible drivers |
| Go `PurchaseRequestRepository.updateStatus`, `joinComma` (`purchase_request_repository.go`) | `staticcheck` U1000: zero callers; not on a money path |

Money-related symbols found unused and **not** deleted: none reported by U1000. Note that
`models.BillingMaxCatchUp` now only feeds the stale-escalation log copy — the two listers
compute their floor per row from `billing_interval` — and stays for that copy.

Kept on purpose (SERVICING, see 11.6): `GET /api/v1/config` and `AppConfigAPIResponse`
(builds 35–40 read it; build 41 keeps it in `AppConfigStore`, refreshed on foreground and
account switch, gating nothing), every fixed-term rendering and settlement path, and the
`billing_mode` CHECK.
