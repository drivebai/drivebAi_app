-- Product-tour ("onboarding") progress persistence.
--
-- Namespace note: this is the ProductTour system's server-side store, not the
-- signup-flow onboarding_status ENUM on users/user_profiles. A table name
-- cannot collide with a Swift enum or Go type, so per the deliverable spec it
-- keeps the human word "onboarding" while every Go/Swift symbol uses the
-- ProductTour namespace.
--
-- One row per (user, tour_key). A user reads/writes ONLY their own rows —
-- every endpoint keys off the authenticated user id from the JWT and there is
-- no user_id path/body parameter to spoof.
CREATE TABLE IF NOT EXISTS user_onboarding_progress (
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tour_key   TEXT        NOT NULL,
    status     TEXT        NOT NULL DEFAULT 'completed'
                   CHECK (status IN ('in_progress', 'completed', 'skipped')),
    step       INTEGER     NOT NULL DEFAULT 0 CHECK (step >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, tour_key)
);

-- Fast lookup of all of a user's tour rows (the GET path).
CREATE INDEX IF NOT EXISTS user_onboarding_progress_user_idx
    ON user_onboarding_progress (user_id);
