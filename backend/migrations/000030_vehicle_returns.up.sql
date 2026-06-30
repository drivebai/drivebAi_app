-- Vehicle return flow: driver-initiated → owner-confirmed → refund issued.
--
-- One row per lease (UNIQUE on lease_request_id) gives us the same
-- idempotent-creation pattern as key_handovers (migration 000026). The
-- separate table keeps the lifecycle, refund metadata, and dispute audit
-- fields out of lease_requests, which is already carrying 11+ statuses
-- and three refund columns from migration 000024.

CREATE TABLE IF NOT EXISTS vehicle_returns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id UUID NOT NULL UNIQUE
        REFERENCES lease_requests(id) ON DELETE CASCADE,
    car_id UUID NOT NULL REFERENCES cars(id),
    owner_id UUID NOT NULL REFERENCES users(id),
    driver_id UUID NOT NULL REFERENCES users(id),

    -- State machine
    status VARCHAR(24) NOT NULL DEFAULT 'driver_initiated'
        CHECK (status IN (
            'driver_initiated',
            'owner_confirmed',
            'disputed',
            'completed',
            'cancelled'
        )),

    driver_initiated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    owner_confirmed_at TIMESTAMPTZ,
    disputed_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,

    -- Rental clock snapshot (frozen at driver_initiated)
    pickup_confirmed_at TIMESTAMPTZ NOT NULL,
    returned_at TIMESTAMPTZ NOT NULL,
    rental_weeks INT NOT NULL,
    paid_amount_cents BIGINT NOT NULL,

    -- Refund computation outcome
    used_days INT NOT NULL,
    refund_amount_cents BIGINT NOT NULL DEFAULT 0,
    refund_id TEXT,
    refund_status VARCHAR(20)
        CHECK (refund_status IS NULL OR refund_status IN ('pending','succeeded','failed','not_applicable')),
    refunded_at TIMESTAMPTZ,
    refund_failure_reason TEXT,

    -- Optional dispute metadata
    dispute_reason TEXT,
    dispute_resolved_by VARCHAR(16) CHECK (dispute_resolved_by IS NULL OR dispute_resolved_by IN ('owner','admin')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vehicle_returns_owner_id ON vehicle_returns(owner_id);
CREATE INDEX IF NOT EXISTS idx_vehicle_returns_driver_id ON vehicle_returns(driver_id);
CREATE INDEX IF NOT EXISTS idx_vehicle_returns_status ON vehicle_returns(status);

-- Stuck-refund scanner companion index: rows where the owner has confirmed
-- (or the row reached completed via the zero-refund fast-path) but the
-- Stripe refund hasn't finalized. Matches the predicate the scanner uses
-- in ListStuckRefunds.
CREATE INDEX IF NOT EXISTS idx_vehicle_returns_stuck_refunds
    ON vehicle_returns(updated_at)
    WHERE refund_amount_cents > 0
      AND refund_id IS NULL
      AND (refund_status IS NULL OR refund_status IN ('pending','failed'));

-- Lease-side flag so the pickup-expiry scanner can ignore leases whose
-- rental has been returned (and so other Today queries can suppress
-- post-return cards). Set in the same tx that flips a vehicle_return to
-- 'completed'. NULL on every lease that pre-dates this migration.
ALTER TABLE lease_requests
    ADD COLUMN IF NOT EXISTS vehicle_returned_at TIMESTAMPTZ;
