ALTER TABLE lease_requests
    DROP CONSTRAINT IF EXISTS lease_requests_pickup_extension_cap;

ALTER TABLE lease_requests
    DROP COLUMN IF EXISTS pickup_last_extended_at,
    DROP COLUMN IF EXISTS pickup_extension_count,
    DROP COLUMN IF EXISTS pickup_extension_total_minutes;
