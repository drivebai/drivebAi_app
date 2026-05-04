ALTER TABLE lease_requests
    ADD COLUMN offered_weekly_price DECIMAL(12,2) NULL,
    ADD COLUMN offered_price_updated_at TIMESTAMPTZ NULL;
