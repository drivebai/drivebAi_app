-- Migration 000021: accidents module
-- accidents stores the full multi-step accident report submitted by a driver or owner.
-- All structured step fields are JSONB for flexibility — this data is only queried by ID,
-- never aggregated across rows.
-- accident_attachments stores every uploaded file (photos, video, docs) per accident.

CREATE TABLE accidents (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    reporter_id          UUID        NOT NULL REFERENCES users(id),
    related_chat_id      UUID        REFERENCES chats(id),
    related_car_id       UUID        REFERENCES cars(id),
    status               VARCHAR(20) NOT NULL DEFAULT 'draft',
    -- Step fields
    driver1_info         JSONB,
    driver2_info         JSONB,
    vehicle_damage       JSONB,
    accident_description TEXT,
    insurance_info       JSONB,
    other_info           JSONB,
    -- Signature
    signature_url        TEXT,
    signature_signed_at  TIMESTAMPTZ,
    -- Timestamps
    submitted_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_accidents_reporter_id ON accidents(reporter_id);
CREATE INDEX idx_accidents_status      ON accidents(status);

CREATE TABLE accident_attachments (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    accident_id UUID        NOT NULL REFERENCES accidents(id) ON DELETE CASCADE,
    slot        VARCHAR(50) NOT NULL,
    file_url    TEXT        NOT NULL,
    file_path   TEXT        NOT NULL,
    file_size   BIGINT,
    mime_type   VARCHAR(100),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_accident_attachments_accident_id ON accident_attachments(accident_id);
