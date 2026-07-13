-- VIN uniqueness for the required-VIN era + a guard for grandfathered
-- VIN-less rows, plus a sold-car escape hatch.
--
-- Background: a valid 17-char VIN is now REQUIRED for every NEW listing
-- (enforced in CreateCar), but pre-existing rows may carry NULL/empty VINs
-- and must keep working. This migration:
--   1. reconciles existing duplicate ACTIVE rows so the new unique indexes
--      can build without aborting on a 23505;
--   2. recreates cars_vin_unique_lower_idx to ALSO exclude sold rows, so a
--      completed sale frees the VIN for a re-reviewed relist by a new owner;
--   3. adds a partial unique index over (owner_id, make, model, year) for
--      grandfathered VIN-less rows so they can't silently duplicate either.
--
-- THREE-PLACE INVARIANT: the predicate on cars_vin_unique_lower_idx below
-- (vin non-empty AND archived_at IS NULL AND status <> 'sold') MUST stay in
-- lockstep with CarRepository.ExistsByVIN and ExistsByVINExcludingID.

-- ── 1. Reconcile duplicate ACTIVE rows ──────────────────────────────────────
-- "Active" = not archived and not sold; those are exactly the rows the new
-- unique indexes cover. For each collision group keep the most recently
-- created row and soft-archive the older duplicates (archived_at = now()),
-- which drops them out of Discover / owner lists / uniqueness while leaving
-- their history intact. Data-only and NOT reversible by the down migration.

-- 1a. Duplicate non-empty VINs (case-insensitive).
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY LOWER(vin)
               ORDER BY created_at DESC, id DESC
           ) AS rn
    FROM cars
    WHERE vin IS NOT NULL AND vin <> ''
      AND archived_at IS NULL
      AND status <> 'sold'
)
UPDATE cars c
SET archived_at = NOW()
FROM ranked
WHERE c.id = ranked.id
  AND ranked.rn > 1;

-- 1b. Duplicate VIN-less rows sharing the same owner + make/model/year.
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY owner_id, LOWER(make), LOWER(model), year
               ORDER BY created_at DESC, id DESC
           ) AS rn
    FROM cars
    WHERE (vin IS NULL OR vin = '')
      AND archived_at IS NULL
      AND status <> 'sold'
)
UPDATE cars c
SET archived_at = NOW()
FROM ranked
WHERE c.id = ranked.id
  AND ranked.rn > 1;

-- ── 2. VIN uniqueness scoped to live (non-archived, non-sold) rows ───────────
DROP INDEX IF EXISTS cars_vin_unique_lower_idx;

CREATE UNIQUE INDEX cars_vin_unique_lower_idx
    ON cars (LOWER(vin))
    WHERE vin IS NOT NULL AND vin <> '' AND archived_at IS NULL AND status <> 'sold';

-- ── 3. Identity uniqueness for grandfathered VIN-less rows ───────────────────
-- Guards the legacy rows that have no VIN: an owner can't hold two live
-- listings of the same make/model/year without a VIN to tell them apart.
CREATE UNIQUE INDEX IF NOT EXISTS cars_vinless_identity_unique_idx
    ON cars (owner_id, LOWER(make), LOWER(model), year)
    WHERE (vin IS NULL OR vin = '') AND archived_at IS NULL AND status <> 'sold';
