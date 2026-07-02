-- Buy the Car (purchase) flow.
--
-- Separate from lease_requests because the state machine and satellite
-- data (Bill of Sale, inspection rejection with evidence) diverge from
-- the rental side. See DESIGN SPEC §2.1 for the rationale.

-- ─────────────────────────────────────────────────────────────────────────────
-- Enums
-- ─────────────────────────────────────────────────────────────────────────────

-- Convert cars.status from the car_listing_status enum to VARCHAR(20) +
-- CHECK so we can add 'sold' without the ALTER TYPE ADD VALUE
-- non-transactional pain. Same pattern as migration 000023 for
-- messages.type and migration 000024 for lease_requests.status.
--
-- ORDERING NOTE: idx_cars_status is a plain b-tree with no predicate, so
-- it does not need to be dropped for the ALTER COLUMN TYPE to succeed.

ALTER TABLE cars
    ALTER COLUMN status DROP DEFAULT,
    ALTER COLUMN status TYPE VARCHAR(20) USING status::text,
    ALTER COLUMN status SET DEFAULT 'pending';

ALTER TABLE cars
    DROP CONSTRAINT IF EXISTS cars_status_check;

ALTER TABLE cars
    ADD CONSTRAINT cars_status_check
    CHECK (status IN ('available', 'rented', 'pending', 'paused', 'sold'));

-- The old `car_listing_status` ENUM type is intentionally left in the
-- catalog as dead schema so any external tooling that referenced it does
-- not break.

