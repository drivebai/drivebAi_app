-- Migration 000026: per-user dismissal of terminal key-handover cards.
--
-- After the pickup deadline elapses and the lease moves to expired_refunded,
-- the Today tab keeps showing the handover card with a "Pickup deadline
-- missed — payment refunded" notice. Each party (owner + driver) can tap
-- "Got it" to acknowledge it; this table records that ack so the row stops
-- being included in their Today list across app restarts.

CREATE TABLE key_handover_dismissals (
    key_handover_id UUID        NOT NULL REFERENCES key_handovers(id) ON DELETE CASCADE,
    user_id         UUID        NOT NULL REFERENCES users(id)         ON DELETE CASCADE,
    dismissed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (key_handover_id, user_id)
);

-- Drives the WHERE NOT EXISTS subquery in ListActiveForUser.
CREATE INDEX idx_key_handover_dismissals_user
    ON key_handover_dismissals(user_id);
