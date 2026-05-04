ALTER TABLE lease_requests
    DROP COLUMN IF EXISTS offered_weekly_price,
    DROP COLUMN IF EXISTS offered_price_updated_at;
