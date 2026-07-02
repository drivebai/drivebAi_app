DROP INDEX IF EXISTS idx_cars_reserved_by_purchase_request_id;
ALTER TABLE cars DROP COLUMN IF EXISTS reserved_by_purchase_request_id;

-- Revert the cars.status conversion. Any rows now sitting at 'sold' will
-- fail the cast back to the enum — the down migration is best-effort for
-- fresh test databases; production rollback requires purging 'sold' rows
-- first.
ALTER TABLE cars DROP CONSTRAINT IF EXISTS cars_status_check;

DROP INDEX IF EXISTS idx_prej_evidence_rejection;
DROP TABLE IF EXISTS purchase_rejection_evidence;

DROP TRIGGER IF EXISTS set_purchase_rejections_updated_at ON purchase_rejections;
DROP INDEX IF EXISTS idx_purchase_rejections_status;
DROP TABLE IF EXISTS purchase_rejections;

DROP TRIGGER IF EXISTS set_purchase_bos_updated_at ON purchase_bill_of_sales;
DROP TABLE IF EXISTS purchase_bill_of_sales;

DROP TRIGGER IF EXISTS set_purchase_requests_updated_at ON purchase_requests;
DROP INDEX IF EXISTS idx_purchase_requests_auth_expiry;
DROP INDEX IF EXISTS idx_purchase_requests_expires_at;
DROP INDEX IF EXISTS idx_purchase_requests_chat_id;
DROP INDEX IF EXISTS idx_purchase_requests_buyer_status;
DROP INDEX IF EXISTS idx_purchase_requests_seller_status;
DROP INDEX IF EXISTS idx_purchase_requests_active_unique;
DROP TABLE IF EXISTS purchase_requests;

DROP TYPE IF EXISTS purchase_rejection_status;
DROP TYPE IF EXISTS purchase_rejection_reason;
DROP TYPE IF EXISTS purchase_request_status;
