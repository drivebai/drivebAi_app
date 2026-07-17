-- 000038_reviews
--
-- Ratings (1-5 stars) for cars AND users (drivers/owners), anchored to a
-- COMPLETED transaction:
--   * a purchase  (purchase_requests.status = 'completed')  — buyer↔seller
--   * a rental    (vehicle_returns.status  = 'completed')   — driver↔owner
--
-- Subject is polymorphic (car or user) via two nullable FKs + a shape CHECK,
-- so both "vehicle rating" and "driver/owner rating" live in one table.
-- One review per (transaction, author, subject kind): a buyer can rate the
-- car once and the seller once for the same purchase, and can never
-- double-rate either. Aggregates (AVG + COUNT) are computed off the two
-- partial subject indexes below — no per-row loops, no denormalized counters.

CREATE TABLE IF NOT EXISTS reviews (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    author_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Polymorphic subject: exactly one of the two FKs, matching subject_type.
    subject_type     VARCHAR(10) NOT NULL CHECK (subject_type IN ('car', 'user')),
    subject_car_id   UUID REFERENCES cars(id)  ON DELETE CASCADE,
    subject_user_id  UUID REFERENCES users(id) ON DELETE CASCADE,

    -- The completed transaction this review is anchored to. transaction_id
    -- points at purchase_requests.id or vehicle_returns.id depending on
    -- transaction_type; validity is enforced by the create path, which
    -- resolves the transaction and derives the allowed subjects from it.
    transaction_type VARCHAR(10) NOT NULL CHECK (transaction_type IN ('purchase', 'rental')),
    transaction_id   UUID NOT NULL,

    rating           SMALLINT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment          TEXT,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT reviews_subject_shape CHECK (
        (subject_type = 'car'  AND subject_car_id IS NOT NULL AND subject_user_id IS NULL) OR
        (subject_type = 'user' AND subject_user_id IS NOT NULL AND subject_car_id IS NULL)
    )
);

-- One review per author per subject-kind per transaction.
CREATE UNIQUE INDEX IF NOT EXISTS reviews_once_per_transaction_idx
    ON reviews (transaction_type, transaction_id, author_id, subject_type);

-- Aggregate lookups: AVG/COUNT by subject.
CREATE INDEX IF NOT EXISTS idx_reviews_subject_car
    ON reviews (subject_car_id) WHERE subject_car_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_reviews_subject_user
    ON reviews (subject_user_id) WHERE subject_user_id IS NOT NULL;

-- Reuse the shared update_updated_at_column() defined in 000001.
DROP TRIGGER IF EXISTS update_reviews_updated_at ON reviews;
CREATE TRIGGER update_reviews_updated_at
    BEFORE UPDATE ON reviews
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
