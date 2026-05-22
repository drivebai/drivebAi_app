-- Migration 000020: support chat read-tracking
-- Adds per-side "last read" timestamps so we can compute unread counts for both
-- the admin panel (messages from users they haven't seen) and the mobile app
-- (admin replies the user hasn't opened yet).

ALTER TABLE support_chats
    ADD COLUMN IF NOT EXISTS admin_last_read_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS user_last_read_at  TIMESTAMPTZ;
