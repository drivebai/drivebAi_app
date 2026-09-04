ALTER TABLE lease_requests DROP COLUMN IF EXISTS payment_pending_at;

-- Any 'unrecoverable' rows must be re-labelled before the narrower CHECK
-- can be restored; 'failed' returns them to the retry sweep.
UPDATE vehicle_returns SET refund_status = 'failed' WHERE refund_status = 'unrecoverable';

ALTER TABLE vehicle_returns
    DROP CONSTRAINT IF EXISTS vehicle_returns_refund_status_check;
ALTER TABLE vehicle_returns
    ADD CONSTRAINT vehicle_returns_refund_status_check
    CHECK (refund_status IS NULL OR refund_status IN ('pending', 'succeeded', 'failed', 'not_applicable'));

ALTER TABLE vehicle_returns
    DROP COLUMN IF EXISTS refund_delay_notified_at;
