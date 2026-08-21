DROP INDEX IF EXISTS idx_lease_requests_rental_ends;

ALTER TABLE lease_requests
    DROP COLUMN IF EXISTS rental_ends_at,
    DROP COLUMN IF EXISTS term_ending_notified_at,
    DROP COLUMN IF EXISTS overdue_notified_at,
    DROP COLUMN IF EXISTS overdue_escalated_at;
