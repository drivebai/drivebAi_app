# CLIENT UPDATE — George, 18 Sep 2026

Every figure below is labelled by where it came from: real money, or Stripe test mode.

## 1 · STATUS

- Recurring (weekly) rentals are **deployed to production** and **switched on for four pilot accounts**.
- **No recurring rental has ever been created with real money.** Production holds zero recurring leases, zero authorisations, zero billing cycles.
- Two backend releases shipped today: v106, then v107 an hour later.
- Build 42 is archived and ready to upload to TestFlight.
- Fixed-term rentals are untouched for everyone outside the pilot.

## 2 · SHIPPED TODAY

- **Server decides the billing mode**, not the app — pilot drivers get renewing weekly; everyone else gets fixed-term.
- **Listings tell the truth**: a car you can't rent says why, in the same words the refusal would use.
- **Owner terms required** before an owner can accept a weekly rental.
- **Monthly rentals**: built and tested, but **blocked on a monthly owner package** — see §7. Not a flag flip.
- **Build identity**: build 42 reports the exact code it was built from. Build numbers alone couldn't tell us what a driver was running — they'd been reused.

## 3 · RECURRING — EXACT STATUS

> **The full weekly recurring rental works and has been run end to end, in Stripe TEST mode, from a phone — and the first REAL-money recurring rental needs build 42 in TestFlight and George's signature on the owner terms.**

- Every step was tapped through on a phone: request → accept → owner terms → authorisation → payment → handover → pickup → weekly renewal → return → pro-rata refund → owner payout.
- No engineering work remains between here and the first real recurring rental.

## 4 · FOUND & FIXED

- **Fixed, deployed today (v107):** if Stripe's payment confirmation failed to reach us, a driver could pay and start a rental while the renewal authorisation was silently not armed — a week later the rental would stop and blame the driver's card, after the owner had been told it would keep renewing.
- Found **in testing, before any real driver met it**, because this was the first end-to-end run against real payment infrastructure. Reviewed twice; covered by a test verified to fail without the fix.
- **Verified working:** declined-card retry ladder then stop; unpaid-balance block with the exact amount, clearable in-app; held payouts released automatically once payout setup completes; fixed-term flow unchanged.
- **Known gaps, scheduled today, not hidden:** (1) the unpaid-balance check fails open if its database read errors; (2) the balance shown in-app and the balance enforced are computed differently in one edge case; (3) car purchases aren't covered by the unpaid-balance check at all. None reachable by the pilot today.

## 5 · MONEY

**Proven with real money**

- George's completed fixed-term rental: **$150 charged → $14.99 platform fee → $134.95 to George**, paid out to his bank.
- The price-amendment ceremony, run end to end on a real booking.

**Verified in Stripe test mode (local machine)**

- Pro-rata refund is exact: $300 charged for two weeks, returned on day 8 → **$128.64 refunded**, $171.36 retained.
- Partial unpaid week bills only days used: **$85.68 for four days**, not the full $150 week.
- A driver who returned on time with a failed renewal charge owed **nothing** — the unused week was written off.
- Held payouts released in full the moment payout setup completed.
- The amount shown at authorisation now matches the signed authorisation text exactly, on every device and locale.

> Fee splits from the test run are **not** quoted above: that environment ran a 5% fee, production is 10%. The charge and refund figures are fee-independent, so they stand as observed.

## 6 · BLOCKING THE FIRST REAL-MONEY RECURRING RENTAL

- **Build 42 into TestFlight** — ready now.
- **George's signature on the weekly owner terms**, in the app, on the account owning the car.

## 7 · ASKS & DECISIONS

- **Sign the weekly owner terms.** Blocks the first real recurring rental.
- **Sign a monthly owner package.** Monthly is blocked because the current guarantee covers one week, and a 28-day cycle is four weeks of exposure. A flag change does not open it; the signed package does.
- **Who takes the first real recurring rental?** A choice, not a recommendation: since George is an active owner with real cars, it could run **between George's account and Aziza's** for a small amount, as soon as he signs and build 42 is up.
  - Removes: dependency on another person's phone, and on an unknown app build.
  - Does not remove: the signature, or the upload. Both required either way.
  - Alternative: wait for Fenix — fine, but adds a reinstall and an unknown build.
- **Widening the pilot** beyond four accounts — after the §4 balance gaps close.

---

## SPEAKING NOTES

- **Open:** "Recurring rentals are deployed to production and on for four pilot accounts. The full weekly recurring rental works and has been run end to end, in Stripe TEST mode, from a phone — and the first REAL-money recurring rental needs build 42 in TestFlight and George's signature on the owner terms."
- **Be exact:** deployed and switched on, but no recurring rental has run with real money yet.
- **If asked "is it finished":** yes for the flow — every step tapped on a phone, not simulated.
- **If asked what broke:** "We deliberately broke the payment path. Found a case where a driver pays, the rental starts, but the renewal authorisation isn't armed — it'd stop a week later and blame their card. Found it before any real driver did. Fixed, reviewed twice, in production today."
- **Real-money numbers:** your rental — $150 charged, $14.99 fee, $134.95 to you.
- **Don't quote test-mode fee splits** — that environment ran 5%, production is 10%.
- **Monthly:** not a switch. Needs a monthly owner package signed, because the guarantee covers one week and a monthly cycle is four.
- **The ask:** sign the weekly terms; decide first-rental on your car with Aziza driving, or wait for Fenix.
- **Don't promise:** a public launch date, monthly going live, or anything about App Store review.
