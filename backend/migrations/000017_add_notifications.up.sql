-- Migration 000017: in-app notifications feed
-- Stores per-user notification records created on key events:
--   - lease_request_created  → notify owner
--   - lease_request_paid     → notify owner (+ optionally driver)

CREATE TABLE IF NOT EXISTS notifications (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type                    VARCHAR(50) NOT NULL,
    title                   TEXT NOT NULL,
    body                    TEXT NOT NULL,
    related_chat_id         UUID NULL REFERENCES chats(id) ON DELETE SET NULL,
    related_lease_request_id UUID NULL REFERENCES lease_requests(id) ON DELETE SET NULL,
    is_read                 BOOLEAN NOT NULL DEFAULT FALSE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_notifications_user_created
    ON notifications(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_notifications_user_unread
    ON notifications(user_id, is_read) WHERE is_read = FALSE;
