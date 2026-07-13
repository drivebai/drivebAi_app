-- Bill of Sale: structured address coordinates + seller-declared title
-- condition. All columns NULLABLE so existing/finalized rows keep working
-- (the display strings seller_address/buyer_address remain the source of the
-- printed address; lat/lng are optional supplements).

ALTER TABLE purchase_bill_of_sales
    ADD COLUMN IF NOT EXISTS seller_address_lat    DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS seller_address_lng    DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS buyer_address_lat     DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS buyer_address_lng     DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS title_condition       VARCHAR,
    ADD COLUMN IF NOT EXISTS title_condition_other TEXT;

-- Seller-declared title brand is the single source of truth (DESIGN SPEC
-- item 20). NULL is allowed (not yet declared); the buyer only acknowledges
-- it during inspection — there is NO title_condition column on the
-- inspection table.
ALTER TABLE purchase_bill_of_sales
    DROP CONSTRAINT IF EXISTS purchase_bos_title_condition_check;

ALTER TABLE purchase_bill_of_sales
    ADD CONSTRAINT purchase_bos_title_condition_check
    CHECK (
        title_condition IS NULL
        OR title_condition IN (
            'clean','lien_recorded','salvage','rebuilt',
            'lemon_buyback','flood','manufacturer_buyback','other'
        )
    );
