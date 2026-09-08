-- Batch: rolling amendments (price / interval), offer → driver accepts →
-- applies from the NEXT cycle. The consent row's amount and interval are
-- immutable under the recorded disclosure ("this amount never changes
-- without a new agreement from you") — an amendment mints a NEW consent
-- with fresh acceptance evidence; it never edits the old one.

CREATE TABLE billing_amendment_offers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lease_request_id UUID NOT NULL REFERENCES lease_requests(id),
    proposed_by UUID NOT NULL REFERENCES users(id),
    kind TEXT NOT NULL CHECK (kind IN ('price', 'interval')),
    new_amount_cents BIGINT NOT NULL CHECK (new_amount_cents > 0),
    new_interval TEXT NOT NULL DEFAULT 'weekly' CHECK (new_interval IN ('weekly', 'monthly')),
    status TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'accepted', 'declined', 'expired', 'withdrawn')),
    expires_at TIMESTAMPTZ NOT NULL,
    acted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One live offer per lease: a second proposal must supersede explicitly
-- (withdraw first), never stack.
CREATE UNIQUE INDEX uq_billing_amendment_open
    ON billing_amendment_offers (lease_request_id) WHERE status = 'open';
CREATE INDEX idx_billing_amendment_expiry
    ON billing_amendment_offers (expires_at) WHERE status = 'open';
