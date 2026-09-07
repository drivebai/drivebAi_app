-- Batch 2, part 1: the rolling-lease mode columns (design §6).
-- billing_mode is set at INSERT and immutable; every pre-existing row is a
-- fixed-term lease and the DEFAULT captures that with no backfill and no
-- table rewrite. All other columns are rolling-only stamps that stay NULL
-- on every fixed-term row forever — the fixed-term guarantee is the
-- DEFAULT plus predicates that all require billing_mode='rolling'.

ALTER TABLE lease_requests
    ADD COLUMN billing_mode TEXT NOT NULL DEFAULT 'fixed_term'
        CHECK (billing_mode IN ('fixed_term', 'rolling')),
    ADD COLUMN renewal_stopped_at    TIMESTAMPTZ,
    ADD COLUMN renewal_stopped_by    TEXT
        CHECK (renewal_stopped_by IN ('driver', 'owner', 'system')),
    ADD COLUMN delinquent_since      TIMESTAMPTZ,
    ADD COLUMN renewal_notified_for  TIMESTAMPTZ,
    ADD COLUMN renewal_halted_reason TEXT
        CHECK (renewal_halted_reason IN
               ('delinquent', 'dispute', 'consent_revoked', 'return_initiated')),
    ADD COLUMN continues_lease_id    UUID REFERENCES lease_requests(id);

-- weeks is frozen at 1 for rolling: TotalAmountCents (first charge),
-- ConfirmPickup's rental_ends_at stamp (first cycle), and
-- BackfillMissingTermEnds all become correct verbatim.
ALTER TABLE lease_requests
    ADD CONSTRAINT lease_requests_rolling_weeks_one
        CHECK (billing_mode = 'fixed_term' OR weeks = 1),
    ADD CONSTRAINT lease_requests_stop_stamp_paired
        CHECK ((renewal_stopped_at IS NULL) = (renewal_stopped_by IS NULL));

-- Due-charge sweep: exactly the rows the billing engine may charge —
-- rolling, occupied, auto-renew live, nothing halting it.
CREATE INDEX idx_lease_requests_rolling_due
    ON lease_requests (rental_ends_at)
    WHERE billing_mode = 'rolling'
      AND status = 'paid'
      AND pickup_confirmed_at IS NOT NULL
      AND vehicle_returned_at IS NULL
      AND renewal_stopped_at IS NULL
      AND delinquent_since IS NULL
      AND renewal_halted_reason IS NULL;

-- One live continuation per predecessor lease (column ships now; the
-- continuation endpoint is a later batch).
CREATE UNIQUE INDEX idx_lease_requests_continuation_live
    ON lease_requests (continues_lease_id)
    WHERE continues_lease_id IS NOT NULL
      AND status NOT IN ('declined', 'cancelled', 'expired', 'expired_refunded');
