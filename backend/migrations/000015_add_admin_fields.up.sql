-- Migration 000015: admin moderation fields
-- Adds:
--   - cars.is_approved        : new listings need admin approval before showing in Discover
--   - users.is_blocked        : admin can block users (block prevents login)
--   - users.blocked_at        : audit timestamp

ALTER TABLE cars
    ADD COLUMN IF NOT EXISTS is_approved BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_cars_is_approved
    ON cars(is_approved) WHERE is_approved = TRUE;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS is_blocked BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS blocked_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_users_is_blocked
    ON users(is_blocked) WHERE is_blocked = TRUE;

-- Backfill: existing cars are auto-approved so we don't break iOS
UPDATE cars SET is_approved = TRUE WHERE is_approved = FALSE;

-- ===== Support chats (user ↔ admin) =====
-- Independent of car-based chats: a user can talk to support without a listing context.

CREATE TABLE IF NOT EXISTS support_chats (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_message_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_support_chats_user UNIQUE (user_id)
);

CREATE INDEX IF NOT EXISTS idx_support_chats_user_id ON support_chats(user_id);

CREATE TABLE IF NOT EXISTS support_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    support_chat_id UUID NOT NULL REFERENCES support_chats(id) ON DELETE CASCADE,
    sender_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- 'user' = the customer | 'admin' = staff response
    sender_kind     VARCHAR(10) NOT NULL CHECK (sender_kind IN ('user','admin')),
    body            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_support_messages_chat_created
    ON support_messages(support_chat_id, created_at);

-- Re-use existing trigger function from migration 000001
CREATE TRIGGER trigger_support_chats_updated_at
    BEFORE UPDATE ON support_chats
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

