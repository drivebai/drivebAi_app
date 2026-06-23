-- "Owner adjusted the price → driver must review" state for lease requests.
--
-- The owner can change `offered_weekly_price` at any moment before payment
-- has actually succeeded (status in {requested, accepted, payment_pending}).
-- Until the driver explicitly accepts or declines the new price, we want
-- the system to refuse payment and show a "Price updated" card on the
-- driver's Today + Requests surfaces.
--
-- All three columns are nullable / default-safe so existing rows stay
-- valid: every row written before this migration was applied has
-- price_change_pending=false (i.e. nothing to review), no previous price,
-- and no acted-at timestamp.
ALTER TABLE lease_requests
    ADD COLUMN price_change_pending           BOOLEAN          NOT NULL DEFAULT FALSE,
    ADD COLUMN previous_offered_weekly_price  DOUBLE PRECISION,
    ADD COLUMN price_change_acted_at          TIMESTAMPTZ;

-- Partial index speeds up the driver Today query, which only wants rows
-- where the flag is true. Small predicate keeps the index tiny — for
-- typical traffic this stays well under the size of an unindexed table.
CREATE INDEX IF NOT EXISTS idx_lease_requests_price_review_pending
    ON lease_requests (driver_id, updated_at DESC)
    WHERE price_change_pending = TRUE;
