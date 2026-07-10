-- Relax the purchase-offer floor from $1,000 (100000 cents) to strictly
-- positive (> 0). Offers are free-form negotiation and may be above or below
-- the listed sale price; the only invariant is that an offer is positive.
--
-- The original inline column CHECK from migration 000031 is auto-named
-- purchase_requests_offer_amount_cents_check by Postgres. Relaxing a CHECK
-- only widens the accepted set, so no existing row (all >= 100000) breaks.
ALTER TABLE purchase_requests
  DROP CONSTRAINT IF EXISTS purchase_requests_offer_amount_cents_check;
ALTER TABLE purchase_requests
  ADD CONSTRAINT purchase_requests_offer_amount_cents_check
  CHECK (offer_amount_cents > 0);
