-- Migration 000040: support message attachments
--
-- Part 2 of the support system: in-chat photos and files. A support message
-- (000015) can now carry evidence — a screenshot the user snapped, a document
-- they picked, or a screenshot support sends back ("tap here"). Each row is one
-- file hung off one support_messages row; a message may have a body, an
-- attachment, or both.
--
-- Storage mirrors ticket evidence: the file lives on the private /uploads path
-- (/uploads/support/{chatID}/...), classified private-by-default, and is served
-- only through a signed, short-lived URL. file_path is the on-disk location
-- (for delete/rollback); file_url is the public relative path that gets signed
-- on the way out.

CREATE TABLE support_message_attachments (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id UUID         NOT NULL REFERENCES support_messages(id) ON DELETE CASCADE,
    file_url   TEXT         NOT NULL,
    file_path  TEXT         NOT NULL,
    file_size  BIGINT       NOT NULL DEFAULT 0,
    mime_type  VARCHAR(100) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_support_msg_attachments_message_id ON support_message_attachments(message_id);
