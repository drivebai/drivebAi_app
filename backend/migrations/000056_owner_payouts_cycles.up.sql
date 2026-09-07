-- Batch 2, part 3: per-cycle payout ledger (design §3, arrears model).
-- The legacy settle-once invariant is preserved EXACTLY via a partial
-- unique index on rows without a cycle; rolling rows settle once per cycle
-- via the second partial index. One ledger, one earnings screen, one sweep.

ALTER TABLE owner_payouts
    ADD COLUMN billing_cycle_id UUID REFERENCES billing_cycles(id),
    ADD COLUMN period_start     TIMESTAMPTZ,
    ADD COLUMN period_end       TIMESTAMPTZ,
    ADD COLUMN consumed_at      TIMESTAMPTZ;

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_lease_request_id_key;
CREATE UNIQUE INDEX uq_owner_payouts_legacy
    ON owner_payouts (lease_request_id)
    WHERE billing_cycle_id IS NULL;
CREATE UNIQUE INDEX uq_owner_payouts_cycle
    ON owner_payouts (billing_cycle_id)
    WHERE billing_cycle_id IS NOT NULL;

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_status_check;
ALTER TABLE owner_payouts ADD CONSTRAINT owner_payouts_status_check
    CHECK (status IN ('accruing', 'awaiting_onboarding', 'pending', 'paid',
                      'failed', 'withheld', 'reversed', 'voided'));

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_source_check;
ALTER TABLE owner_payouts ADD CONSTRAINT owner_payouts_source_check
    CHECK (source IN ('return_completed', 'admin_settlement', 'cycle_consumed'));

-- The promotion sweep scans accruing rows by period_end.
CREATE INDEX idx_owner_payouts_accruing
    ON owner_payouts (period_end)
    WHERE status = 'accruing';
