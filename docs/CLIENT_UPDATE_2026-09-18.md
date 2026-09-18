# CLIENT UPDATE — George, 18 Sep 2026

Every figure below is labelled by where it came from: real money, or Stripe test mode.

## 1 · STATUS

- **The first real-money recurring rental is running right now.** Fenix, on George's CR-V, started today 11:09 UTC.
- $100.00 charged live, the weekly mandate is bound to his card, week 1 is paid, **first automatic renewal 25 Sep**.
- George signed the weekly owner terms in the app today, 11:14 UTC.
- Recurring is switched on for four pilot accounts; fixed-term is untouched for everyone else.
- Build 42 is uploaded and in use. Two backend releases shipped today: v106, then v107.

## 2 · SHIPPED TODAY

- **Server decides the billing mode**, not the app — pilot drivers get renewing weekly; everyone else gets fixed-term.
- **Listings tell the truth**: a car you can't rent says why, in the same words the refusal would use.
- **Owner terms required** before an owner can accept a weekly rental.
- **Monthly rentals**: built and tested, but **blocked on a monthly owner package** — see §7. Not a flag flip.
- **Build identity**: build 42 reports the exact code it was built from. Build numbers alone couldn't tell us what a driver was running — they'd been reused.

## 3 · RECURRING — EXACT STATUS

> **The full weekly recurring rental works, has been run end to end from a phone, and is now running with REAL money — Fenix's rental started today and the first automatic renewal is 25 Sep.**

- **Proved with real money so far:** request → accept → owner terms signed → price amendment → authorisation → $100.00 charge → handover → pickup → owner earnings accruing.
- **Proved in test mode only, not yet with real money:** the automatic renewal, the return, the pro-rata refund and the owner payout transfer. Those are what 25 Sep onward will prove.
- Every step in both runs was tapped through on a phone, not simulated.

## 4 · FOUND & FIXED

- **Fixed, deployed today (v107):** if Stripe's payment confirmation failed to reach us, a driver could pay and start a rental while the renewal authorisation was silently not armed — a week later the rental would stop and blame the driver's card, after the owner had been told it would keep renewing.
- Found **in testing, before any real driver met it**, because this was the first end-to-end run against real payment infrastructure. Reviewed twice; covered by a test verified to fail without the fix.
- **Verified working:** declined-card retry ladder then stop; unpaid-balance block with the exact amount, clearable in-app; held payouts released automatically once payout setup completes; fixed-term flow unchanged.
- **Known gaps, scheduled today, not hidden:** (1) the unpaid-balance check fails open if its database read errors; (2) the balance shown in-app and the balance enforced are computed differently in one edge case; (3) car purchases aren't covered by the unpaid-balance check at all. None reachable by the pilot today.

## 5 · MONEY

**Proven with real money**

- **Fenix's recurring rental, today:** **$100.00 charged → $10.00 platform fee (10%) → $90.00 accruing to George.** Mandate bound to his Visa; week 1 paid; first renewal 25 Sep.
- **The price amendment held:** $100.03 → $100.00 was recorded **one second before** the authorisation, and the authorisation, billing cycle, charge and payout all agree at **$100.00**.
- **George's earlier fixed-term rental:** $150 charged → $14.99 fee → $134.95 to George, paid out to his bank.

**Verified in Stripe test mode (local machine), stated at the production 10% fee**

- Pro-rata refund is exact: $300 charged for two weeks, returned day 8 → **$128.64 refunded**, $171.36 retained → **$17.13 fee, $154.23 to the owner**.
- Partial unpaid week bills only days used: **$85.68 for four days**, not the full $150 week.
- A driver who returned on time with a failed renewal charge owed **nothing** — the unused week was written off.
- Held payouts released in full the moment payout setup completed.
- The amount shown at authorisation matches the signed authorisation text exactly, on every device and locale.

## 6 · WHAT IS ACTUALLY LEFT

- **Nothing is blocking.** Build 42 is uploaded and George is running it; the owner terms are signed; the rental is live.
- **Build identity is proven in production:** the app now reports the exact code it is running, so we can tell a real report apart from a stale install. First time we have had this.
- **Next real milestone: the 25 Sep automatic renewal** — the first charge that happens with nobody watching.

## 7 · ASKS & DECISIONS

- ~~Sign the weekly owner terms~~ — **done today, 11:14 UTC.**
- ~~Who takes the first real recurring rental~~ — **settled: Fenix took it today.**
- **Sign a monthly owner package.** Monthly is blocked because the current guarantee covers one week, and a 28-day cycle is four weeks of exposure. A flag change does not open it; the signed package does.
- **Watch the 25 Sep renewal together?** It is the first charge nobody is watching. Worth agreeing now who checks it and what happens if it declines.
- **Widening the pilot** beyond four accounts — after the §4 balance gaps close.

---

## SPEAKING NOTES

- **Open:** "The first real-money recurring rental is running right now — Fenix, on your CR-V, since this morning. $100 charged, your $90 accruing, renews automatically on the 25th."
- **Be exact about what is proved:** everything up to and including the first charge is proved with real money. The automatic renewal, return, refund and payout are proved in test mode; 25 Sep proves them for real.
- **Your numbers:** $100.00 charged → $10.00 fee → $90.00 to you. Earlier fixed-term rental: $150 → $14.99 fee → $134.95, already paid out.
- **The price change held:** $100.03 → $100.00 recorded a second before he authorised, and every record agrees at $100.00.
- **If asked what broke:** "We deliberately broke the payment path. Found a case where a driver pays, the rental starts, but the renewal authorisation isn't armed — it'd stop a week later and blame their card. Found it before any real driver did. Fixed, reviewed twice, in production today, hours before Fenix's rental."
- **Don't quote test-mode fee splits** — that environment ran 5%, production is 10%.
- **Monthly:** not a switch. Needs a monthly owner package signed, because the guarantee covers one week and a monthly cycle is four.
- **The ask:** sign a monthly package if he wants monthly; agree who watches the 25 Sep renewal.
- **Don't promise:** a public launch date, monthly going live, or anything about App Store review.
