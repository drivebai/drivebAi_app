DROP INDEX IF EXISTS idx_login_otps_message_id;
ALTER TABLE login_otps DROP COLUMN IF EXISTS message_id;
