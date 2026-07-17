-- Revert 000038_reviews: drop the reviews table and everything attached to
-- it (indexes + trigger drop with the table).

DROP TABLE IF EXISTS reviews;
