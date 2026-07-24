-- Migration 000039: support tickets
--
-- A support ticket is a STRUCTURED support request — distinct from the free-text
-- support_chats conversation (000015). A user picks a category, describes the
-- problem, and attaches evidence; the request then becomes a discrete, trackable
-- item in the admin queue. The conversation about it still happens in the user's
-- one support_chats thread (linked by user_id), which is why there is no
-- messages table here — this is the record, the chat is the reply channel.
--
-- Modeled as a lighter sibling of the accidents module (000021): a server-side
-- draft born on open, evidence uploaded against the draft id, then a submit
-- transition. category is a real column (not a bare int like accident.diagram)
-- so the admin queue can triage by it.

CREATE TABLE support_tickets (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category     VARCHAR(40) NOT NULL DEFAULT '',
    subject      TEXT        NOT NULL DEFAULT '',
    description  TEXT        NOT NULL DEFAULT '',
    -- Lifecycle: draft -> open -> resolved -> closed (reopen: resolved -> open)
    status       VARCHAR(20) NOT NULL DEFAULT 'draft',
    -- Wizard resume cursor: which step the user last reached (accidents lost this).
    last_step    INT         NOT NULL DEFAULT 0,
    submitted_at TIMESTAMPTZ,
    resolved_at  TIMESTAMPTZ,
    closed_at    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_support_tickets_user_id ON support_tickets(user_id);
CREATE INDEX idx_support_tickets_status  ON support_tickets(status);

CREATE TABLE support_ticket_attachments (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id  UUID        NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    file_url   TEXT        NOT NULL,
    file_path  TEXT        NOT NULL,
    file_size  BIGINT,
    mime_type  VARCHAR(100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_support_ticket_attachments_ticket_id ON support_ticket_attachments(ticket_id);
