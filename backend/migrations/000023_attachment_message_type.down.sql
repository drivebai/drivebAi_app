-- WARNING: lossy. Once any 'attachment'-typed message exists, reverting
-- messages.type to the original message_type ENUM (labels: 'text','system')
-- would otherwise fail at the cast. We delete those rows first; the
-- linked rows in `attachments` have ON DELETE SET NULL on `message_id`,
-- so the files themselves stay accessible via /chats/{id}/attachments
-- (Shared Files) but disappear from the per-message stream.

DELETE FROM messages WHERE type = 'attachment';

ALTER TABLE messages
    DROP CONSTRAINT IF EXISTS messages_type_check;

ALTER TABLE messages
    ALTER COLUMN type DROP DEFAULT,
    ALTER COLUMN type TYPE message_type USING type::message_type,
    ALTER COLUMN type SET DEFAULT 'text'::message_type;
