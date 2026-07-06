-- Migration 000032: 12-point QA round — schema foundation.
--
-- Contents (all transaction-safe; no ALTER TYPE ADD VALUE, per the
-- CHECK-over-enum pattern established by 000023/000024/000031):
--   1. car_photos.slot_type   ENUM → VARCHAR(30) + CHECK, +3 guided slots
--   2. car_documents.document_type ENUM → VARCHAR(30) + CHECK, +'title'
--   3. cars.archived_at (soft-archive replaces hard DELETE)
--   4. VIN unique index recreated with `archived_at IS NULL` so archiving
--      a listing frees its VIN for re-listing (three-place invariant:
--      this index + ExistsByVIN + ExistsByVINExcludingID must agree)
--   5. Status backfill: cars stuck on 'available' while a paid,
--      picked-up, not-yet-returned lease occupies them flip to 'rented'
--   6. Deposit neutralization: zero every row + default 0 (the column and
--      its JSON key stay for old iOS builds; the value is now always 0)

-- ── 1. car_photos.slot_type: enum → varchar + CHECK (8 guided slots) ────────
-- The UNIQUE(car_id, slot_type) constraint has no predicate, so the type
-- conversion rebuilds it automatically.
ALTER TABLE car_photos
    ALTER COLUMN slot_type TYPE VARCHAR(30) USING slot_type::text;

ALTER TABLE car_photos
    ADD CONSTRAINT car_photos_slot_type_check
    CHECK (slot_type IN (
        'cover_front', 'right', 'left', 'back', 'dashboard',
        'front_left_34', 'rear_right_34', 'interior'
    ));

DROP TYPE IF EXISTS photo_slot_type;

-- ── 2. car_documents.document_type: enum → varchar + CHECK (+'title') ───────
ALTER TABLE car_documents
    ALTER COLUMN document_type TYPE VARCHAR(30) USING document_type::text;

ALTER TABLE car_documents
    ADD CONSTRAINT car_documents_document_type_check
    CHECK (document_type IN (
        'inspection', 'registration', 'permit', 'insurance', 'title'
    ));

DROP TYPE IF EXISTS car_document_type;

-- ── 3. Soft-archive column ───────────────────────────────────────────────────
-- DeleteCar becomes SET archived_at = NOW(). Rows and files are never
-- destroyed — chats/leases/payments keep valid FKs and image URLs.
ALTER TABLE cars
    ADD COLUMN archived_at TIMESTAMPTZ;

-- ── 4. VIN uniqueness scoped to non-archived listings ───────────────────────
-- A physical car may legitimately be re-listed after its old listing is
-- archived, so archived rows must not tombstone the VIN forever.
DROP INDEX IF EXISTS cars_vin_unique_lower_idx;

CREATE UNIQUE INDEX cars_vin_unique_lower_idx
    ON cars (LOWER(vin))
    WHERE vin IS NOT NULL AND vin <> '' AND archived_at IS NULL;

-- ── 5. Status backfill for pre-status-flip rentals ───────────────────────────
-- Mirrors EXACTLY the active-rental derivation used by
-- CarRepository.GetByOwnerIDWithActiveRental: the lease reserving the car
-- is paid, pickup was confirmed, and the vehicle has not been returned.
-- Repairs rows created before the 2026-07-02 status-flip deploy.
UPDATE cars c
SET status = 'rented'
FROM lease_requests lr
WHERE c.reserved_by_lease_request_id = lr.id
  AND lr.status = 'paid'
  AND lr.pickup_confirmed_at IS NOT NULL
  AND lr.vehicle_returned_at IS NULL
  AND c.status = 'available';

-- ── 6. Deposit neutralization ────────────────────────────────────────────────
-- deposit_amount never entered any payment formula (lease = weekly × weeks,
-- purchase = offer/sale cents) — zeroing it is money-safe. The column and
-- JSON key are retained because shipped iOS builds decode deposit_amount
-- as a NON-optional Double; they now render a cosmetic "$0 deposit".
UPDATE cars SET deposit_amount = 0 WHERE deposit_amount <> 0;

ALTER TABLE cars
    ALTER COLUMN deposit_amount SET DEFAULT 0;
