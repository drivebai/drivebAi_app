-- Audit P0/P1 fix batch (Sep 4).
--
-- M3: payments gains terminal sub-states beyond Stripe's own so the
-- orphaned-payment reconciliation can record its outcome:
--   'refunded'             — charge captured against a lease that had already
--                            gone terminal; we refunded it in full.
--   'refund_unrecoverable' — same, but the refund can never succeed
--                            automatically (PI gone at Stripe); a support
--                            ticket owns it. Excluded from the retry sweep.
-- Enum values cannot be dropped, so the down file relabels rows and leaves
-- the values in place (documented there).
ALTER TYPE payment_status ADD VALUE IF NOT EXISTS 'refunded';
ALTER TYPE payment_status ADD VALUE IF NOT EXISTS 'refund_unrecoverable';

-- H3: purchase accepted-TTL clocks. Explicit stamp columns, NOT updated_at —
-- set_purchase_requests_updated_at (000031) overwrites updated_at on every
-- write, exactly the trap 000051 documented for lease_requests.
ALTER TABLE purchase_requests
    ADD COLUMN accepted_at TIMESTAMPTZ,
    ADD COLUMN accept_expiry_warned_at TIMESTAMPTZ;

-- Backfill in-flight post-accept rows so their 72h clock starts from the
-- closest available proxy. (Prod has zero such rows today — verified — but
-- the backfill must still be correct for any environment that does.)
UPDATE purchase_requests SET accepted_at = updated_at
WHERE status IN ('accepted', 'bos_pending_seller', 'bos_pending_buyer', 'bos_signed')
  AND accepted_at IS NULL;

-- H6: durable user→Stripe-customer binding. Customers were previously found
-- by EMAIL SEARCH at charge time, which lets a re-registered email inherit a
-- deleted user's saved cards. The column is the binding; email search dies in
-- code. Backfill from each driver's most recent payment row; deleted users
-- deliberately keep NULL (a re-registrant is a different user row and mints
-- a fresh customer).
ALTER TABLE users ADD COLUMN stripe_customer_id TEXT;

-- The NOT EXISTS guard refuses to cement a binding the email-search bug
-- already contaminated: a customer id that also appears on a DELETED
-- user's payments is exactly the inherited-cards case — leave it NULL so
-- customerForUser mints a clean customer instead.
UPDATE users u SET stripe_customer_id = p.scid
FROM (
    SELECT DISTINCT ON (lr.driver_id) lr.driver_id, pm.stripe_customer_id AS scid
    FROM payments pm
    JOIN lease_requests lr ON lr.id = pm.lease_request_id
    WHERE pm.stripe_customer_id IS NOT NULL
    ORDER BY lr.driver_id, pm.created_at DESC
) p
WHERE u.id = p.driver_id
  AND u.stripe_customer_id IS NULL
  AND u.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM payments pm2
      JOIN lease_requests lr2 ON lr2.id = pm2.lease_request_id
      JOIN users du ON du.id = lr2.driver_id
      WHERE pm2.stripe_customer_id = p.scid
        AND du.id <> p.driver_id
        AND du.deleted_at IS NOT NULL
  );

-- The orphaned-payment sweep (M3 backstop) runs every scanner tick with
-- predicate status='succeeded'; give it a partial index so it never
-- degrades into a per-minute seq scan as payments grow.
CREATE INDEX idx_payments_succeeded_updated
    ON payments(updated_at)
    WHERE status = 'succeeded';
