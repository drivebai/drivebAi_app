-- 000042_ticket_ratings
--
-- Support-ticket ratings live in the SAME polymorphic reviews table as car
-- and user ratings (000038) — a third subject arm, not a parallel table.
-- Every existing aggregate consumer filters on subject_car_id /
-- subject_user_id, which stay NULL on ticket rows, so ticket ratings are
-- automatically invisible to car/owner averages with zero query changes.
--
-- Anchoring keeps the 000038 invariant: a ticket rating's "transaction" IS
-- the ticket (transaction_type = 'support', transaction_id = the ticket id).
--
-- Client rules (7/24 item 3f), enforced at the DB where possible:
--   * one rating per ticket           -> reviews_one_per_ticket_idx
--   * 4★ and below require a comment  -> reviews_ticket_comment_rule
--   * 3★ and below flag the ticket    -> support_tickets.needs_followup
--     (set in the same transaction as the insert, by the create path)

ALTER TABLE reviews
    ADD COLUMN subject_ticket_id UUID REFERENCES support_tickets(id) ON DELETE CASCADE;

-- Widen the two enums. The inline column CHECKs from 000038 carry Postgres'
-- auto-generated names (<table>_<column>_check).
ALTER TABLE reviews DROP CONSTRAINT reviews_subject_type_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_subject_type_check
    CHECK (subject_type IN ('car', 'user', 'ticket'));

ALTER TABLE reviews DROP CONSTRAINT reviews_transaction_type_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_transaction_type_check
    CHECK (transaction_type IN ('purchase', 'rental', 'support'));

-- Three-arm subject shape: exactly one subject FK, matching subject_type.
ALTER TABLE reviews DROP CONSTRAINT reviews_subject_shape;
ALTER TABLE reviews ADD CONSTRAINT reviews_subject_shape CHECK (
    (subject_type = 'car'    AND subject_car_id IS NOT NULL AND subject_user_id IS NULL     AND subject_ticket_id IS NULL) OR
    (subject_type = 'user'   AND subject_user_id IS NOT NULL AND subject_car_id IS NULL     AND subject_ticket_id IS NULL) OR
    (subject_type = 'ticket' AND subject_ticket_id IS NOT NULL AND subject_car_id IS NULL   AND subject_user_id IS NULL)
);

-- Strictly ONE rating per ticket, regardless of author. (The 000038
-- composite index only gives one-per-author; a ticket has one reporter, and
-- this closes the gap at the DB layer.)
CREATE UNIQUE INDEX reviews_one_per_ticket_idx
    ON reviews (subject_ticket_id) WHERE subject_ticket_id IS NOT NULL;

-- 4★ and below require a non-blank comment on ticket ratings. The create
-- path already normalizes empty/whitespace comments to NULL, which is what
-- makes this CHECK reliable. (Friendly errors live in the handler.)
ALTER TABLE reviews ADD CONSTRAINT reviews_ticket_comment_rule CHECK (
    subject_type <> 'ticket' OR rating = 5 OR (comment IS NOT NULL AND btrim(comment) <> '')
);

-- 3★ and below flag the ticket for admin follow-up.
ALTER TABLE support_tickets
    ADD COLUMN needs_followup BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX idx_support_tickets_needs_followup
    ON support_tickets (needs_followup) WHERE needs_followup;
