-- Migration 000032 down: best-effort rollback.
--
-- Deliberately lossy:
--   * rows using the three new photo slots / 'title' doc type are DELETED
--     (the original enums cannot represent them);
--   * the status backfill is NOT reverted (the 'rented' values it wrote
--     are correct under either schema);
--   * zeroed deposits are NOT restored (original values are gone).

-- Deposit default back to the legacy 500.
ALTER TABLE cars
    ALTER COLUMN deposit_amount SET DEFAULT 500;

-- VIN index back to the unscoped predicate. NOTE: fails if an archived
-- row now duplicates a live row's VIN — reconcile manually before rolling
-- back in that case.
DROP INDEX IF EXISTS cars_vin_unique_lower_idx;

CREATE UNIQUE INDEX cars_vin_unique_lower_idx
    ON cars (LOWER(vin))
    WHERE vin IS NOT NULL AND vin <> '';

ALTER TABLE cars
    DROP COLUMN IF EXISTS archived_at;

-- car_documents back to the original 4-value enum.
DELETE FROM car_documents WHERE document_type = 'title';

ALTER TABLE car_documents
    DROP CONSTRAINT IF EXISTS car_documents_document_type_check;

CREATE TYPE car_document_type AS ENUM ('inspection', 'registration', 'permit', 'insurance');

ALTER TABLE car_documents
    ALTER COLUMN document_type TYPE car_document_type USING document_type::car_document_type;

-- car_photos back to the original 5-value enum.
DELETE FROM car_photos WHERE slot_type IN ('front_left_34', 'rear_right_34', 'interior');

ALTER TABLE car_photos
    DROP CONSTRAINT IF EXISTS car_photos_slot_type_check;

CREATE TYPE photo_slot_type AS ENUM ('cover_front', 'right', 'left', 'back', 'dashboard');

ALTER TABLE car_photos
    ALTER COLUMN slot_type TYPE photo_slot_type USING slot_type::photo_slot_type;
