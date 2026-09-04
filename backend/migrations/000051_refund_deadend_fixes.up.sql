-- Money dead-end fixes (Sep batch, items 2+3).
--
-- Item 2: refund_delay_notified_at makes the "Refund delayed" driver
-- notice claimed-once — a stuck refund used to push a fresh notification
-- on every scanner tick (~2 min). House pattern: the term scanner's
-- phase flags.
--
-- Item 3: refund_status gains 'unrecoverable' — a PERMANENT refund
-- failure (Stripe resource_missing: the PaymentIntent no longer exists,
-- so no automated refund can ever succeed). Distinct from 'failed'
-- (transient, scanner retries): the scanner excludes it, a support
-- ticket surfaces it, and the admin settle endpoint gains a product
-- exit for it. Demonstrated live in the Aug 28 cleanup, which needed
-- raw SQL — never again.

ALTER TABLE vehicle_returns
    ADD COLUMN refund_delay_notified_at TIMESTAMPTZ;

ALTER TABLE vehicle_returns
    DROP CONSTRAINT IF EXISTS vehicle_returns_refund_status_check;
ALTER TABLE vehicle_returns
    ADD CONSTRAINT vehicle_returns_refund_status_check
    CHECK (refund_status IS NULL OR refund_status IN ('pending', 'succeeded', 'failed', 'not_applicable', 'unrecoverable'));

-- Item 4: explicit payment-window stamp (the house auth-TTL idiom from the
-- purchase scanner — auth_expires_at). updated_at is unusable as the TTL
-- clock because set_lease_requests_updated_at overwrites it on every write.
ALTER TABLE lease_requests
    ADD COLUMN payment_pending_at TIMESTAMPTZ;

-- Backfill any in-flight payment windows so the sweep's clock starts now
-- rather than never.
UPDATE lease_requests SET payment_pending_at = updated_at
WHERE status = 'payment_pending' AND payment_pending_at IS NULL;

-- Accepted-lease TTL (client decision, Sep 4): 72 hours from acceptance,
-- both parties warned 24 hours before expiry. Same idiom as
-- payment_pending_at above; accept_expiry_warned_at is the claimed-once
-- warning flag.
ALTER TABLE lease_requests
    ADD COLUMN accepted_at TIMESTAMPTZ,
    ADD COLUMN accept_expiry_warned_at TIMESTAMPTZ;

-- Backfill in-flight accepted rows: updated_at is the closest available
-- proxy for when acceptance happened; their 72h clock starts from there.
UPDATE lease_requests SET accepted_at = updated_at
WHERE status = 'accepted' AND accepted_at IS NULL;
