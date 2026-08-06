-- Ticket rows must go before the checks re-tighten.
DELETE FROM reviews WHERE subject_type = 'ticket';

DROP INDEX IF EXISTS idx_support_tickets_needs_followup;
ALTER TABLE support_tickets DROP COLUMN IF EXISTS needs_followup;

ALTER TABLE reviews DROP CONSTRAINT IF EXISTS reviews_ticket_comment_rule;
DROP INDEX IF EXISTS reviews_one_per_ticket_idx;

ALTER TABLE reviews DROP CONSTRAINT IF EXISTS reviews_subject_shape;
ALTER TABLE reviews ADD CONSTRAINT reviews_subject_shape CHECK (
    (subject_type = 'car'  AND subject_car_id IS NOT NULL AND subject_user_id IS NULL) OR
    (subject_type = 'user' AND subject_user_id IS NOT NULL AND subject_car_id IS NULL)
);

ALTER TABLE reviews DROP CONSTRAINT IF EXISTS reviews_transaction_type_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_transaction_type_check
    CHECK (transaction_type IN ('purchase', 'rental'));

ALTER TABLE reviews DROP CONSTRAINT IF EXISTS reviews_subject_type_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_subject_type_check
    CHECK (subject_type IN ('car', 'user'));

ALTER TABLE reviews DROP COLUMN IF EXISTS subject_ticket_id;
