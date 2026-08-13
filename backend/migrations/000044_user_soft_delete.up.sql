-- 000044_user_soft_delete
--
-- Admin account deletion is ANONYMIZE-IN-PLACE, not row removal (batch
-- item 3). A hard DELETE is blocked by RESTRICT for any user with purchase
-- history and, where it isn't blocked, cascades away counterparties' chats,
-- payments (the only local copy of Stripe payment-intent ids) and refund
-- audit trails. The tombstone instead: frees the email (renamed) and phone
-- (NULLed — both uniqueness mechanisms provably release them), blanks the
-- name, blocks the account, and stamps deleted_at. Transaction history
-- stays intact, attributed to "Deleted User".

ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;
