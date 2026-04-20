ALTER TABLE users DROP COLUMN IF EXISTS active_profile_id;
DROP TRIGGER IF EXISTS update_user_profiles_updated_at ON user_profiles;
DROP INDEX IF EXISTS idx_user_profiles_user_id;
DROP TABLE IF EXISTS user_profiles;
