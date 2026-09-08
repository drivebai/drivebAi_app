# Rent-to-Own — Design (2026-09-08, design only, nothing built)

The client's framing: an owner lists a car both ways — e.g. $500/mo to rent,
$1,000/mo to own after a set period — and is unsure whether the schedule's
total should exceed the cash price, and by how much.

## 0. The structural decision that everything else hangs on

Two lawful shapes exist. The choice between them is the single biggest thing
counsel must ratify, because it decides which body of law applies:

**A. Rental + purchase option ("rent credit" model) — RECOMMENDED.**
The driver rents at the elevated rate under the existing rolling engine. A
defined slice of each collected payment accrues as *purchase credit*. At any
time (or at schedule end) the driver may exercise the option: the accrued
credit counts against the posted purchase price, and the remainder — often $0
at schedule end — settles through the EXISTING purchase/bill-of-sale flow.
Walk away any time = a normal rolling return; the rental part was real
consideration for real use. This is the rent-to-own shape state RPO
(rental-purchase) statutes contemplate. It composes almost entirely out of
machinery we already run.

**B. Installment/conditional sale.** Title passes at the end of a fixed
payment schedule; missed payments trigger repossession of a car the driver is
*buying*. This drags in retail-installment-sales acts, Truth-in-Lending-style
disclosure duties, usury ceilings on the implied interest rate, and
repossession law. It is a lender's compliance surface, not a marketplace's.

Recommendation: **A**, and the rest of this document designs A. B remains on
the table only if counsel says New York treats A as a disguised B anyway (see
lawyer list) — in which case the honest answer is that DriveBai should not
build RTO until it is willing to run a consumer-credit program.

## 1. Economics — what the owner sets, what everyone sees

The owner sets exactly three numbers on the listing:

1. **Cash price** — what the car sells for outright today. This is the
   anchor; it already exists as the sale price field.
2. **RTO monthly amount** — what the driver pays per month on the RTO track.
3. **Term** — number of monthly payments to full credit.

Everything else is *derived and displayed*, never typed:

- **Total if completed** = monthly × term.
- **Rental component** = the car's plain rental rate × term (the same car's
  posted rent price — both tracks exist on one listing by the client's own
  framing).
- **Credit component** = total − rental component, accrued pro-rata per
  collected payment (this is the per-payment credit slice: e.g. $1,000/mo
  with $500/mo plain rent ⇒ $500/mo credit).
- **The multiple**: "Total if completed: $12,000 — 2.4× the $5,000 cash
  price" — shown to BOTH parties, on the listing and again on the consent
  screen, in the same type size as the price. The client's instinct is
  right and we should go further than "display": the acceptance disclosure
  must contain the sentence, because it is the sentence a regulator or
  journalist would ask about. Should the total exceed cash price? Yes,
  necessarily — the owner finances the purchase, carries use-wear on a car
  they may get back, and eats default risk; market RTO premiums run
  1.5×–2.5×. The app should not CAP the multiple (that is the owner's
  business) but should (a) show it everywhere, (b) warn the owner above
  ~2.5× that the listing will display an outlier premium, and (c) refuse
  only the degenerate configs (total < cash price after credit accounting,
  zero-credit schedules marketed as RTO).

Derivation choice argued: letting the owner type the monthly amount and term
(deriving the total) beats typing a purchase price and deriving payments —
owners think in monthly cash flow, and a derived *total* is a natural place
to hang the comparison display. The cash price stays an independent field so
the multiple is honest rather than circular.

## 2. Discovery — on the listing

