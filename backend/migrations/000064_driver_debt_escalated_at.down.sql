DROP INDEX IF EXISTS idx_driver_debts_unescalated;
ALTER TABLE driver_debts DROP COLUMN IF EXISTS escalated_at;
