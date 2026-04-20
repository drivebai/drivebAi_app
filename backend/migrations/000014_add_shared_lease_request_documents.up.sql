-- Link table: snapshots which of the driver's existing onboarding documents
-- are shared with the car owner through a given lease request. References
-- documents.id so the same file is shown to the owner — no re-upload, no
-- duplication. Read access is gated at the API layer by chat participation.

CREATE TABLE lease_request_shared_documents (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    lease_request_id UUID NOT NULL REFERENCES lease_requests(id) ON DELETE CASCADE,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    UNIQUE (lease_request_id, document_id)
);

CREATE INDEX idx_lr_shared_docs_lr ON lease_request_shared_documents(lease_request_id);
CREATE INDEX idx_lr_shared_docs_doc ON lease_request_shared_documents(document_id);
