-- Any reversed rows must re-label before the narrow CHECK returns;
-- 'withheld' is the closest legacy meaning (money deliberately not with
-- the owner, note explains why).
UPDATE owner_payouts SET status = 'withheld' WHERE status = 'reversed';

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_status_check;
ALTER TABLE owner_payouts ADD CONSTRAINT owner_payouts_status_check
    CHECK (status IN ('awaiting_onboarding', 'pending', 'paid', 'failed', 'withheld'));

ALTER TABLE owner_payouts
    DROP COLUMN IF EXISTS reversal_reason,
    DROP COLUMN IF EXISTS reversed_at,
    DROP COLUMN IF EXISTS reversed_amount_cents,
    DROP COLUMN IF EXISTS stripe_reversal_id;

DROP INDEX IF EXISTS idx_charge_disputes_lease;
DROP TABLE IF EXISTS charge_disputes;
