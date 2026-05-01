DROP TABLE IF EXISTS support_messages;
DROP TABLE IF EXISTS support_chats;

DROP INDEX IF EXISTS idx_users_is_blocked;
DROP INDEX IF EXISTS idx_cars_is_approved;

ALTER TABLE users DROP COLUMN IF EXISTS blocked_at;
ALTER TABLE users DROP COLUMN IF EXISTS is_blocked;

ALTER TABLE cars DROP COLUMN IF EXISTS is_approved;
