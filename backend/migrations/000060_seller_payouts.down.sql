DROP INDEX IF EXISTS uq_owner_payouts_purchase;
ALTER TABLE owner_payouts DROP CONSTRAINT IF EXISTS owner_payouts_source_check;
ALTER TABLE owner_payouts
    ADD CONSTRAINT owner_payouts_source_check CHECK (
        source IN ('return_completed', 'admin_settlement', 'cycle_consumed')
    );
ALTER TABLE owner_payouts DROP CONSTRAINT IF EXISTS owner_payouts_one_source;
-- Sale payouts must be gone before lease_request_id can be NOT NULL again.
DELETE FROM owner_payouts WHERE purchase_request_id IS NOT NULL;
ALTER TABLE owner_payouts DROP COLUMN IF EXISTS purchase_request_id;
ALTER TABLE owner_payouts ALTER COLUMN lease_request_id SET NOT NULL;
