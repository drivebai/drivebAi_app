-- Mode profiles: a user (identity) can have one profile per non-admin role.
-- Enables a single email to act as both Car Owner and Driver without re-registering.

CREATE TABLE user_profiles (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role user_role NOT NULL,
    onboarding_status onboarding_status NOT NULL DEFAULT 'role_selected',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, role),
    CONSTRAINT user_profiles_role_check CHECK (role IN ('driver', 'car_owner'))
);

CREATE INDEX idx_user_profiles_user_id ON user_profiles(user_id);

CREATE TRIGGER update_user_profiles_updated_at
    BEFORE UPDATE ON user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();

-- Active profile pointer on users. Nullable so the FK can be added before backfill.
ALTER TABLE users ADD COLUMN active_profile_id UUID REFERENCES user_profiles(id);

-- Backfill: every existing non-admin user gets ONE profile matching their
-- current role + onboarding_status, which becomes their active profile.
-- The second profile is created lazily on first mode switch.
INSERT INTO user_profiles (user_id, role, onboarding_status, created_at, updated_at)
SELECT id, role, onboarding_status, created_at, updated_at
FROM users
WHERE role IN ('driver', 'car_owner');

UPDATE users u
SET active_profile_id = p.id
FROM user_profiles p
WHERE p.user_id = u.id AND p.role = u.role;
