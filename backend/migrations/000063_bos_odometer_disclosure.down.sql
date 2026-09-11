ALTER TABLE purchase_bill_of_sales
    DROP COLUMN IF EXISTS odometer_declared_at,
    DROP COLUMN IF EXISTS odometer_accuracy,
    DROP COLUMN IF EXISTS odometer_reading;
