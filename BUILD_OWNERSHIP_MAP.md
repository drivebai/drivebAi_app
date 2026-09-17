# Who taps it, and what build they need

The rule we learned the hard way: **the build that matters is the one belonging to the
person whose finger is on the screen.** The weekly-rental CTA and the Buy CTA both live
in the listing detail page, which only the *driver* / *buyer* ever opens. The owner and
seller never see them, so their build is irrelevant to those steps.

Legend — 33 = App Store. 35–39 = TestFlight only. 40 = v102, archived 2026-09-16.

## Weekly (rolling) rental

| # | Step | Who taps | Build they need | Why |
|---|---|---|---|---|
| 1 | Sees the weekly option on a car | **Driver** | **35+** (use 40) | CTA is driver-side; gated on `GET /config` |
| 2 | Reads + accepts the consent screen | **Driver** | **35+** (use 40) | Consent sheet shipped in 35 |
| 3 | Sends the request with `billing_mode` | **Driver** | **35+** | Field absent before 35 |
| 4 | Accepts the request | **Owner** | **38+** | Owner-terms sheet shipped in 38 |
| 5 | Pays week 1 | **Driver** | **35+** | A rolling first payment shows the consent sheet before Stripe's sheet (`ChatView.swift:745-750`); 33 has no sheet |
| 6 | Sees the weekly billing card | **Driver** | **35+** | `RollingBillingCard` |
| 7 | Sees a debt balance if a week fails | **Driver** | **36+** | Debt card |
| 8 | Offers / accepts a price change | Owner / **Driver** | **36+** both | Amendment UI |

**Critical:** steps 1–3 are all the driver. George's build never mattered. That is the
entire reason Sunday's $50 came through fixed-term.

## Car sale

| # | Step | Who taps | Build they need | Why |
|---|---|---|---|---|
| 1 | Sees the Buy button | **Buyer** | 33 works | Shipped |
| 2 | Makes an offer | **Buyer** | 33 works | Shipped |
| 3 | Accepts the offer | **Seller** | 33 works | Title must be on file (backend gate) |
| 4 | Signs the Bill of Sale | **Seller** | **33 works as of v102** | Odometer is now optional; was blocking |
| 5 | Declares the odometer while signing | **Seller** | **40** | Optional; prints "NOT DECLARED BY SELLER" if skipped |
| 6 | Signs the Bill of Sale | **Buyer** | 33 works | Never gated |
| 7 | Hands over keys | **Seller** | **37+** | Hold-bounded handover picker |
| 8 | Inspection countdown + confirm | **Buyer** | **37+** | Countdown and confirm handover |

**Critical:** step 7 is the seller and needs 37+. Step 8 is the buyer and needs 37+.
Both sides need TestFlight for a sale to complete, even though the first six steps work
from the App Store build.

## Owner self-service (new in v102)

| Step | Who taps | Build |
|---|---|---|
| Sees "your car is held by a rental that never started" | **Owner** | **40** |
| Releases the car | **Owner** | **40** |
| Same, done for them | Support, via admin | any |

## Rows the first version missed (added 2026-09-17, from `docs/REPRO_ROLLING_LEASE_DRIVER_FLOW.md` §2)

| # | Step | Who taps | Build they need | Why |
|---|---|---|---|---|
| 9 | Receives the pre-charge notice | **Driver** | any (push + in-app) | `billingNoticePhase` uses `notifHandler.Notify`, not email |
| 10 | Pays an arrears balance ("Pay now") | **Driver** | **35+** | `RollingBillingCard.swift:615` |
| 11 | Hands over the keys ("I handed over the keys") | **Owner** | 33 works | `KeyHandoverModels.swift:80-83` |
| 12 | Confirms the keys ("I received the keys") | **Driver** | 33 works | `KeyHandoverModels.swift:84-85` |
| 13 | Starts a return ("Request to Return the Vehicle") | **Driver** | 33 works | `LeaseRequestCardView.swift:359` |
| 14 | Confirms the return ("Confirm return") | **Owner** | 33 works | `VehicleReturnModels.swift:210-215` |
| 15 | Completes Stripe Connect onboarding | **Owner** | any | Gates the owner's **payout**, not Accept (`payouts.go:455-517`) |

**Build 41 changes rows 1–3.** The driver no longer chooses; there is one CTA and the
server decides the mode. Rows 1–3 collapse to "opens the listing and taps the primary
button — any build ≥ 35 that shows the consent sheet at checkout". Build 33 drivers are
refused with an update message once they are rolling-eligible.
