-- Owner-side terms for rolling (weekly auto-renewing) rentals.
--
-- The driver's consent has been recorded verbatim since 000055; the OWNER's
-- agreement to how they are paid, what happens when a driver's card fails,
-- and what the capped guarantee does and does not cover never had a row.
-- An owner accepting a rolling lease request must have accepted the current
-- owner package first (gate in handleLeaseAction); the acceptance is
-- recorded verbatim, once per version, from the app or by an admin on the
-- owner's behalf (with a note saying how it was obtained).
CREATE TABLE owner_terms_acceptances (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    terms_version TEXT NOT NULL,
    terms_text    TEXT NOT NULL,
    channel       TEXT NOT NULL CHECK (channel IN ('app', 'admin')),
    recorded_by   UUID REFERENCES users(id) ON DELETE SET NULL,
    note          TEXT,
    accepted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_owner_terms_acceptances_version UNIQUE (owner_id, terms_version)
);
CREATE INDEX idx_owner_terms_acceptances_owner ON owner_terms_acceptances(owner_id);
