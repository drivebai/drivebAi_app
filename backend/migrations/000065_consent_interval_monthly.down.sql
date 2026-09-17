-- Re-pins the consent interval to weekly. This DOWN deliberately FAILS if a
-- monthly consent row exists: a consent is evidence, and narrowing the
-- CHECK under it would make history unreadable. Close or migrate those
-- rows first; never delete them.
ALTER TABLE lease_billing_consents
    DROP CONSTRAINT IF EXISTS lease_billing_consents_billing_interval_check;
ALTER TABLE lease_billing_consents
    ADD CONSTRAINT lease_billing_consents_billing_interval_check
    CHECK (billing_interval = 'weekly');
ALTER TABLE lease_requests DROP COLUMN IF EXISTS billing_interval;
