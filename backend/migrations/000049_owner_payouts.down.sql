DROP TABLE IF EXISTS owner_payouts;

DROP INDEX IF EXISTS uq_users_stripe_account;

ALTER TABLE users
    DROP COLUMN IF EXISTS stripe_account_id,
    DROP COLUMN IF EXISTS payout_status,
    DROP COLUMN IF EXISTS payout_requirements,
    DROP COLUMN IF EXISTS payout_status_updated_at;
