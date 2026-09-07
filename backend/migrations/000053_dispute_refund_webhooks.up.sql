-- Batch 1 of the rolling-billing plan (also closes audit M2 for the
-- EXISTING product): card disputes and out-of-band refunds become visible
-- and actionable instead of silently debiting the platform.
--
-- charge_disputes mirrors the Stripe dispute lifecycle one row per dispute
-- (stripe_dispute_id UNIQUE = idempotent webhook redelivery). Policy
-- (design §5): on OPEN, withhold this lease's UNPAID payout rows and open a
-- ticket — never claw back; on LOST, partially reverse exactly the paid
-- transfer; on WON, release what was withheld.

CREATE TABLE charge_disputes (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    stripe_dispute_id  TEXT NOT NULL UNIQUE,
    stripe_charge_id   TEXT NOT NULL,
    payment_intent_id  TEXT,
    lease_request_id   UUID REFERENCES lease_requests(id),
    amount_cents       BIGINT NOT NULL,
    currency           TEXT NOT NULL DEFAULT 'usd',
    reason             TEXT,
    status             TEXT NOT NULL,             -- Stripe's own status string
    outcome            TEXT CHECK (outcome IN ('won', 'lost', 'refunded')),
    ticket_id          UUID REFERENCES support_tickets(id),
    payouts_withheld   BOOLEAN NOT NULL DEFAULT FALSE,
    reversal_done      BOOLEAN NOT NULL DEFAULT FALSE,
    outcome_settled    BOOLEAN NOT NULL DEFAULT FALSE,  -- ALL closure side effects completed
    closed_at          TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_charge_disputes_lease ON charge_disputes(lease_request_id)
    WHERE lease_request_id IS NOT NULL;

-- owner_payouts learns to record a clawback (design §3 / 000055 subset
-- pulled forward: the reversal columns are needed by the dispute-lost path
-- on the EXISTING product, not just by rolling cycles).
ALTER TABLE owner_payouts
    ADD COLUMN stripe_reversal_id    TEXT,
    ADD COLUMN reversed_amount_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN reversed_at           TIMESTAMPTZ,
    ADD COLUMN reversal_reason       TEXT;

ALTER TABLE owner_payouts DROP CONSTRAINT owner_payouts_status_check;
ALTER TABLE owner_payouts ADD CONSTRAINT owner_payouts_status_check
    CHECK (status IN ('awaiting_onboarding', 'pending', 'paid', 'failed',
                      'withheld', 'reversed'));
