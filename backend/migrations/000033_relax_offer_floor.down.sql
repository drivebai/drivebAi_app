-- Restore the $1,000 (100000 cents) offer floor. NOTE: this down migration
-- will fail if any purchase_requests row created under the relaxed rule has
-- offer_amount_cents between 1 and 99,999 — that is expected, since those
-- rows are only representable under the widened constraint.
ALTER TABLE purchase_requests
  DROP CONSTRAINT IF EXISTS purchase_requests_offer_amount_cents_check;
ALTER TABLE purchase_requests
  ADD CONSTRAINT purchase_requests_offer_amount_cents_check
  CHECK (offer_amount_cents >= 100000);
