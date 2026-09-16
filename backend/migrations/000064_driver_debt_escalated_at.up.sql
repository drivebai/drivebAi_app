-- A claim stamp so an orphan debt is escalated to a human exactly once.
--
-- BlockingBalanceFor now refuses to block on a debt the driver cannot pay
-- (a debt whose cycle has left 'arrears_due' — settled, waived or refunded —
-- while the debt row stayed open). That removes the user-facing trap, but it
-- turns the orphan into something worse if nothing watches it: a silent
-- ledger lie. Open debt, settled cycle, no lister, money we believe is owed
-- that nothing can collect. The sweep doctrine's rule is that floors need a
-- voice.
--
-- The stamp is load-bearing for a second, concrete reason. CreateSystemTicket
-- dedupes on lease_request_id among LIVE tickets only
-- (uq_support_tickets_overdue_lease, migration 000048). Without a per-debt
-- claim the inverse sweep would either storm a ticket every 60 seconds or
-- have its ticket silently swallowed by the arrears ticket already open on
-- that same lease — and swallowed is worse, because it looks like success.
ALTER TABLE driver_debts ADD COLUMN IF NOT EXISTS escalated_at TIMESTAMPTZ;

-- Partial index: the sweep only ever asks for unescalated open debts.
CREATE INDEX IF NOT EXISTS idx_driver_debts_unescalated
    ON driver_debts (opened_at)
    WHERE status = 'open' AND escalated_at IS NULL;
