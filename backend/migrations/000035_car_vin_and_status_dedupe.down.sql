-- Reverse the index changes from 000035.
--
-- NOTE: the duplicate-row reconciliation in the up migration (step 1) sets
-- archived_at = now() on older duplicates. That is a data change and is NOT
-- reversible here — we cannot know which rows were archived by the migration
-- versus by owners, so the down migration deliberately does NOT un-archive
-- anything. It only restores the index definitions to their 000032 shape.

DROP INDEX IF EXISTS cars_vinless_identity_unique_idx;

-- Restore cars_vin_unique_lower_idx to the pre-000035 (000032) predicate,
-- which did NOT exclude sold rows.
DROP INDEX IF EXISTS cars_vin_unique_lower_idx;

CREATE UNIQUE INDEX cars_vin_unique_lower_idx
    ON cars (LOWER(vin))
    WHERE vin IS NOT NULL AND vin <> '' AND archived_at IS NULL;
