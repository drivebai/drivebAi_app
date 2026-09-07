-- Relabel rows using the new statuses/sources before narrowing the CHECKs.
UPDATE owner_payouts SET status = 'withheld' WHERE status IN ('accruing', 'voided');
UPDATE owner_payouts SET source = 'admin_settlement' WHERE source = 'cycle_consumed';

DROP INDEX IF EXISTS idx_owner_payouts_accruing;

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_source_check;
ALTER TABLE owner_payouts ADD CONSTRAINT owner_payouts_source_check
    CHECK (source IN ('return_completed', 'admin_settlement'));

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_status_check;
ALTER TABLE owner_payouts ADD CONSTRAINT owner_payouts_status_check
    CHECK (status IN ('awaiting_onboarding', 'pending', 'paid', 'failed',
                      'withheld', 'reversed'));

-- Cycle rows cannot survive the legacy UNIQUE; they exist only if rolling
-- ran, and rolling down-migrations remove them with the cycles table.
DELETE FROM owner_payouts WHERE billing_cycle_id IS NOT NULL;
DROP INDEX IF EXISTS uq_owner_payouts_cycle;
DROP INDEX IF EXISTS uq_owner_payouts_legacy;
ALTER TABLE owner_payouts
    ADD CONSTRAINT owner_payouts_lease_request_id_key UNIQUE (lease_request_id);
ALTER TABLE owner_payouts
    DROP COLUMN IF EXISTS consumed_at,
    DROP COLUMN IF EXISTS period_end,
    DROP COLUMN IF EXISTS period_start,
    DROP COLUMN IF EXISTS billing_cycle_id;
