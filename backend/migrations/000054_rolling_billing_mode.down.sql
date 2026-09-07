DROP INDEX IF EXISTS idx_lease_requests_continuation_live;
DROP INDEX IF EXISTS idx_lease_requests_rolling_due;
ALTER TABLE lease_requests
    DROP CONSTRAINT IF EXISTS lease_requests_stop_stamp_paired,
    DROP CONSTRAINT IF EXISTS lease_requests_rolling_weeks_one,
    DROP COLUMN IF EXISTS continues_lease_id,
    DROP COLUMN IF EXISTS renewal_halted_reason,
    DROP COLUMN IF EXISTS renewal_notified_for,
    DROP COLUMN IF EXISTS delinquent_since,
    DROP COLUMN IF EXISTS renewal_stopped_by,
    DROP COLUMN IF EXISTS renewal_stopped_at,
    DROP COLUMN IF EXISTS billing_mode;
