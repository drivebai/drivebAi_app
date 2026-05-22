ALTER TABLE support_chats
    DROP COLUMN IF EXISTS admin_last_read_at,
    DROP COLUMN IF EXISTS user_last_read_at;
