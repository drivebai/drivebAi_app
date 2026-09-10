ALTER TABLE owner_payouts DROP CONSTRAINT IF EXISTS owner_payouts_guarantee_shape;
DROP INDEX IF EXISTS idx_owner_payouts_guarantee;
DROP INDEX IF EXISTS uq_owner_payouts_guarantee_cycle;
-- Guarantee rows must go before the columns that identify them.
DELETE FROM owner_payouts WHERE funding_source = 'platform_guarantee';
ALTER TABLE owner_payouts
    DROP COLUMN IF EXISTS guarantee_recovered_cents,
    DROP COLUMN IF EXISTS guarantee_for_cycle_id,
    DROP COLUMN IF EXISTS funding_source;
