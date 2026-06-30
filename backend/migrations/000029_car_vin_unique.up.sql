-- Enforce VIN uniqueness on cars (case-insensitive).
--
-- The unique index is partial: rows with NULL or empty-string VIN are exempt
-- so existing legacy listings that pre-date the VIN column continue to be valid
-- and owners can still create a listing without a VIN. The CarHandler also
-- runs a pre-flight ExistsByVIN check and returns 409 so users see a clean
-- error instead of an opaque DB failure.
--
-- WARNING: if production already has duplicate (LOWER(vin)) rows this
-- migration will abort with a unique-violation error. Before deploying, run:
--   SELECT LOWER(vin), COUNT(*) FROM cars
--    WHERE vin IS NOT NULL AND vin <> ''
--    GROUP BY 1 HAVING COUNT(*) > 1;
-- and manually reconcile any duplicates. This migration deliberately does
-- NOT auto-delete duplicate rows.
CREATE UNIQUE INDEX IF NOT EXISTS cars_vin_unique_lower_idx
ON cars (LOWER(vin))
WHERE vin IS NOT NULL AND vin <> '';
