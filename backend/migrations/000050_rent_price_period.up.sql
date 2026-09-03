-- Daily / Weekly / Monthly pricing (client request, Part B — reading (1):
-- entry & display convenience, period stored first-class).
--
-- Storage design: the OWNER-TYPED number and its unit are the source of
-- truth (rent_price_amount + rent_price_period); weekly_rent_price stays
-- the CANONICAL BOOKING PRICE derived from them — every money path
-- (lease weekly_price snapshot, refund math, payout split, term math)
-- keeps reading weekly_rent_price and is untouched by this migration.
-- When drivers can one day book in other units (reading (2)), the period
-- is already first-class here.
--
-- A pricing "month" is exactly 4 weeks (28 days) — per the client's own
-- arithmetic ($420/wk × 4 = $1,680/mo), never a calendar month. The single
-- definition lives in models.RentMonthWeeks and this comment.

ALTER TABLE cars
    ADD COLUMN rent_price_period VARCHAR(8) NOT NULL DEFAULT 'weekly'
        CHECK (rent_price_period IN ('daily', 'weekly', 'monthly')),
    ADD COLUMN rent_price_amount DECIMAL(10, 2);

-- Backfill: every existing listing was priced weekly, so the owner-typed
-- amount IS the weekly price. No owner re-enters anything.
UPDATE cars SET rent_price_amount = weekly_rent_price
WHERE rent_price_amount IS NULL AND weekly_rent_price IS NOT NULL;
