-- Migration 000025: owner-initiated pickup deadline extension.
--
-- Adds three columns to lease_requests so the car owner can grant the driver
-- more time before the expiry scanner refunds the rental. Total extension
-- minutes are capped (enforced at the application layer + a CHECK constraint
-- here) so a misbehaving client can't push the deadline indefinitely.

ALTER TABLE lease_requests
    ADD COLUMN pickup_extension_total_minutes INT         NOT NULL DEFAULT 0,
    ADD COLUMN pickup_extension_count         INT         NOT NULL DEFAULT 0,
    ADD COLUMN pickup_last_extended_at        TIMESTAMPTZ;

-- Hard upper bound. Matches PickupMaxExtensionMinutes in the Go layer; the
-- application enforces this proactively (clear API error) but the CHECK is
-- the last line of defence if a future code path forgets.
ALTER TABLE lease_requests
    ADD CONSTRAINT lease_requests_pickup_extension_cap
    CHECK (pickup_extension_total_minutes BETWEEN 0 AND 120);
