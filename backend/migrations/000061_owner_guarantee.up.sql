-- The capped owner guarantee.
--
-- DriveBai pays the owner up to ONE WEEK of rent it could not collect from
-- the driver, once the rental is closed out on the platform by any route we
-- recognise — including a car that was never recovered. A guarantee that pays
-- when an owner loses a week's rent and pays nothing when they lose the car
-- is backwards for a policy whose purpose is keeping owners, and the one-week
-- cap already bounds the exposure either way.
--
-- A guaranteed payment is NOT a collected one, and the two must never be
-- mistakable for each other in the ledger.
ALTER TABLE owner_payouts
    ADD COLUMN IF NOT EXISTS funding_source TEXT NOT NULL DEFAULT 'collected'
        CHECK (funding_source IN ('collected', 'platform_guarantee')),
    -- The uncollected week this guarantee covers.
    ADD COLUMN IF NOT EXISTS guarantee_for_cycle_id UUID REFERENCES billing_cycles(id) ON DELETE RESTRICT,
    -- Money later recovered from the driver, applied against what we fronted.
    -- The owner keeps their payment; this reimburses the PLATFORM.
    ADD COLUMN IF NOT EXISTS guarantee_recovered_cents BIGINT NOT NULL DEFAULT 0
        CHECK (guarantee_recovered_cents >= 0);

-- One guarantee per uncollected week, ever.
CREATE UNIQUE INDEX IF NOT EXISTS uq_owner_payouts_guarantee_cycle
    ON owner_payouts (guarantee_for_cycle_id)
    WHERE guarantee_for_cycle_id IS NOT NULL;

-- Outstanding exposure, per owner and in total, is a scan of these.
CREATE INDEX IF NOT EXISTS idx_owner_payouts_guarantee
    ON owner_payouts (owner_id)
    WHERE funding_source = 'platform_guarantee';

-- A guarantee row must name the week it covers; a collected row must not.
ALTER TABLE owner_payouts
    ADD CONSTRAINT owner_payouts_guarantee_shape CHECK (
        (funding_source = 'platform_guarantee' AND guarantee_for_cycle_id IS NOT NULL) OR
        (funding_source = 'collected' AND guarantee_for_cycle_id IS NULL)
    );
