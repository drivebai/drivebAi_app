-- Rental term end (lifecycle batch, defect 2): the paid term's end was only
-- ever DERIVED in the owner-cars handler (pickup_confirmed_at + weeks*7d) and
-- stored nowhere, so nothing could index, query, or scan for "this rental is
-- over" — a driver who never tapped "I returned the car" held the listing as
-- 'rented' forever.
--
-- rental_ends_at is stamped by ConfirmPickup (the single transaction both
-- pickup paths funnel through) and backfilled here for live rentals.
--
-- The three *_at flags are the term scanner's idempotency ledger — each phase
-- claims a row by setting its flag under a WHERE flag IS NULL guard, so a
-- second scanner instance (or a re-run after a crash) can never notify or
-- escalate twice. Mirrors the claim pattern of ClaimForExpiry.

ALTER TABLE lease_requests
    ADD COLUMN rental_ends_at TIMESTAMPTZ,
    ADD COLUMN term_ending_notified_at TIMESTAMPTZ,
    ADD COLUMN overdue_notified_at TIMESTAMPTZ,
    ADD COLUMN overdue_escalated_at TIMESTAMPTZ;

-- Backfill live rentals so nothing already picked up is left without an end
-- date. GREATEST(weeks, 1) mirrors ComputeReturnRefund's defensive floor —
-- the lease handler enforces weeks >= 1, but a 0-week row must not produce
-- an end date in the past of its own pickup.
UPDATE lease_requests
SET rental_ends_at = pickup_confirmed_at + (GREATEST(weeks, 1) * INTERVAL '7 days')
WHERE status = 'paid'
  AND pickup_confirmed_at IS NOT NULL
  AND rental_ends_at IS NULL;

-- Partial index scoped to exactly the rows the term scanner sweeps:
-- paid, picked up, not yet returned.
CREATE INDEX idx_lease_requests_rental_ends
    ON lease_requests(rental_ends_at)
    WHERE status = 'paid'
      AND pickup_confirmed_at IS NOT NULL
      AND vehicle_returned_at IS NULL;
