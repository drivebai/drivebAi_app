-- Driver debt as a first-class, driver-level balance.
--
-- Until now a debt was one billing_cycles row with status 'arrears_due'.
-- That row has no driver_id (attribution runs through lease_request_id), so
-- two debts on two rentals were two rows nothing ever summed, and the app's
-- promise that "new bookings are paused until this is resolved" had nothing
-- to enforce it. It also could not survive account deletion in any usable
-- form: SoftDeleteUser rewrites email, NULLs phone and replaces the name,
-- so anything needed to recognise the person later must be SNAPSHOT here at
-- the moment the debt opens.

CREATE TABLE IF NOT EXISTS driver_debts (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    driver_id             UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    lease_request_id      UUID NOT NULL REFERENCES lease_requests(id) ON DELETE RESTRICT,
    -- The uncollected week this debt came from. Nullable so a future debt
    -- source (a sale, an admin adjustment) can live in the same ledger.
    billing_cycle_id      UUID REFERENCES billing_cycles(id) ON DELETE RESTRICT,

    original_amount_cents BIGINT NOT NULL CHECK (original_amount_cents > 0),
    outstanding_cents     BIGINT NOT NULL CHECK (outstanding_cents >= 0),
    currency              TEXT   NOT NULL DEFAULT 'USD',
    status                TEXT   NOT NULL DEFAULT 'open'
                            CHECK (status IN ('open', 'paid', 'waived', 'written_off')),
    reason                TEXT   NOT NULL,

    -- Identifier snapshot. Account deletion destroys these on users; a debt
    -- that outlives the account needs its own copy to remain attributable.
    -- Never used to auto-block anyone — human review only.
    snapshot_email        TEXT,
    snapshot_phone        TEXT,
    snapshot_name         TEXT,
    card_fingerprint      TEXT,
    card_last4            TEXT,

    opened_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at             TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CHECK (outstanding_cents <= original_amount_cents),
    CHECK ((status = 'open') = (closed_at IS NULL))
);

-- One debt per uncollected week: the claim that makes debt-opening
-- re-entrant when a sweep retries.
CREATE UNIQUE INDEX IF NOT EXISTS uq_driver_debts_cycle
    ON driver_debts (billing_cycle_id) WHERE billing_cycle_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_driver_debts_open
    ON driver_debts (driver_id) WHERE status = 'open';

CREATE INDEX IF NOT EXISTS idx_driver_debts_fingerprint
    ON driver_debts (card_fingerprint)
    WHERE card_fingerprint IS NOT NULL AND status = 'open';

-- Append-only history. The balance lives on driver_debts.outstanding_cents;
-- this is the audit trail behind every change to it.
CREATE TABLE IF NOT EXISTS driver_debt_entries (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    debt_id                  UUID NOT NULL REFERENCES driver_debts(id) ON DELETE RESTRICT,
    kind                     TEXT NOT NULL
                               CHECK (kind IN ('opened', 'payment', 'waive', 'write_off', 'adjustment')),
    amount_cents             BIGINT NOT NULL,
    stripe_payment_intent_id TEXT,
    actor                    TEXT NOT NULL DEFAULT 'system'
                               CHECK (actor IN ('system', 'driver', 'admin')),
    note                     TEXT,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A redelivered webhook must not apply the same money twice.
CREATE UNIQUE INDEX IF NOT EXISTS uq_driver_debt_entries_intent
    ON driver_debt_entries (stripe_payment_intent_id)
    WHERE stripe_payment_intent_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_driver_debt_entries_debt
    ON driver_debt_entries (debt_id, created_at DESC);
