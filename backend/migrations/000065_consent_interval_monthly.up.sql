-- Recurring-only, batch 1 (2026-09-17): the consent ledger admits the
-- monthly interval.
--
-- The engine has keyed cycle length, notice lead and charge lead on
-- lease_billing_consents.billing_interval since 32b9408; only this CHECK
-- pinned it to 'weekly'. Daily is deliberately NOT admitted (see
-- docs/DESIGN_RECURRING_BILLING.md §11): a Stripe fee per charge, a 7x
-- decline/dispute surface, and a per-charge notice the consent text
-- promises that we cannot keep for every day.
ALTER TABLE lease_billing_consents
    DROP CONSTRAINT IF EXISTS lease_billing_consents_billing_interval_check;
ALTER TABLE lease_billing_consents
    ADD CONSTRAINT lease_billing_consents_billing_interval_check
    CHECK (billing_interval IN ('weekly', 'monthly'));

-- The lease itself records the interval it renews on, set at INSERT from
-- the listing's price period and immutable afterwards (like billing_mode).
-- Every pre-existing row is weekly: fixed-term rows never renew, and every
-- rolling row so far consented on the weekly text.
ALTER TABLE lease_requests
    ADD COLUMN IF NOT EXISTS billing_interval TEXT NOT NULL DEFAULT 'weekly'
        CHECK (billing_interval IN ('weekly', 'monthly'));
