# Can we do DAILY rentals? — 18 Sep 2026

Answer prepared by running it, not by reading it. A daily rental was driven end
to end on a test machine with real payment plumbing: request → accept →
authorisation → first charge → automatic day-2 charge → automatic day-3 charge →
return → owner payout.

## Does it work?

**Yes, with changes.** Nothing about daily is impossible. It is roughly a week
of work, not an afternoon, and one part of it cannot be done in the backend
alone.

- **The billing engine can do it.** Charged $20.00 a day, automatically, three
  days running. Correct amounts, correct authorisation, correct payouts.
- **But "daily" was never built.** The system only knows two cadences, week and
  month, and that assumption is written into **ten separate places** — five in
  code, five inside database queries where the code cannot reach. Miss one and
  the failure is silent: in the first run the driver was charged for one day and
  the system recorded them as paid up for a **week**. Found and fixed during the
  run; that is the kind of thing that has to be hunted down one by one.
- **The app needs a change — this is the blocking one.** The authorisation
  screen only understands "weekly" and "monthly". Given "daily" it silently
  shows **"weekly"** and **"7 days"** — directly beneath the text saying you'll
  be charged every 24 hours. The driver would be agreeing to two different
  things on one screen. That cannot ship, and it cannot be fixed from the
  server.
- **Three things are still broken** even after the run: a failed payment retries
  over three days, so a daily rental is three days deep before it gives up; a
  driver who catches up after a failure gets more paid time than they paid for;
  and after any outage the system would try to bill every missed day in quick
  succession, which is how a card gets flagged for fraud.

## What it costs

Stripe charges **30¢ plus 2.9%** on every single charge. The 30¢ does not care
how small the charge is, so splitting a week into seven charges means paying it
seven times.

| | Per charge | Stripe takes | Our 10% fee | We keep |
|---|---|---|---|---|
| **Weekly** — $100/week | $100.00 | $3.20 | $10.00 | **$6.80** |
| **Daily** — $14.30/day | $14.30 | $0.71 | $1.43 | **$0.72** |

Over the same week of rental: weekly keeps **$6.80**, daily keeps **$5.01**.

> **Daily costs about a quarter of the margin — Stripe takes 50% of our fee
> instead of 32%.** On a cheaper car it gets worse, because the 30¢ is fixed.

A 30-day rental also produces **6× the charges, 6× the payout records, and
roughly 7× the payment notifications** — about two payment messages a day to the
driver.

## What it changes in the terms you've already signed

You signed the weekly owner package this morning. Two things in it do not
survive the move to daily.

**1. The unpaid-days guarantee.** The terms promise cover for **one week** of
unpaid rental. That cap was written against weekly billing, where a week of
exposure costs one missed charge. On a daily cycle a driver can run up the same
week of unpaid days while we have collected far less revenue from them — and the
current retry behaviour means a rental is already **three days** unpaid before
billing even stops. **The cap has to be re-decided for daily. It cannot simply
be inherited.**

Worth knowing: right now the system does **not** stop a daily rental from being
accepted under the weekly package. Monthly is explicitly blocked; daily is not.
In the test run a daily rental was accepted under your weekly terms without
anyone being asked.

**2. Daily owner payouts.** Your terms promise payment **after each rental week
completes**. Paying daily is a change to that sentence, not a setting.

- **Can the code do it?** Yes. It already creates one payout record per billing
  cycle, so daily cycles produce daily payouts automatically — the test run
  produced two payout records for two days.
- **Should it?** That is the question the terms have to answer, not the code.

## The three options

**A. Daily as it stands — not available.** The app would show "weekly" on the
authorisation screen for a daily rental. Not shippable at any price.

**B. Daily, built properly.** Requires: the ten cadence assumptions made
consistent, a new daily authorisation text (drafted, not legally reviewed), an
app update to the authorisation screen, a rethink of the retry behaviour, and a
revised owner package covering the unpaid-days cap and payout timing. Roughly a
week of engineering plus a legal pass, and it permanently costs a quarter of the
margin on every daily rental.

**C. Don't do daily.** Weekly already works and is running with real money
today. Short rentals can be served by a fixed-term booking, which is unchanged
and has none of these problems.

These are laid out so you can pick. I'm not recommending one.
