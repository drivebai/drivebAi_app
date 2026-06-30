-- Revert migration 000030: drop the vehicle_returns table + its supporting
-- column on lease_requests. Refunds already issued at Stripe are NOT
-- reversed by this — only the local audit row is lost. Do not roll back
-- after a production return has succeeded; ship a forward-fix instead.
ALTER TABLE lease_requests
    DROP COLUMN IF EXISTS vehicle_returned_at;

DROP INDEX IF EXISTS idx_vehicle_returns_stuck_refunds;
DROP INDEX IF EXISTS idx_vehicle_returns_status;
DROP INDEX IF EXISTS idx_vehicle_returns_driver_id;
DROP INDEX IF EXISTS idx_vehicle_returns_owner_id;
DROP TABLE IF EXISTS vehicle_returns;