DO $$ BEGIN
    CREATE TYPE purchase_request_status AS ENUM (
        'requested','accepted','declined','cancelled',
        'bos_pending_seller','bos_pending_buyer','bos_signed',
        'payment_authorized','handover_scheduled','awaiting_inspection',
        'inspection_accepted','completed',
        'inspection_rejected','rejected_refunded','rejected_upheld',
        'expired','expired_auth'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE purchase_rejection_reason AS ENUM (
        'undisclosed_damage',
        'mechanical_issues',
        'title_or_paperwork',
        'vin_mismatch',
        'not_as_described',
        'no_show',
        'other'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    CREATE TYPE purchase_rejection_status AS ENUM (
        'submitted',
        'under_review',
        'accepted',
        'upheld',
        'withdrawn'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- purchase_requests
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS purchase_requests (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    car_id                    UUID NOT NULL REFERENCES cars(id) ON DELETE RESTRICT,
    seller_id                 UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    buyer_id                  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    chat_id                   UUID NOT NULL REFERENCES chats(id) ON DELETE RESTRICT,

    offer_amount_cents        BIGINT NOT NULL CHECK (offer_amount_cents >= 100000),
    currency                  VARCHAR(3) NOT NULL DEFAULT 'USD',
    buyer_message             TEXT,

    status                    purchase_request_status NOT NULL DEFAULT 'requested',
    expires_at                TIMESTAMPTZ NOT NULL,
    auth_expires_at           TIMESTAMPTZ,
    handover_location         TEXT,
    handover_latitude         DOUBLE PRECISION,
    handover_longitude        DOUBLE PRECISION,
    handover_scheduled_at     TIMESTAMPTZ,
    keys_handed_over_at       TIMESTAMPTZ,
    inspection_deadline_at    TIMESTAMPTZ,
    inspection_accepted_at    TIMESTAMPTZ,
    completed_at              TIMESTAMPTZ,

    payment_intent_id         TEXT UNIQUE,
    payment_status            VARCHAR(32),
    refund_status             VARCHAR(20) CHECK (refund_status IS NULL OR refund_status IN ('pending','succeeded','failed','not_applicable')),
    refund_id                 TEXT,
    refunded_at               TIMESTAMPTZ,
    refund_failure_reason     TEXT,

    cancellation_reason       TEXT,

    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Enforce ONE non-terminal request per (car, buyer). Terminal states are
-- {declined, cancelled, expired, expired_auth, completed, rejected_refunded,
-- rejected_upheld} — a buyer whose offer terminated may send another later.
CREATE UNIQUE INDEX IF NOT EXISTS idx_purchase_requests_active_unique
  ON purchase_requests(car_id, buyer_id)
  WHERE status IN (
    'requested','accepted','bos_pending_seller','bos_pending_buyer','bos_signed',
    'payment_authorized','handover_scheduled','awaiting_inspection',
    'inspection_accepted','inspection_rejected'
  );

CREATE INDEX IF NOT EXISTS idx_purchase_requests_seller_status ON purchase_requests(seller_id, status);
CREATE INDEX IF NOT EXISTS idx_purchase_requests_buyer_status  ON purchase_requests(buyer_id, status);
CREATE INDEX IF NOT EXISTS idx_purchase_requests_chat_id       ON purchase_requests(chat_id);
CREATE INDEX IF NOT EXISTS idx_purchase_requests_expires_at    ON purchase_requests(expires_at) WHERE status = 'requested';
CREATE INDEX IF NOT EXISTS idx_purchase_requests_auth_expiry   ON purchase_requests(auth_expires_at)
  WHERE status IN ('payment_authorized','handover_scheduled','awaiting_inspection');

-- Reuse the shared update_updated_at_column() defined in 000001.
DO $$ BEGIN
    CREATE TRIGGER set_purchase_requests_updated_at
        BEFORE UPDATE ON purchase_requests
        FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- purchase_bill_of_sales
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS purchase_bill_of_sales (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_request_id      UUID NOT NULL UNIQUE REFERENCES purchase_requests(id) ON DELETE CASCADE,

    vehicle_year             INT  NOT NULL,
    vehicle_make             VARCHAR(64) NOT NULL,
    vehicle_model            VARCHAR(64) NOT NULL,
    vin                      VARCHAR(17) NOT NULL,

    sale_amount_cents        BIGINT NOT NULL,
    currency                 VARCHAR(3) NOT NULL DEFAULT 'USD',

    terms_conditions         TEXT NOT NULL DEFAULT
        'Vehicle is sold as-is, where-is, with no warranties unless otherwise stated in writing.',

    seller_name              VARCHAR(160) NOT NULL DEFAULT '',
    seller_address           TEXT NOT NULL DEFAULT '',
    seller_signature_url     TEXT,
    seller_signed_at         TIMESTAMPTZ,

    buyer_name               VARCHAR(160) NOT NULL DEFAULT '',
    buyer_address            TEXT NOT NULL DEFAULT '',
    buyer_signature_url      TEXT,
    buyer_signed_at          TIMESTAMPTZ,

    finalized_pdf_url        TEXT,
    finalized_at             TIMESTAMPTZ,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
    CREATE TRIGGER set_purchase_bos_updated_at
        BEFORE UPDATE ON purchase_bill_of_sales
        FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- ─────────────────────────────────────────────────────────────────────────────
-- purchase_rejections + evidence
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS purchase_rejections (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_request_id   UUID NOT NULL UNIQUE REFERENCES purchase_requests(id) ON DELETE CASCADE,
    reason_category       purchase_rejection_reason NOT NULL,
    explanation           TEXT NOT NULL CHECK (char_length(explanation) BETWEEN 20 AND 2000),
    status                purchase_rejection_status NOT NULL DEFAULT 'submitted',
    refund_status         VARCHAR(20) CHECK (refund_status IS NULL OR refund_status IN ('pending','succeeded','failed','not_applicable')),
    admin_note            TEXT,
    resolved_by           UUID REFERENCES users(id),
    resolved_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_purchase_rejections_status ON purchase_rejections(status);

DO $$ BEGIN
    CREATE TRIGGER set_purchase_rejections_updated_at
        BEFORE UPDATE ON purchase_rejections
        FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS purchase_rejection_evidence (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_rejection_id  UUID NOT NULL REFERENCES purchase_rejections(id) ON DELETE CASCADE,
    file_url               TEXT NOT NULL,
    file_path              TEXT NOT NULL,
    filename               TEXT NOT NULL,
    mime_type              VARCHAR(64) NOT NULL,
    size_bytes             BIGINT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_prej_evidence_rejection ON purchase_rejection_evidence(purchase_rejection_id);

-- ─────────────────────────────────────────────────────────────────────────────
-- cars.reserved_by_purchase_request_id
-- ─────────────────────────────────────────────────────────────────────────────

ALTER TABLE cars ADD COLUMN IF NOT EXISTS reserved_by_purchase_request_id UUID
    REFERENCES purchase_requests(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_cars_reserved_by_purchase_request_id
    ON cars(reserved_by_purchase_request_id)
    WHERE reserved_by_purchase_request_id IS NOT NULL;
