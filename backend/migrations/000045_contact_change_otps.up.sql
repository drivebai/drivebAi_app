-- 000045_contact_change_otps
--
-- OTP-confirmed email/phone changes (batch items 7+8). A SEPARATE table
-- from login_otps on purpose: login_otps is keyed by bare email with no
-- purpose column, and its verify path mints a REGISTRATION token for
-- unknown addresses — reusing it for change-codes would let the two flows
-- consume each other's codes. This table is user-bound and single-purpose.
--
-- One pending change per user at a time (latest unconsumed wins; issuing a
-- new code supersedes the old row the same way login OTPs do). Delivery is
-- EMAIL-ONLY today: a new-email code goes to the new address (proves
-- ownership); a phone-change code goes to the account's current email
-- (proves account control — SMS possession-proof needs an SMS provider we
-- don't have).

CREATE TABLE contact_change_otps (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    field      VARCHAR(10) NOT NULL CHECK (field IN ('email', 'phone')),
    new_value  TEXT NOT NULL,
    code_hash  VARCHAR(255) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    attempts   INT NOT NULL DEFAULT 0,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_contact_change_otps_user
    ON contact_change_otps (user_id, created_at DESC);
