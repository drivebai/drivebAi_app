-- Reverse 000024.
--
-- WARNING: lossy if any rows have status='expired_refunded' — reverting the
-- column back to the lease_request_status ENUM (which lacks that value)
-- would otherwise fail at cast time. We remap them to 'expired' first.

UPDATE lease_requests SET status = 'expired' WHERE status = 'expired_refunded';

ALTER TABLE lease_requests
    DROP CONSTRAINT IF EXISTS lease_requests_status_check;

-- Drop partial indexes whose predicates reference status before the type
-- revert, for the same reason as the up migration: ALTER COLUMN TYPE
-- would otherwise try to recompile their predicates against the new
-- (enum) type and fail.
DROP INDEX IF EXISTS idx_lease_requests_pickup_deadline;
DROP INDEX IF EXISTS idx_lease_requests_active_per_driver_listing;
DROP INDEX IF EXISTS idx_lease_requests_expires_at;

ALTER TABLE lease_requests
    ALTER COLUMN status DROP DEFAULT,
    ALTER COLUMN status TYPE lease_request_status USING status::lease_request_status,
    ALTER COLUMN status SET DEFAULT 'requested'::lease_request_status;

-- Recreate the partial indexes against the restored enum column. Mirrors
-- the original definitions from migrations 000007 and 000009.
CREATE UNIQUE INDEX idx_lease_requests_active_per_driver_listing
    ON lease_requests(driver_id, listing_id)
    WHERE status IN ('requested', 'accepted', 'payment_pending');

CREATE INDEX idx_lease_requests_expires_at
    ON lease_requests(expires_at)
    WHERE status = 'requested';

ALTER TABLE lease_requests
    DROP COLUMN IF EXISTS refund_status,
    DROP COLUMN IF EXISTS refunded_at,
    DROP COLUMN IF EXISTS refund_id,
    DROP COLUMN IF EXISTS pickup_confirmed_at,
    DROP COLUMN IF EXISTS pickup_deadline_at;

DROP INDEX IF EXISTS idx_cars_reserved_by;

ALTER TABLE cars
    DROP COLUMN IF EXISTS reserved_by_lease_request_id;
