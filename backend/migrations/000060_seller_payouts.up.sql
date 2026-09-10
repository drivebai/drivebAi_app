-- Let the payout ledger record a CAR SALE.
--
-- owner_payouts.lease_request_id is NOT NULL REFERENCES lease_requests(id), so
-- a sale — which has no lease row and never will — could not be recorded at
-- all. A captured sale simply sat in the platform Stripe balance with no
-- ledger entry of any kind. That is why DISABLE_CAR_SALES is on.
--
-- Shape: keep one ledger for both rentals and sales rather than a parallel
-- seller_payouts table. The money mechanics are identical (split, transfer,
-- escrow when the payee is not onboarded, reversal on refund), and a second
-- table would mean a second implementation of every one of those, each free
-- to drift. The row now points at EXACTLY ONE source.

ALTER TABLE owner_payouts
    ALTER COLUMN lease_request_id DROP NOT NULL;

ALTER TABLE owner_payouts
    ADD COLUMN IF NOT EXISTS purchase_request_id UUID REFERENCES purchase_requests(id) ON DELETE RESTRICT;

-- Exactly one source. Without this a row could reference both, or neither,
-- and every downstream query would have to guess which it was.
ALTER TABLE owner_payouts
    ADD CONSTRAINT owner_payouts_one_source CHECK (
        (lease_request_id IS NOT NULL AND purchase_request_id IS NULL) OR
        (lease_request_id IS NULL AND purchase_request_id IS NOT NULL)
    );

-- 'sale_completed' joins the existing rental sources.
ALTER TABLE owner_payouts DROP CONSTRAINT IF EXISTS owner_payouts_source_check;
ALTER TABLE owner_payouts
    ADD CONSTRAINT owner_payouts_source_check CHECK (
        source IN ('return_completed', 'admin_settlement', 'cycle_consumed', 'sale_completed')
    );

-- One payout per sale: the claim that makes paying a seller re-entrant.
CREATE UNIQUE INDEX IF NOT EXISTS uq_owner_payouts_purchase
    ON owner_payouts (purchase_request_id) WHERE purchase_request_id IS NOT NULL;
