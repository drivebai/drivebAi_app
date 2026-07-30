-- Migration 000041: phone uniqueness (client item A3)
--
-- Phones mirror the email/VIN pattern: DB-level uniqueness + a pre-flight
-- availability endpoint + inline client UX. The index is PARTIAL on
-- E.164-shaped values only: legacy junk ("abc", free-text) and NULL/empty
-- phones are exempt until the owner next edits their profile, at which point
-- validation forces E.164 and the constraint applies. Same partial-index
-- philosophy as cars' VIN index (000035).
--
-- Plain CREATE (not CONCURRENTLY): the users table is tiny; staying inside
-- golang-migrate's transaction beats lock-avoidance complexity at this size.

CREATE UNIQUE INDEX users_phone_unique_idx
    ON users (phone)
    WHERE phone IS NOT NULL AND phone ~ '^\+[1-9][0-9]{6,14}$';
