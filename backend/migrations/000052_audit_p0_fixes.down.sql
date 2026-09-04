-- Postgres cannot drop enum values; relabel rows that use the new ones so
-- pre-000052 code never sees an unknown status, and leave the enum values
-- in place (harmless — nothing selects by them after this relabel).
--   'refunded'             → 'canceled': closest legacy meaning (the intent
--                            can no longer be charged; money went back).
--   'refund_unrecoverable' → 'succeeded': returns the row to the pre-fix
--                            world where the orphan sat silently succeeded.
UPDATE payments SET status = 'canceled'  WHERE status = 'refunded';
UPDATE payments SET status = 'succeeded' WHERE status = 'refund_unrecoverable';

-- H7 rows: lease refund_status is an unconstrained VARCHAR; relabel
-- 'unrecoverable' back to 'failed' so the retry sweep owns them again.
UPDATE lease_requests SET refund_status = 'failed' WHERE refund_status = 'unrecoverable';

ALTER TABLE purchase_requests
    DROP COLUMN IF EXISTS accepted_at,
    DROP COLUMN IF EXISTS accept_expiry_warned_at;

ALTER TABLE users DROP COLUMN IF EXISTS stripe_customer_id;

DROP INDEX IF EXISTS idx_payments_succeeded_updated;
