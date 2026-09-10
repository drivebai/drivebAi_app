# Draft texts — owner guarantee + driver debt (NOT MINTED)

Status: drafts for approval. Nothing in `internal/models/terms.go` has been changed.
`TermsVersionRolling` (v1), `TermsVersionRollingV2`, `RollingDriverDisclosure`,
`RollingDriverDisclosureV2` and `RollingOwnerTermsSentence` stay byte-identical.

## Finding that changes the rationale (not the instruction)

The sentence "rental days that are never successfully collected are borne by the
owner" is in `RollingOwnerTermsSentence` — an OWNER-facing constant referenced
nowhere but its own declaration. It is not in the driver disclosure, is written to
no database row, and no owner terms page exists. Its code comment claims it is
"recorded on every consent"; that comment is false. Production has 0 consent rows.

So: the OWNER text changes because the guarantee makes it false.
The DRIVER text changes because of the debt balance and the deletion truth —
not because of the guarantee. The driver's obligations are unchanged.

---

## 1. Owner terms — new version `owner-rolling-v2 (2026-09-10)`

> **How you get paid.** DriveBai collects the weekly rent from the driver's saved
> card and pays your share after each rental week completes. Your share is that
> week's rent less the DriveBai fee.
>
> **If we can't collect.** If a weekly payment cannot be collected from the driver
> after all retries, DriveBai will pay you for up to one week of that unpaid rent,
> at your normal share, once the rental has been closed out on DriveBai. A rental
> is closed out when the driver returns the car through the app and the return is
> completed, or when DriveBai support records a settlement that ends the rental —
> including where the car was not recovered. Nothing is paid before the rental is
> closed out, and never more than one week per rental, however long the unpaid
> period ran.
>
> This payment takes the place of the driver's payment; it is not in addition to
> it. If DriveBai later recovers the money from the driver, DriveBai keeps that
> recovery and you keep the payment already made to you.
>
> **What this does not cover.** Unpaid time beyond that one week is not covered.
> The vehicle itself is not covered. DriveBai does not insure your car, does not
> guarantee its return, and this payment is not an insurance policy. If a driver
> does not return your car, DriveBai will pursue the driver and support you, but
> recovering the vehicle is a matter for you, your insurer and law enforcement.

Old text preserved verbatim as `RollingOwnerTermsSentence` (unversioned, v1).

---

## 2. Driver consent — new version `rolling-billing-v3 (2026-09-10)`

Paragraphs 1 and 2 are byte-identical to v2. The pickup-day example survives, per
standing client instruction. Only the third paragraph changes.

**v2 (preserved untouched):**
> If a weekly payment fails, we'll retry your card over the next two days and
> notify you each time; you keep the car while we retry. If it still can't be
> collected, weekly billing stops and your rental ends when your paid time runs
> out. Any days you used but didn't pay for are still owed — you can settle them
> in the app with Pay now, and we'll contact you if the balance isn't paid.

**v3 (proposed):**
> If a weekly payment fails, we'll retry your card over the next two days and
> notify you each time; you keep the car while we retry. If it still can't be
> collected, weekly billing stops and your rental ends when your paid time runs
> out. Any days you used but didn't pay for are still owed. They're added to a
> balance you can see and pay off in the app at any time. Until that balance is
> cleared you won't be able to start a new rental, and we'll contact you about it.
> Closing your DriveBai account doesn't clear what you owe.

### Recommendation: do NOT tell the driver about the guarantee

1. Their obligation is unchanged, so there is nothing new for them to agree to.
2. "The owner gets paid anyway" reduces the urgency to pay a debt we intend to
   pursue.
3. It invites the belief that the debt is settled — the exact confusion the
   accounting design exists to prevent.

Your call; flagging it rather than deciding it.

---

## 3. Deletion notice copy (Priority 4)

Shown at the deletion confirmation step when a balance is outstanding. Deletion
proceeds either way.

> **You have an outstanding balance of $X.XX.**
> Deleting your account does not clear it. You still owe this amount, and we may
> continue to contact you about it or pass it to a collections process.
> We'll keep a record of the balance and the transactions behind it for as long
> as it is owed and as long as the law requires us to keep financial records.
> Everything else about your account is deleted.
>
> If you'd rather clear it first, pay now and then delete — it takes one tap.
> [Pay $X.XX and delete]  [Delete anyway]

Both buttons delete. The first collects payment first.
