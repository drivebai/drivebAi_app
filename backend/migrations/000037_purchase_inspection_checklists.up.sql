-- Buyer's pre-capture inspection checklist (DESIGN SPEC item 22, SAFETY
-- CRITICAL). One row per purchase; persisted BEFORE the inspection_accepted
-- status flip and BEFORE Stripe capture. A NULL row is tolerated for
-- pre-migration in-flight purchases.

CREATE TABLE IF NOT EXISTS purchase_inspection_checklists (
    id                                             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    purchase_request_id                            UUID NOT NULL UNIQUE
                                                       REFERENCES purchase_requests(id) ON DELETE CASCADE,
    vin_matches                                    BOOLEAN NOT NULL,
    odometer_reviewed                              BOOLEAN NOT NULL,
    exterior_ok                                    BOOLEAN NOT NULL,
    interior_ok                                    BOOLEAN NOT NULL,
    mechanical_test_drive_ok                       BOOLEAN NOT NULL,
    title_reviewed                                 BOOLEAN NOT NULL,
    keys_handed_over                               BOOLEAN NOT NULL,
    buyer_understands_acceptance_completes_payment BOOLEAN NOT NULL,
    created_at                                     TIMESTAMPTZ NOT NULL DEFAULT now()
);
