-- Owner payouts (Stripe Connect, separate charges & transfers).
--
-- Money design: the driver's charge always lands whole in the PLATFORM
-- balance; the owner's share moves later as a Transfer when the rental
-- reaches a settled end (return completed, or an admin settlement). This
-- ledger is the single record of every owner share: what it is, why, and
-- whether it has actually moved. One row per lease (UNIQUE) — a rental's
-- money settles exactly once.
--
-- users gains the connected-account mirror: the Stripe account id and a
-- coarse local status the app renders from, refreshed by the Connect
-- webhook (account.updated) and on-demand reads.

ALTER TABLE users
    ADD COLUMN stripe_account_id TEXT,
    ADD COLUMN payout_status VARCHAR(24) NOT NULL DEFAULT 'none',
    ADD COLUMN payout_requirements JSONB,
    ADD COLUMN payout_status_updated_at TIMESTAMPTZ;

CREATE UNIQUE INDEX uq_users_stripe_account
    ON users(stripe_account_id)
    WHERE stripe_account_id IS NOT NULL;

CREATE TABLE owner_payouts (
    id                 UUID PRIMARY KEY,
    lease_request_id   UUID NOT NULL UNIQUE REFERENCES lease_requests(id) ON DELETE RESTRICT,
    owner_id           UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    stripe_account_id  TEXT,

    -- The split, frozen at settlement time. gross_kept = what the driver's
    -- payment left after refunds; fee = floor(gross_kept * bps / 10000);
    -- owner_amount = gross_kept - fee (the owner receives the rounding
    -- remainder — no cent is ever lost or invented).
    gross_kept_cents   BIGINT NOT NULL,
    fee_bps            INT    NOT NULL,
    fee_cents          BIGINT NOT NULL,
    owner_amount_cents BIGINT NOT NULL,
    currency           VARCHAR(3) NOT NULL DEFAULT 'USD',

    -- awaiting_onboarding: owner not payout-ready; executes automatically
    --   when account.updated says ready (escrow — time-bounded via the
    --   reminder/escalation columns below).
    -- pending: ready to transfer (or transfer in flight / to retry).
    -- paid: Transfer created and accepted by Stripe.
    -- failed: Transfer rejected; sweep retries with the same idempotency key.
    -- withheld: deliberate admin decision NOT to pay, note required.
    status             VARCHAR(24) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('awaiting_onboarding','pending','paid','failed','withheld')),

    -- return_completed: the normal path (vehicle return finalized).
    -- admin_settlement: a rental that ended without a clean return, settled
    --   deliberately by an admin (close / payout_only / withhold).
    source             VARCHAR(24) NOT NULL
        CHECK (source IN ('return_completed','admin_settlement')),

    source_charge_id   TEXT,
    stripe_transfer_id TEXT,
    failure_reason     TEXT,
    note               TEXT,

    -- Escrow aging (claimed-once ledger, house pattern): reminders while
    -- awaiting_onboarding, one escalation ticket when stale.
    reminder_count     INT NOT NULL DEFAULT 0,
    last_reminder_at   TIMESTAMPTZ,
    escalated_at       TIMESTAMPTZ,

    paid_at            TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_owner_payouts_status ON owner_payouts(status);
CREATE INDEX idx_owner_payouts_owner  ON owner_payouts(owner_id);
