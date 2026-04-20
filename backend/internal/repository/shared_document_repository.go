package repository

import (
	"context"
	"time"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// SharedDocumentRepository persists the links between lease requests and the
// driver documents shared with the car owner through them.
type SharedDocumentRepository struct {
	db *database.DB
}

func NewSharedDocumentRepository(db *database.DB) *SharedDocumentRepository {
	return &SharedDocumentRepository{db: db}
}

// SharedDocumentInfo is the joined row returned by list queries. It exposes
// only the document metadata we surface to the client — the on-disk file_path
// stays internal and is converted to a public URL by the handler.
type SharedDocumentInfo struct {
	ID         uuid.UUID
	DocumentID uuid.UUID
	UploaderID uuid.UUID
	Type       models.DocumentType
	FileName   string
	FilePath   string
	FileSize   int64
	MimeType   string
	Status     models.DocumentStatus
	SharedAt   time.Time
	DocCreatedAt time.Time
}

// CreateForLeaseRequest idempotently inserts one link row per document. Safe
// to call even if some links already exist (ON CONFLICT DO NOTHING). If
// documentIDs is empty this is a no-op.
func (r *SharedDocumentRepository) CreateForLeaseRequest(ctx context.Context, leaseRequestID uuid.UUID, documentIDs []uuid.UUID) error {
	if len(documentIDs) == 0 {
		return nil
	}
	query := `
		INSERT INTO lease_request_shared_documents (lease_request_id, document_id)
		VALUES ($1, $2)
		ON CONFLICT (lease_request_id, document_id) DO NOTHING
	`
	for _, docID := range documentIDs {
		if _, err := r.db.Pool.Exec(ctx, query, leaseRequestID, docID); err != nil {
			return err
		}
	}
	return nil
}

// ListByChatID returns every document shared through any lease request that
// belongs to the given chat. Deduplicated by document_id — if the same doc was
// shared across multiple requests in the chat, it appears once (with the
// earliest sharedAt). Ordered newest-share first.
func (r *SharedDocumentRepository) ListByChatID(ctx context.Context, chatID uuid.UUID) ([]*SharedDocumentInfo, error) {
	query := `
		SELECT DISTINCT ON (d.id)
		       s.id, d.id, d.user_id, d.type, d.file_name, d.file_path,
		       d.file_size, d.mime_type, d.status, s.created_at, d.created_at
		FROM lease_request_shared_documents s
		JOIN lease_requests lr ON lr.id = s.lease_request_id
		JOIN documents d ON d.id = s.document_id
		WHERE lr.chat_id = $1
		ORDER BY d.id, s.created_at ASC
	`
	rows, err := r.db.Pool.Query(ctx, query, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var infos []*SharedDocumentInfo
	for rows.Next() {
		info := &SharedDocumentInfo{}
		if err := rows.Scan(
			&info.ID, &info.DocumentID, &info.UploaderID, &info.Type,
			&info.FileName, &info.FilePath, &info.FileSize, &info.MimeType,
			&info.Status, &info.SharedAt, &info.DocCreatedAt,
		); err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, rows.Err()
}