Agreed with the client's call, and one sharpening: RTO terms belong on the
listing *as structured fields*, not prose — `rto_enabled`,
`rto_monthly_cents`, `rto_term_months`, derived total + multiple — because
structured terms are comparable across cars, filterable ("rent-to-own only"),
and immune to prose games. Chat negotiates away from the anchor; if a
negotiation lands on different numbers, the REQUEST records the negotiated
terms and the same derived display is re-rendered at request time (the
consent evidence must show the numbers actually agreed, not the listing's).
Hidden-terms-until-chat produces shotgunned requests and un-comparable
offers; posted terms are also the predatory-optics defense — nothing was
sprung on anyone.

## 3. Engine mapping — configuration vs genuinely new

**Configuration of what exists (the majority):**
- The recurring charge IS the rolling engine on the monthly interval — which
  makes the monthly-bounds batch (see the amendments checkpoint) a hard
  prerequisite, not an option.
- Consent/disclosure: the v2 consent ceremony extended with the RTO
  sentences (total, multiple, credit slice, forfeiture terms). New terms
  version, same recording machinery.
- Failed payments: the existing ladder, delinquency, arrears, return flow.
  A missed RTO payment is FIRST a missed rent payment — car comes back
  through the machinery already proven.
- Completion handoff: at full credit, auto-create a purchase request at
  price = remainder (usually $0; any balloon amount if counsel wants one),
  flowing into the EXISTING purchase + `billofsale` package for the bill of
  sale and title ceremony. `DISABLE_CAR_SALES` must be lifted first — which
  ties RTO's ship date to the seller-payout batch (M1) that flag has been
  waiting on. That ordering is a feature: RTO cannot ship before sales work.

**Genuinely new (the build cost):**
1. `rto_agreements` — the option contract record: parties, cash price,
   monthly, term, credit slice, terms version, state machine
   (active/completed/forfeited/cancelled/converted).
2. `rto_credit_ledger` — one row per collected cycle: payment ref, credit
   slice accrued, running total. Append-only; the fairness options below
   read from it.
3. Schedule progress surface (driver: "14 of 24 payments; $7,000 credited"),
   owner mirror, admin drawer.
4. The completion/forfeiture state machine and its notifications.
5. Listing fields + discovery UI + request-time term capture.
6. Fee plumbing per §5.

Estimate: monthly-bounds batch first, then RTO is roughly two Batch-3-sized
efforts (ledger+agreement machinery; completion/forfeiture+surfaces), plus
the legal review loop.

## 4. Fairness and failure — the options for counsel

Missed payment at payment 40 of 50, with ~$20,000 paid. The four shapes:

| Option | Driver outcome | Owner outcome | Notes |
|---|---|---|---|
| **Forfeit-all** (classic RTO) | Loses all credit; car returns | Keeps car + all payments | Lawful in many states *for true rental-purchase*; the optics the client fears. NY scrutiny likely. |
| **Cure period** (reinstatement) | N days to pay and continue; forfeits only after | Delay, then as above | RPO statutes commonly MANDATE reinstatement rights; 14–21 days is typical. This is the floor, not a choice. |
| **Credit survival** | Credit converts to a voucher against any future purchase (this car or another) on the platform, expiring in e.g. 12 months | Keeps payments; platform carries the voucher liability | Middle path; defensible, cheap to build on the ledger. |
| **Equity refund** | Some % of accrued credit refunded on return | Owner keeps rental component only | Consumer-friendliest; some jurisdictions require something like it for large paid-in shares. Hardest for owner economics. |

Recommendation to bring to counsel: **cure period (mandatory floor) +
credit survival**, with equity refund as the fallback if NY's
rental-purchase framework demands it. Forfeit-all should be off the table
regardless of legality — a driver 80% through a schedule losing everything
is the headline that ends the product.

Also for the same conversation: early buyout must exist (credit + a defined
remainder formula at any time), because a schedule you can only complete by
riding it to the end re-creates installment-sale optics.

## 5. The fee

10% of a $1,000 RTO payment where $500 is purchase credit is a 10% surcharge
on the car's principal — the platform would be taking $1,200 over a 24-month
schedule of which $600 is fee-on-principal. Options:

| Model | Revenue (example: $1,000/mo × 24, $500 credit slice, $5,000 cash price) | Character |
|---|---|---|
| 10% on full payment (status quo) | $2,400 | Simple; surcharges principal; the one to avoid |
| **10% on rental component only** | $1,200 | Fee on the service we actually provide; clean story |
| Flat origination + rental-component fee | $250 + $1,200 | Front-loads platform revenue; origination fees smell like lending |
| **Completion fee on the sale leg** | $1,200 + sale-side fee at title transfer (rides the existing sale fee when sales re-enable) | Aligns platform with completions, not defaults |

Recommendation: **rental-component fee + the normal sale fee at completion**.
The platform then earns nothing from the credit slice and more when schedules
finish — the incentive alignment a regulator would want to see, and the
number the client can defend in one sentence.

## 6. What the lawyer must decide (the short list)

1. Does New York's rental-purchase / RPO framework govern shape A, and what
   disclosures does it mandate (total-cost statements, reinstatement rights,
   itemized statements)?
2. Is there a paid-in threshold beyond which forfeiture is restricted or an
   equity interest attaches — i.e. is credit survival enough, or is equity
   refund mandatory?
3. Cure/reinstatement period: required length and notice form.
4. Does shape A risk recharacterization as an installment sale (usury/TILA
   exposure on the implied rate), and does an early-buyout right cut for or
   against that?
5. Title, registration, insurance, and lien state during the schedule —
   owner holds title throughout in shape A; whose policy, and what does TLC
   licensing require of the titleholder?
6. Fee legality: any constraint on platform fees touching the credit slice.
7. The forfeiture notice ritual: what must be sent, when, and how recorded.
8. Repossession mechanics if the driver neither pays nor returns — same as
   today's delinquent-rental answer, or stricter because purchase credit is
   involved?

## 7. What this unlocks and in what order

Monthly bounds batch → sales re-enable (M1 seller payouts) → RTO ledger +
agreement machinery → completion/forfeiture + surfaces → counsel sign-off
woven through. Nothing here starts until the client has the lawyer's answers
to §6, and the flag discipline applies to every stage.
