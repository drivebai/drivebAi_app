-- Migration 000022: key handovers
-- One handover per paid lease request. Models the two-party confirmation handshake
-- (owner hands over keys → driver confirms receipt) that starts the rental clock.
-- The UNIQUE(lease_request_id) gives DB-level idempotency for creation on payment success.

CREATE TABLE key_handovers (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id      UUID        NOT NULL UNIQUE REFERENCES lease_requests(id) ON DELETE CASCADE,
    car_id                UUID        NOT NULL REFERENCES cars(id),
    owner_id              UUID        NOT NULL REFERENCES users(id),
    driver_id             UUID        NOT NULL REFERENCES users(id),
    -- Pickup location snapshot copied from the car listing at creation time
    pickup_latitude       DOUBLE PRECISION,
    pickup_longitude      DOUBLE PRECISION,
    pickup_area           TEXT,
    -- pending | owner_confirmed | completed | expired
    status                VARCHAR(20) NOT NULL DEFAULT 'pending',
    owner_confirmed_at    TIMESTAMPTZ,
    driver_confirmed_at   TIMESTAMPTZ,
    confirmation_deadline TIMESTAMPTZ,
    started_at            TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_key_handovers_owner_id  ON key_handovers(owner_id);
CREATE INDEX idx_key_handovers_driver_id ON key_handovers(driver_id);
CREATE INDEX idx_key_handovers_status    ON key_handovers(status);
