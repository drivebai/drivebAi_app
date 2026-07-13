-- Reverse 000036.

ALTER TABLE purchase_bill_of_sales
    DROP CONSTRAINT IF EXISTS purchase_bos_title_condition_check;

ALTER TABLE purchase_bill_of_sales
    DROP COLUMN IF EXISTS seller_address_lat,
    DROP COLUMN IF EXISTS seller_address_lng,
    DROP COLUMN IF EXISTS buyer_address_lat,
    DROP COLUMN IF EXISTS buyer_address_lng,
    DROP COLUMN IF EXISTS title_condition,
    DROP COLUMN IF EXISTS title_condition_other;
