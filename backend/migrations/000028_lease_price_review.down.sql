DROP INDEX IF EXISTS idx_lease_requests_price_review_pending;
ALTER TABLE lease_requests
    DROP COLUMN IF EXISTS price_change_acted_at,
    DROP COLUMN IF EXISTS previous_offered_weekly_price,
    DROP COLUMN IF EXISTS price_change_pending;
