-- Return-dispute operations (lifecycle batch, defect 4 + audit findings).
--
-- 1. support_tickets gains two nullable entity links so system-created
--    tickets are findable from the thing they are about. Discipline:
--    a DISPUTE ticket sets vehicle_return_id (only); an OVERDUE-rental
--    escalation ticket sets lease_request_id (only). The two partial unique
--    indexes make programmatic creation idempotent — a scanner re-run or a
--    double-fired dispute can never open a second ticket for the same thing.
--    ON DELETE SET NULL: a ticket must survive its subject being deleted.
--
-- 2. vehicle_returns.resolution_note persists the admin's required note on
--    accept/reject — AdminResolve previously decoded body.Note and dropped
--    it on the floor.
--
-- 3. vehicle_returns.owner_reminder_sent_at is the idempotency flag for the
--    stale-return owner reminder (a driver_initiated return the owner has
--    ignored) — same claim-under-IS NULL pattern as the term scanner flags.

ALTER TABLE support_tickets
    ADD COLUMN lease_request_id UUID REFERENCES lease_requests(id) ON DELETE SET NULL,
    ADD COLUMN vehicle_return_id UUID REFERENCES vehicle_returns(id) ON DELETE SET NULL;

-- Scoped to LIVE tickets: a resolved/closed ticket must not block a new one
-- for the same entity — a return can be disputed again after a revive, and
-- conflating "was ever ticketed" with "has an open ticket" would make the
-- second dispute silently ticketless.
CREATE UNIQUE INDEX uq_support_tickets_vehicle_return
    ON support_tickets(vehicle_return_id)
    WHERE vehicle_return_id IS NOT NULL AND status NOT IN ('resolved', 'closed');

CREATE UNIQUE INDEX uq_support_tickets_overdue_lease
    ON support_tickets(lease_request_id)
    WHERE lease_request_id IS NOT NULL AND status NOT IN ('resolved', 'closed');

ALTER TABLE vehicle_returns
    ADD COLUMN resolution_note TEXT,
    ADD COLUMN owner_reminder_sent_at TIMESTAMPTZ;
