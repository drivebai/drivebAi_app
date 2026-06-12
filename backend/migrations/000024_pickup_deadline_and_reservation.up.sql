-- Migration 000024: pickup deadline + listing reservation.
--
-- New rental lifecycle: once a lease request is accepted, the listing is
-- reserved (hidden from discovery). Once paid, the driver has
-- PICKUP_DEADLINE_MINUTES (default 120) to confirm pickup; if they don't,
-- a background worker refunds via Stripe and releases the listing.

-- 1. cars.reserved_by_lease_request_id
-- A direct FK pointer is cheaper than an EXISTS-subquery in the discovery
-- query, and ON DELETE SET NULL means an unrelated lease delete (admin
-- cleanup) automatically unreserves the car.
ALTER TABLE cars
    ADD COLUMN reserved_by_lease_request_id UUID
        REFERENCES lease_requests(id) ON DELETE SET NULL;

CREATE INDEX idx_cars_reserved_by
    ON cars(reserved_by_lease_request_id)
    WHERE reserved_by_lease_request_id IS NOT NULL;

-- 2. lease_requests pickup + refund columns
ALTER TABLE lease_requests
    ADD COLUMN pickup_deadline_at  TIMESTAMPTZ,
    ADD COLUMN pickup_confirmed_at TIMESTAMPTZ,
    ADD COLUMN refund_id           TEXT,
    ADD COLUMN refunded_at         TIMESTAMPTZ,
    ADD COLUMN refund_status       VARCHAR(20);

-- 3. Convert lease_requests.status from the lease_request_status ENUM to
-- VARCHAR(30) + CHECK so we can add 'expired_refunded' (and any future
-- values) without the ALTER TYPE ADD VALUE non-transactional pain. Same
-- pattern as migration 000023 for messages.type.
--
-- ORDERING NOTE: two pre-existing partial indexes on lease_requests have
-- predicates that explicitly cast literals to the enum type (visible in
-- pg_indexes as e.g. `WHERE status = 'requested'::lease_request_status`):
--
--   * idx_lease_requests_active_per_driver_listing   (migration 000007)
--   * idx_lease_requests_expires_at                  (migration 000009)
--
-- ALTER COLUMN TYPE forces Postgres to rebuild those predicates against
-- the new column type; varchar has no implicit cast to/from the enum, so
-- the rebuild fails with:
--   ERROR: operator does not exist: character varying = lease_request_status
--
-- The fix is to drop both partial indexes, do the type conversion, then
-- recreate them with plain string literals (which now resolve cleanly to
-- varchar). Plain b-tree indexes on status (idx_lease_requests_status) do
-- not need this dance — they have no predicate to recompile.
--
-- The old `lease_request_status` ENUM type is intentionally left in the
-- catalog as dead schema — no other column references it, but dropping
-- it would couple this migration to that assumption.

DROP INDEX IF EXISTS idx_lease_requests_active_per_driver_listing;
DROP INDEX IF EXISTS idx_lease_requests_expires_at;

ALTER TABLE lease_requests
    ALTER COLUMN status DROP DEFAULT,
    ALTER COLUMN status TYPE VARCHAR(30) USING status::text,
    ALTER COLUMN status SET DEFAULT 'requested';

ALTER TABLE lease_requests
    ADD CONSTRAINT lease_requests_status_check
    CHECK (status IN (
        'requested',
        'accepted',
        'declined',
        'cancelled',
        'payment_pending',
        'paid',
        'expired',
        'expired_refunded'
    ));

-- Recreate the previously-dropped partial indexes against the new varchar
-- column. Same predicates as the original migrations (000007, 000009),
-- minus the explicit ::lease_request_status casts.
CREATE UNIQUE INDEX idx_lease_requests_active_per_driver_listing
    ON lease_requests(driver_id, listing_id)
    WHERE status IN ('requested', 'accepted', 'payment_pending');

CREATE INDEX idx_lease_requests_expires_at
    ON lease_requests(expires_at)
    WHERE status = 'requested';

-- The expiry scanner runs ~every 30s and queries:
--   pickup_deadline_at <= NOW() AND pickup_confirmed_at IS NULL AND status='paid'
-- This partial index gives it a constant-time scan. Created AFTER the
-- type conversion (see ordering note above).
CREATE INDEX idx_lease_requests_pickup_deadline
    ON lease_requests(pickup_deadline_at)
    WHERE pickup_confirmed_at IS NULL AND status = 'paid';

-- 4. Backfill: reserve cars whose existing leases are in any post-accept
-- state (accepted, payment_pending, paid). One lease per car at this
-- stage in production today — if there are multiple, the most recent
-- wins (ORDER BY created_at DESC LIMIT 1).
UPDATE cars c
SET reserved_by_lease_request_id = sub.id
FROM (
    SELECT DISTINCT ON (listing_id) id, listing_id
    FROM lease_requests
    WHERE status IN ('accepted', 'payment_pending', 'paid')
    ORDER BY listing_id, created_at DESC
) sub
WHERE c.id = sub.listing_id;
