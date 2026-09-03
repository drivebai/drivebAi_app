ALTER TABLE cars
    DROP COLUMN IF EXISTS rent_price_period,
    DROP COLUMN IF EXISTS rent_price_amount;
