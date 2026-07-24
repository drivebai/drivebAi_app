package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
)

// TicketRepository handles support-ticket CRUD for BOTH the user and admin
// surfaces. Keeping admin queries here (rather than in AdminRepository, as
// accidents did) is deliberate: the accident admin endpoints forgot to sign
// their file URLs precisely because they lived in a separate handler/repo. One
// home, one signing path.
type TicketRepository struct {
	db *database.DB
}

func NewTicketRepository(db *database.DB) *TicketRepository {
	return &TicketRepository{db: db}
}

const ticketCols = `id, user_id, category, subject, description, status, last_step,
	submitted_at, resolved_at, closed_at, created_at, updated_at`

func scanTicket(row scanRow) (*models.SupportTicket, error) {
	var t models.SupportTicket
	t.Attachments = []models.TicketAttachment{}
	err := row.Scan(
		&t.ID, &t.UserID, &t.Category, &t.Subject, &t.Description, &t.Status, &t.LastStep,
		&t.SubmittedAt, &t.ResolvedAt, &t.ClosedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Create inserts a new draft ticket for the user.
func (r *TicketRepository) Create(ctx context.Context, userID uuid.UUID) (*models.SupportTicket, error) {
	row := r.db.Pool.QueryRow(ctx, `
		INSERT INTO support_tickets (id, user_id, status, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'draft', NOW(), NOW())
		RETURNING `+ticketCols, userID)
	return scanTicket(row)
}

// GetDraftForUser returns the user's most recent draft ticket, or an error when none.
func (r *TicketRepository) GetDraftForUser(ctx context.Context, userID uuid.UUID) (*models.SupportTicket, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+ticketCols+`
		FROM support_tickets
		WHERE user_id = $1 AND status = 'draft'
		ORDER BY created_at DESC
		LIMIT 1`, userID)
	return scanTicket(row)
}

// GetByIDForUser fetches a ticket and verifies ownership.
func (r *TicketRepository) GetByIDForUser(ctx context.Context, ticketID, userID uuid.UUID) (*models.SupportTicket, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT `+ticketCols+`
		FROM support_tickets
		WHERE id = $1 AND user_id = $2`, ticketID, userID)
	return scanTicket(row)
}

// ListForUser returns all of a user's tickets, newest first. Drafts included so
// the user can resume from "My requests".
func (r *TicketRepository) ListForUser(ctx context.Context, userID uuid.UUID) ([]models.SupportTicket, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT `+ticketCols+`
		FROM support_tickets
		WHERE user_id = $1
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	defer rows.Close()

	out := []models.SupportTicket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

// TicketPatch carries the draft-editable fields; nil pointer = not provided.
type TicketPatch struct {
	Category    *string
	Subject     *string
	Description *string
	LastStep    *int
}

// Update applies a partial patch to a DRAFT ticket. Returns found=false when the
// ticket isn't the user's draft (already submitted, or not theirs) so the
// handler can return 409 instead of a silent 200 (the accident flow's mistake).
func (r *TicketRepository) Update(ctx context.Context, ticketID, userID uuid.UUID, p TicketPatch) (found bool, err error) {
	sets := []string{"updated_at = NOW()"}
	args := []any{}
	argIdx := 1
	add := func(col string, v any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, argIdx))
		args = append(args, v)
		argIdx++
	}
	if p.Category != nil {
		add("category", *p.Category)
	}
	if p.Subject != nil {
		add("subject", *p.Subject)
	}
	if p.Description != nil {
		add("description", *p.Description)
	}
	if p.LastStep != nil {
		add("last_step", *p.LastStep)
	}

	query := "UPDATE support_tickets SET "
	for i, s := range sets {
		if i > 0 {
			query += ", "
		}
		query += s
	}
	args = append(args, ticketID, userID)
	query += fmt.Sprintf(" WHERE id = $%d AND user_id = $%d AND status = 'draft'", argIdx, argIdx+1)

	tag, err := r.db.Pool.Exec(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("update ticket: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Submit transitions draft -> open, stamping submitted_at. Returns found=false
// when the ticket isn't the user's draft (so the handler returns 409).
func (r *TicketRepository) Submit(ctx context.Context, ticketID, userID uuid.UUID) (found bool, err error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE support_tickets
		SET status = 'open', submitted_at = $1, updated_at = NOW()
		WHERE id = $2 AND user_id = $3 AND status = 'draft'`,
		time.Now().UTC(), ticketID, userID)
	if err != nil {
		return false, fmt.Errorf("submit ticket: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Reopen moves a RESOLVED ticket back to open (user action: it wasn't actually
// fixed), clearing resolved_at. Returns found=false when the ticket isn't the
// user's resolved ticket.
func (r *TicketRepository) Reopen(ctx context.Context, ticketID, userID uuid.UUID) (found bool, err error) {
	tag, err := r.db.Pool.Exec(ctx, `
		UPDATE support_tickets
		SET status = 'open', resolved_at = NULL, updated_at = NOW()
		WHERE id = $1 AND user_id = $2 AND status = 'resolved'`, ticketID, userID)
	if err != nil {
		return false, fmt.Errorf("reopen ticket: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// AddAttachment inserts an evidence file record.
func (r *TicketRepository) AddAttachment(ctx context.Context, ticketID uuid.UUID, fileURL, filePath string, fileSize int64, mimeType string) (*models.TicketAttachment, error) {
	var att models.TicketAttachment
	err := r.db.Pool.QueryRow(ctx, `
		INSERT INTO support_ticket_attachments (id, ticket_id, file_url, file_path, file_size, mime_type, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NOW())
		RETURNING id, ticket_id, file_url, file_size, mime_type, created_at`,
		ticketID, fileURL, filePath, fileSize, mimeType).Scan(
		&att.ID, &att.TicketID, &att.FileURL, &att.FileSize, &att.MimeType, &att.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("add ticket attachment: %w", err)
	}
	return &att, nil
}

// DeleteAttachment removes an attachment and returns its disk path for cleanup.
func (r *TicketRepository) DeleteAttachment(ctx context.Context, attachID, ticketID uuid.UUID) (string, error) {
	var filePath string
	err := r.db.Pool.QueryRow(ctx, `
		DELETE FROM support_ticket_attachments
		WHERE id = $1 AND ticket_id = $2
		RETURNING file_path`, attachID, ticketID).Scan(&filePath)
	if err != nil {
		return "", fmt.Errorf("delete ticket attachment: %w", err)
	}
	return filePath, nil
}

// ListAttachments returns a ticket's evidence, oldest first.
func (r *TicketRepository) ListAttachments(ctx context.Context, ticketID uuid.UUID) ([]models.TicketAttachment, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, ticket_id, file_url, file_size, mime_type, created_at
		FROM support_ticket_attachments
		WHERE ticket_id = $1
		ORDER BY created_at ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.TicketAttachment{}
	for rows.Next() {
		var a models.TicketAttachment
		if err := rows.Scan(&a.ID, &a.TicketID, &a.FileURL, &a.FileSize, &a.MimeType, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// --- Admin surface ---

// AdminTicketsPage is the paginated envelope the admin queue consumes.
type AdminTicketsPage struct {
	Items []models.AdminTicketRow `json:"items"`
	Total int                     `json:"total"`
	Page  int                     `json:"page"`
	Limit int                     `json:"limit"`
}

// AdminList returns tickets for the queue: drafts always excluded; oldest
// submitted first (triage the oldest). An empty status shows all non-draft
// tickets; a status filters to exactly that state.
func (r *TicketRepository) AdminList(ctx context.Context, page, limit int, status string) (*AdminTicketsPage, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}

	where := "WHERE t.status != 'draft'"
	args := []any{}
	if status != "" && status != "draft" {
		where = "WHERE t.status = $1"
		args = append(args, status)
	}

	var total int
	countQ := "SELECT COUNT(*) FROM support_tickets t " + where
	if err := r.db.Pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count tickets: %w", err)
	}

	args = append(args, limit, (page-1)*limit)
	query := fmt.Sprintf(`
		SELECT t.id, t.user_id, t.category, t.subject, t.description, t.status, t.last_step,
		       t.submitted_at, t.resolved_at, t.closed_at, t.created_at, t.updated_at,
		       COALESCE(NULLIF(TRIM(CONCAT(u.first_name, ' ', u.last_name)), ''), u.email) AS user_name,
		       u.email, u.role::text
		FROM support_tickets t
		JOIN users u ON u.id = t.user_id
		%s
		ORDER BY t.submitted_at ASC NULLS LAST, t.created_at ASC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args))

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tickets (admin): %w", err)
	}
	defer rows.Close()

	items := []models.AdminTicketRow{}
	for rows.Next() {
		var row models.AdminTicketRow
		row.Attachments = []models.TicketAttachment{}
		if err := rows.Scan(
			&row.ID, &row.UserID, &row.Category, &row.Subject, &row.Description, &row.Status, &row.LastStep,
			&row.SubmittedAt, &row.ResolvedAt, &row.ClosedAt, &row.CreatedAt, &row.UpdatedAt,
			&row.UserName, &row.UserEmail, &row.UserRole,
		); err != nil {
			return nil, err
		}
		items = append(items, row)
	}
	return &AdminTicketsPage{Items: items, Total: total, Page: page, Limit: limit}, nil
}

// AdminGet returns a single ticket with reporter identity, for the drawer.
func (r *TicketRepository) AdminGet(ctx context.Context, ticketID uuid.UUID) (*models.AdminTicketRow, error) {
	var row models.AdminTicketRow
	row.Attachments = []models.TicketAttachment{}
	err := r.db.Pool.QueryRow(ctx, `
		SELECT t.id, t.user_id, t.category, t.subject, t.description, t.status, t.last_step,
		       t.submitted_at, t.resolved_at, t.closed_at, t.created_at, t.updated_at,
		       COALESCE(NULLIF(TRIM(CONCAT(u.first_name, ' ', u.last_name)), ''), u.email) AS user_name,
		       u.email, u.role::text
		FROM support_tickets t
		JOIN users u ON u.id = t.user_id
		WHERE t.id = $1`, ticketID).Scan(
		&row.ID, &row.UserID, &row.Category, &row.Subject, &row.Description, &row.Status, &row.LastStep,
		&row.SubmittedAt, &row.ResolvedAt, &row.ClosedAt, &row.CreatedAt, &row.UpdatedAt,
		&row.UserName, &row.UserEmail, &row.UserRole,
	)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// AdminUpdateStatus moves a ticket to a new status, stamping resolved_at /
// closed_at as appropriate, and returns the ticket's owner id so the caller can
// notify them. Drafts can't be transitioned by an admin.
func (r *TicketRepository) AdminUpdateStatus(ctx context.Context, ticketID uuid.UUID, status models.TicketStatus) (userID uuid.UUID, err error) {
	// $1 must be cast to text at EVERY use. Without the casts, `status = $1`
	// deduces $1 as varchar (the column type) while `$1 = 'resolved'` deduces it
	// as text — and Postgres refuses to prepare a statement with inconsistent
	// types for one parameter ("inconsistent types deduced for parameter $1:
	// text versus character varying"). psql lets you dodge this by declaring the
	// param type; pgx always prepares, so it fails every time. Casting pins $1
	// to text everywhere so the deduction is consistent.
	err = r.db.Pool.QueryRow(ctx, `
		UPDATE support_tickets
		SET status = $1::text,
		    resolved_at = CASE WHEN $1::text = 'resolved' THEN NOW() ELSE resolved_at END,
		    closed_at   = CASE WHEN $1::text = 'closed'   THEN NOW() ELSE closed_at   END,
		    updated_at  = NOW()
		WHERE id = $2 AND status != 'draft'
		RETURNING user_id`, string(status), ticketID).Scan(&userID)
	return userID, err
}
