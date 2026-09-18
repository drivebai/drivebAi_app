# CLIENT UPDATE — George, 18 Sep 2026

Prepared for the call on 18 Sep. Everything below is verified against the live
system or a recorded test run; nothing here is projected.

---

## 1 · WHERE WE ARE

Recurring (weekly) rentals are **built, tested end to end, and running in
production**. Two backend releases shipped today: **v106** and, an hour later, **v107**. The app side is archived as **build 42** and is ready to upload to
TestFlight.

The platform still creates fixed-term rentals for anyone outside the pilot, so
nothing that worked before has been taken away. Recurring is switched on for a
named pilot list only — four accounts — and is off for everyone else.

## 2 · WHAT SHIPPED SINCE THE LAST CALL

| | |
|---|---|
| **Recurring rentals** | The server decides the billing mode, not the app. A driver on the pilot list gets a renewing weekly rental; everyone else gets the old fixed-term flow. |
| **Listing honesty** | A car a given driver cannot actually rent now says why on the listing, in the same words the server would use to refuse the booking. No more buttons whose only outcome is an error. |
| **Owner terms** | Accepting a weekly rental requires the owner to have read and accepted the weekly-rental terms in the app. |
| **Monthly rentals** | Built and tested, but **switched off**. It is one flag away when we want it. |
| **Build identity** | Build 42 reports the exact code it was built from. Until now a build number alone could not tell us what a driver was running — numbers had been reused across builds. |

## 3 · RECURRING RENTALS — EXACT STATUS

Stated precisely, because the distinction matters:

> **The full weekly recurring rental works and has been run end to end, in
> Stripe TEST mode, from a phone.** Request → owner accepts → owner signs the
> weekly terms → driver reads and authorises the recurring charge → payment →
> key handover → pickup → weekly renewal → return → pro-rata refund → owner
> payout. Every step was performed by tapping through the app, not simulated.
>
> **The first REAL-money recurring rental needs two things: build 42 in
> TestFlight, and George's signature on the owner terms.**

That is the whole gap. There is no remaining engineering work between here and
the first real recurring rental.

## 4 · WHAT WE FOUND, AND WHAT WE FIXED

We ran the failure cases deliberately — declined cards, interrupted payments,
drivers who owe money, owners who have not finished payout setup — rather than
only the happy path.

**Fixed and live today.** The most serious finding: if the payment confirmation
from Stripe failed to reach us, a driver could pay successfully and the rental
would start, but the renewal authorisation would silently not be armed. A week
later the rental would stop renewing and the app would tell the driver we could
not charge their card — when the card was fine and they had done nothing wrong.
Meanwhile the owner had been told the rental would keep renewing.

We found this **in testing, before any real driver met it**, because this was
the first time the whole flow was run end to end against real payment
infrastructure. It was fixed, reviewed twice, covered by a test that was
verified to fail without the fix, and **deployed to production today as v107**.
No customer was ever exposed to it: production has not yet processed a single
recurring rental.

**Verified working.** Declined-card handling retries on a defined schedule and
then stops billing and ends the rental; a driver who genuinely owes money is
refused a new rental with the exact amount named, and can clear it in the app;
an owner who has not finished payout setup has their earnings held and released
automatically the moment they finish; and the old fixed-term flow is unchanged,
down to the cent.

**Known gaps, scheduled for today, not hidden.** Three items we found and chose
not to rush between releases:

1. If the database read behind the unpaid-balance check fails, the booking is
   allowed rather than blocked. It fails open, not closed.
2. The balance shown in the app and the balance the block enforces are computed
   slightly differently, so in one edge case the app can say "you cannot rent"
   while the server would allow it.
3. Car purchases are not covered by the unpaid-balance check at all — only
   rentals are.

None is reachable by the pilot today. All three are queued for today's work.

## 5 · THE MONEY, PROVEN

From the recorded test run, exact figures:

- A two-week rental at $150/week charged **$300.00**. Returned after 8 days:
  driver refunded **$128.64**, owner kept **$171.36**, platform fee **$8.56**
  (5%), owner received **$162.80**. The three add back to the charge exactly.
- When a card stopped working mid-rental, the driver was billed only for the
  days actually used — **$85.68** for four days, not the full $150 week — and
  that was collectable in the app.
- In a separate run where the driver returned the car on time and the renewal
  charge had failed, the unpaid week was **written off entirely**: they had used
  none of it, so they owed nothing.
- Owner earnings held while payout setup was incomplete were released
  automatically and transferred in full once setup finished.

Every amount the driver sees at the moment of authorisation is now rendered
identically to the amount recorded in the signed authorisation text, on every
device and in every locale.

## 6 · WHAT IS BLOCKING THE FIRST REAL-MONEY RECURRING RENTAL

Exactly two things, both outside the code:

1. **Build 42 into TestFlight.** Ready to upload; Aziza can do it today.
2. **George's signature on the owner terms**, in the app, on the account that
   owns the car being rented.

## 7 · DECISIONS FOR GEORGE

**a. Who takes the first real recurring rental?**

One option, offered as a choice rather than a recommendation: because George is
himself an active owner with real cars on the platform, the first real-money
recurring rental could be run **between George's account and Aziza's**, for a
small amount, as soon as he signs the owner terms and build 42 is uploaded.

- **What that removes:** the dependency on another person's phone, and on an
  unknown app build. We control both ends and can see every row.
- **What it does not remove:** George still has to sign the owner terms, and
  build 42 still has to be uploaded. Those two are required either way.

The alternative is to wait for Fenix, which is also fine — it just adds a
dependency on him reinstalling and on us confirming which build he ends up
running.

**b. Monthly rentals.** Built and tested, currently off. Turn on now, or hold
until weekly has run for real?

**c. Widening the pilot.** Recurring is limited to four named accounts. Widening
is a flag change, but the unpaid-balance gaps in §4 should close first.

---

## SPEAKING NOTES

**Opening line.** "Recurring rentals are live in production as of today. The
full weekly recurring rental works and has been run end to end, in Stripe test
mode, from a phone — and the first real-money recurring rental needs build 42 in
TestFlight and your signature on the owner terms. That's the whole gap."

**If he asks whether it is really finished.** Say yes for the flow, and be
specific about what "tested" means: every step tapped through on a phone, not
simulated — request, accept, terms, authorisation, payment, handover, pickup,
renewal, return, refund, payout.

**If he asks what went wrong.** Lead with the fact it was found in testing and
is already fixed and deployed. "We deliberately broke the payment path to see
what happens. We found a case where a driver pays, the rental starts, but the
renewal authorisation is silently not armed — the rental would stop a week later
and blame the driver's card. We found it before any real driver did, fixed it,
reviewed it twice, and it went to production today."

**Do not oversell.** Three known gaps in the unpaid-balance handling are listed
in §4 and are being closed today. If he asks, name them plainly — none is
reachable by the pilot right now.

**The ask.** Two things: sign the owner terms in the app, and decide whether the
first real recurring rental runs on his own car with Aziza as the driver, or
waits for Fenix.

**Numbers to have ready.** $300 charged → $128.64 refunded → $162.80 to the
owner → $8.56 fee. Four days used out of a week → $85.68, not $150.

**Do not promise** a public launch date, monthly rentals being switched on, or
anything about the App Store review of build 33.
