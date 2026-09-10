DROP INDEX IF EXISTS idx_purchase_inspection_deadline;
ALTER TABLE purchase_requests
    DROP COLUMN IF EXISTS inspection_auto_accepted_at,
    DROP COLUMN IF EXISTS inspection_warned_2h_at,
    DROP COLUMN IF EXISTS inspection_warned_24h_at;
