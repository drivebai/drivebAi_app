-- Odometer disclosure on the Bill of Sale.
--
-- Federal law (49 U.S.C. 32705, 49 CFR 580) requires the transferor of a
-- motor vehicle to disclose the odometer reading in writing, with a
-- certification that it is the actual mileage, exceeds the odometer's
-- mechanical limits, or is NOT the actual mileage — and requires the
-- transferee to acknowledge it. Until now the document carried only an
-- optional "Mileage" line copied from nowhere. The seller now declares the
-- reading and its accuracy before they can sign; the buyer signs beneath the
-- statement; the PDF prints it as its own section.
ALTER TABLE purchase_bill_of_sales
    ADD COLUMN IF NOT EXISTS odometer_reading     INTEGER CHECK (odometer_reading IS NULL OR odometer_reading >= 0),
    ADD COLUMN IF NOT EXISTS odometer_accuracy    VARCHAR
        CHECK (odometer_accuracy IS NULL OR odometer_accuracy IN ('actual', 'not_actual', 'exceeds_mechanical_limits')),
    ADD COLUMN IF NOT EXISTS odometer_declared_at TIMESTAMPTZ;
