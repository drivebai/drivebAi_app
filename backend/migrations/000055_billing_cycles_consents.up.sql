-- Batch 2, part 2: the recurring engine's two tables (design §3).
--
-- lease_billing_consents: the ONLY source of amount + payment method for
-- every off-session charge. terms_version + disclosure_text are the
-- durable consent record card networks require (and the client requires:
-- the owner-terms sentence is part of the versioned terms).
--
-- billing_cycles: one row per lease-week; ONE PaymentIntent per cycle,
-- created once, re-confirmed on retries. Notification stamps live on the
-- cycle row, so claimed-once is per-episode by construction.

CREATE TABLE lease_billing_consents (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id          UUID NOT NULL REFERENCES lease_requests(id),
    driver_id                 UUID NOT NULL REFERENCES users(id),
    amount_cents              BIGINT NOT NULL CHECK (amount_cents > 0),
    billing_interval          TEXT NOT NULL DEFAULT 'weekly'
                              CHECK (billing_interval = 'weekly'),
    terms_version             TEXT NOT NULL,
    disclosure_text           TEXT NOT NULL,
    stripe_payment_method_id  TEXT,
    card_brand                TEXT,
    card_last4                TEXT,
    card_fingerprint          TEXT,
    activated_at              TIMESTAMPTZ,
    revoked_at                TIMESTAMPTZ,
    revoked_reason            TEXT,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX uq_billing_consents_active
    ON lease_billing_consents (lease_request_id)
    WHERE revoked_at IS NULL;

CREATE TABLE billing_cycles (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id          UUID NOT NULL REFERENCES lease_requests(id),
    cycle_number              INT NOT NULL CHECK (cycle_number >= 1),
    period_start              TIMESTAMPTZ NOT NULL,
    period_end                TIMESTAMPTZ NOT NULL,
    amount_cents              BIGINT NOT NULL CHECK (amount_cents > 0),
    status TEXT NOT NULL DEFAULT 'scheduled' CHECK (status IN
        ('scheduled',       -- minted at T-24h, not yet charged
         'charging',        -- PI confirm in flight
         'needs_action',    -- authentication_required; driver rescue, 72h TTL
         'retrying',        -- declined, ladder not exhausted
         'paid',
         'failed_final',    -- ladder exhausted; lease delinquent
         'arrears_due',     -- unpaid at return; on-session collection only
         'waived',          -- admin write-off (note required)
         'refunded',        -- fully refunded (overshoot / pickup no-show)
         'partially_refunded')),
    stripe_payment_intent_id  TEXT,
    attempt_count             INT NOT NULL DEFAULT 0,
    next_attempt_at           TIMESTAMPTZ,
    last_decline_code         TEXT,
    needs_action_since        TIMESTAMPTZ,
    refunded_cents            BIGINT NOT NULL DEFAULT 0,
    refund_id                 TEXT,
    -- claimed-once notification stamps, per cycle
    failure_notified_at       TIMESTAMPTZ,
    delinquent_notified_at    TIMESTAMPTZ,
    admin_note                TEXT,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX uq_billing_cycles_lease_cycle
    ON billing_cycles (lease_request_id, cycle_number);
CREATE INDEX idx_billing_cycles_due
    ON billing_cycles (next_attempt_at)
    WHERE status IN ('scheduled', 'retrying');
CREATE INDEX idx_billing_cycles_needs_action
    ON billing_cycles (needs_action_since)
    WHERE status = 'needs_action';
