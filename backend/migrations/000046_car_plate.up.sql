-- License plate on cars (client request: "search vehicle by license plate
-- and vin number"). Nullable free-text: plates were never captured before,
-- so every existing row starts empty and admins backfill via the console.
-- Stored trimmed + uppercased by the write path; SEARCH normalizes both
-- sides (strip non-alphanumerics, case-fold), so "ABC-123", "abc 123" and
-- "ABC123" all match. No index: the cars table is tens of rows and the
-- admin list already does unindexed ILIKE over title/make/model — add a
-- pg_trgm index only when scale demands it.
ALTER TABLE cars
    ADD COLUMN plate VARCHAR(16);
