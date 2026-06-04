-- Migration 000023: allow 'attachment' as a first-class message type.
--
-- Why: chat attachments uploaded via POST /chats/{chatId}/attachments need a
-- corresponding row in the messages table so they survive refresh and broadcast
-- via WebSocket like text messages do. The original `message_type` ENUM only
-- permits ('text','system') and cannot be extended inside a transaction (which
-- golang-migrate uses). We convert messages.type to VARCHAR(20) with a CHECK
-- constraint that preserves the domain (matching the codebase's post-accidents
-- VARCHAR-for-status convention).
--
-- The old `message_type` ENUM type is intentionally NOT dropped — it is left in
-- the catalog as dead schema so any external tooling that referenced it does
-- not break. Future migrations should use the CHECK below instead of touching
-- the dead enum.
--
-- NOTE: ALTER COLUMN TYPE rewrites the messages table and takes an
-- ACCESS EXCLUSIVE lock for the duration. On a busy chat DB this can lock
-- chat APIs for the rewrite. Prefer a low-traffic deploy window.

ALTER TABLE messages
    ALTER COLUMN type DROP DEFAULT,
    ALTER COLUMN type TYPE VARCHAR(20) USING type::text,
    ALTER COLUMN type SET DEFAULT 'text';

ALTER TABLE messages
    ADD CONSTRAINT messages_type_check
    CHECK (type IN ('text', 'system', 'attachment'));

-- Backfill: every chat attachment created before this migration was stored
-- with message_id IS NULL (the old UploadAttachment handler did not create a
-- linked message). Now that ListMessages joins attachments by message_id,
-- those orphan rows would silently disappear from the chat scrollback. Walk
-- the orphans and synthesize one attachment-message per orphan, preserving
-- the original chat_id, uploader_id, and created_at so the message slots
-- back into the timeline at its original position.
--
-- Request-only attachments (chat_id IS NULL OR request_id IS NOT NULL) are
-- skipped — they belong to a request, not a chat message stream.
--
-- The CTE form runs as ONE statement: the `orphans` CTE is materialized (it's
-- referenced by both the data-modifying `inserted` CTE and the main UPDATE),
-- so each row gets a stable `new_msg_id`. The `inserted` CTE is data-modifying
-- and is guaranteed to execute even though the main UPDATE does not reference
-- it directly. Both INSERT into messages and UPDATE on attachments see a
-- single snapshot, so there are no row-by-row round trips and the messages
-- table is released for normal queries as soon as the migration commits.
WITH orphans AS (
    SELECT id, chat_id, uploader_id, filename, created_at,
           gen_random_uuid() AS new_msg_id
    FROM attachments
    WHERE message_id IS NULL
      AND request_id IS NULL
      AND chat_id IS NOT NULL
),
inserted AS (
    INSERT INTO messages (id, chat_id, sender_id, type, body, client_message_id, created_at)
    SELECT new_msg_id,
           chat_id,
           uploader_id,
           'attachment',
           '📎 ' || filename,
           gen_random_uuid(),
           created_at
    FROM orphans
    RETURNING id
)
UPDATE attachments AS a
SET message_id = o.new_msg_id
FROM orphans o
WHERE a.id = o.id;
