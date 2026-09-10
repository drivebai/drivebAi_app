-- Make the inspection window real.
--
-- inspection_deadline_at has been written, returned to clients and read by
-- NOTHING since the purchase flow shipped. The only clock that ended
-- `awaiting_inspection` was the 7-day Stripe authorization lapsing, and by
-- then the seller had already handed over the keys — so the exit was "buyer
-- keeps the car, the hold dies, nobody is paid".
--
-- Silence now completes the sale. That is only defensible if the silence was
-- informed, so these columns record that we warned the buyer, twice, and
-- when. The record is the evidence if a buyer later says they had no chance
-- to inspect.
ALTER TABLE purchase_requests
    ADD COLUMN IF NOT EXISTS inspection_warned_24h_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS inspection_warned_2h_at  TIMESTAMPTZ,
    -- Set when the window expired into a capture rather than a buyer action,
    -- so "who accepted this sale" is answerable forever.
    ADD COLUMN IF NOT EXISTS inspection_auto_accepted_at TIMESTAMPTZ;

-- The sweep picks rows by deadline; keep it off a full scan.
CREATE INDEX IF NOT EXISTS idx_purchase_inspection_deadline
    ON purchase_requests (inspection_deadline_at)
    WHERE status = 'awaiting_inspection' AND inspection_deadline_at IS NOT NULL;
