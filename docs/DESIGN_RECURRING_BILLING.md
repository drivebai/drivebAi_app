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
