ALTER TABLE vehicle_returns
    DROP COLUMN IF EXISTS resolution_note,
    DROP COLUMN IF EXISTS owner_reminder_sent_at;

DROP INDEX IF EXISTS uq_support_tickets_overdue_lease;
DROP INDEX IF EXISTS uq_support_tickets_vehicle_return;

ALTER TABLE support_tickets
    DROP COLUMN IF EXISTS lease_request_id,
    DROP COLUMN IF EXISTS vehicle_return_id;
