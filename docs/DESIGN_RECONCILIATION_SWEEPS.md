# Reconciliation sweeps need a cutoff

*Written 2026-09-10, after a sweep tried to pay out $25,000 against a charge
that does not exist.*

## The rule

**Any sweep that heals, expires, or settles rows by STATE must have a lower
bound on how far back it looks.**

A sweep selects rows by the state they are in. State is timeless: a row that
entered a state in July looks exactly like one that entered it a minute ago.
So the day a sweep ships — or the day an existing sweep gains a new state to
act on — it does not begin operating on new rows. It begins operating on
**every row that has ever been in that state**, including:

- rows created before the feature existed, which were never eligible for the
  action the sweep performs;
- test-era artifacts, and rows whose external counterpart lives on a Stripe
  account, provider or environment we no longer use;
- rows deliberately left in a state by a human, which the sweep reads as a
  fault to be repaired.

The mental model that causes the bug is "this sweep fixes things that are
broken". The accurate one is **"this sweep acts on everything matching a
pattern, and I have only reasoned about the recent examples."**

## The question to ask of every sweep

> If this looked back across all of production history right now, what would
> it find, and what would it do to it?

Not "what is it for". Not "what will it catch tomorrow". If the answer to that
question includes a single row you would not want touched, the sweep needs a
bound.

## Acceptable bounds

In rough order of preference:

1. **A feature-live constant.** `models.SellerPayoutsLiveFrom` — an explicit
   instant before which the action was not possible. Self-documenting, and it
   states the reasoning in the type system rather than a comment.
2. **A claim stamp on the row.** The sweep can only act on rows something
   recent wrote, so history is structurally invisible to it.
3. **A grace window with a floor** — "older than 2 minutes, younger than 30
   days" rather than only "older than 2 minutes".
4. **A state only reachable recently**, because a migration created it. Weaker
   than it sounds: backfills reach into history by design.

An unbounded sweep is acceptable only where the action is provably harmless on
every historical row, and that argument belongs in a comment on the query.

## Classifying a sweep: three answers, not two

Every sweep is **safe by design**, **safe by timing**, or **unsafe**.

The middle class is the dangerous one, because it looks like the first. A
sweep is safe by timing when nothing in its own code prevents it reaching into
history — it is safe only because the history does not exist yet.

**A safe-by-timing classification is incomplete without the triggering event:
the thing that would make it unsafe.** Record it next to the classification,
in the code, at the query.

| Sweep | Triggering event | Plausible? |
|---|---|---|
| debt reconcile | the day rolling billing gains history | yes — it is the next flag we turn on |
| any sweep keyed to a provider object | the day we migrate provider or account | yes — it has already happened once |
| any sweep over a table someone may backfill | the day a backfill lands | yes — backfills reach into history by design |

The test for whether to bound it now rather than record it:

> Is the triggering event **plausible**, or **hypothetical**?

If plausible, bound it now. A classification survives only as long as someone
remembers to revisit it, and the events above all arrive months later, in
someone else's ticket, with no reason for anyone to reread this file.

The debt reconcile is the worked example. Its trigger — "rolling billing gains
history" — was knowable in advance and imminent, so it was bounded rather than
merely classified. That is the standard: **note the trigger, and if you can
name the week it might happen, bound it instead.**

## The two examples

### The sale reconcile: unsafe, and it fired

`ListCompletedSalesWithoutPayout` selected completed purchases with no payout
row. Correct for its purpose: a crash between capture and the ledger write
leaves exactly that shape, and without healing it the seller is never paid.

Deployed at 09:15. By 09:16:54 it had found five car sales completed in **July
2026** and begun settling them, once a minute, including one for $25,000.
Those rows are from the previous Stripe account; their charges return
`resource_missing` on the live key, so the platform never received the money.
It created five payout rows totalling $26,811 in owner share.

No money moved, for one reason only: none of those sellers had completed
Connect onboarding, so every row parked as `awaiting_onboarding`. One of them
named a real account that is **mid-onboarding**. Had that seller finished, the
payout sweep would have transferred **$22,500 of platform funds** against a
charge that does not exist.

Fixed by refusing to look before `SellerPayoutsLiveFrom`. The five rows were
voided with a note recording why.

### The debt reconcile: safe only by timing

`ListArrearsCyclesWithoutDebt` has the identical shape — arrears cycles with
no ledger row, no lower bound at all. It is safe today for one reason:
**rolling billing has never run in production**, so not a single `arrears_due`
row exists to look back at.

That is an accident of sequencing, not a property of the design. The moment
rolling billing has history, this sweep acquires the same hazard the sale
reconcile had, and nothing in the code would warn us. It needs the same bound
before the rolling flag is turned on.

## What this costs when ignored

The sale sweep was written *in response to* a review finding about money going
missing. It was tested, it passed, and it did the right thing for the case it
was written for. The failure was not in the logic — it was in never asking
what the logic would do to rows nobody was thinking about.

Reconciliation is the one class of code that is *designed* to act on data
nobody is watching. That is exactly why it needs the tightest bound.
