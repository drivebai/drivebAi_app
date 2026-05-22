package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/drivebai/backend/internal/database"
	"github.com/google/uuid"
)

// SupportRepository handles the user-facing side of support chats.
// Admin-side queries live in AdminRepository to keep concerns separated.
type SupportRepository struct {
	db *database.DB
}

func NewSupportRepository(db *database.DB) *SupportRepository {
	return &SupportRepository{db: db}
}

// SupportChatResponse is the shape returned to the mobile client.
type SupportChatResponse struct {
	ID            uuid.UUID  `json:"id"`
	UserID        uuid.UUID  `json:"user_id"`
	UnreadCount   int        `json:"unread_count"` // admin replies not yet seen by user
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// SupportMessageResponse is the shape for individual messages on both sides.
type SupportMessageResponse struct {
	ID            uuid.UUID `json:"id"`
	SupportChatID uuid.UUID `json:"support_chat_id"`
	SenderID      uuid.UUID `json:"sender_id"`
	SenderKind    string    `json:"sender_kind"` // "user" | "admin"
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

// GetOrCreateChat returns the existing support chat for the user or creates one.
func (r *SupportRepository) GetOrCreateChat(ctx context.Context, userID uuid.UUID) (*SupportChatResponse, error) {
	var chat SupportChatResponse
	var userLastRead *time.Time
	err := r.db.Pool.QueryRow(ctx, `
		INSERT INTO support_chats (id, user_id, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, NOW(), NOW())
		ON CONFLICT (user_id) DO UPDATE SET updated_at = support_chats.updated_at
		RETURNING id, user_id, last_message_at, created_at, user_last_read_at
	`, userID).Scan(&chat.ID, &chat.UserID, &chat.LastMessageAt, &chat.CreatedAt, &userLastRead)
	if err != nil {
		return nil, fmt.Errorf("upsert support chat: %w", err)
	}

	// Compute unread: admin messages created after user's last read
	if err := r.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM support_messages sm
		JOIN support_chats sc ON sc.id = sm.support_chat_id
		WHERE sm.support_chat_id = $1
		  AND sm.sender_kind = 'admin'
		  AND (sc.user_last_read_at IS NULL OR sm.created_at > sc.user_last_read_at)
	`, chat.ID).Scan(&chat.UnreadCount); err != nil {
		chat.UnreadCount = 0
	}

	return &chat, nil
}

// GetChatForUser returns the support chat only if it belongs to userID (ownership check).
func (r *SupportRepository) GetChatForUser(ctx context.Context, chatID, userID uuid.UUID) (*SupportChatResponse, error) {
	var chat SupportChatResponse
	var userLastRead *time.Time
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, user_id, last_message_at, created_at, user_last_read_at
		FROM support_chats
		WHERE id = $1 AND user_id = $2
	`, chatID, userID).Scan(&chat.ID, &chat.UserID, &chat.LastMessageAt, &chat.CreatedAt, &userLastRead)
	if err != nil {
		return nil, err
	}

	if err := r.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM support_messages
		WHERE support_chat_id = $1
		  AND sender_kind = 'admin'
		  AND ($2::timestamptz IS NULL OR created_at > $2)
	`, chatID, userLastRead).Scan(&chat.UnreadCount); err != nil {
		chat.UnreadCount = 0
	}

	return &chat, nil
}

// ListMessages returns messages for a support chat, verifying that chatID belongs to userID.
func (r *SupportRepository) ListMessages(ctx context.Context, chatID, userID uuid.UUID) ([]SupportMessageResponse, error) {
	// Ownership check
	var ownerID uuid.UUID
	if err := r.db.Pool.QueryRow(ctx,
		"SELECT user_id FROM support_chats WHERE id = $1", chatID,
	).Scan(&ownerID); err != nil {
		return nil, fmt.Errorf("support chat not found")
	}
	if ownerID != userID {
		return nil, fmt.Errorf("not authorized")
	}

	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, support_chat_id, sender_id, sender_kind, body, created_at
		FROM support_messages
		WHERE support_chat_id = $1
		ORDER BY created_at ASC
	`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list support messages: %w", err)
	}
	defer rows.Close()

	out := []SupportMessageResponse{}
	for rows.Next() {
		var m SupportMessageResponse
		if err := rows.Scan(&m.ID, &m.SupportChatID, &m.SenderID, &m.SenderKind, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// PostMessage inserts a user message and bumps last_message_at.
func (r *SupportRepository) PostMessage(ctx context.Context, chatID, userID uuid.UUID, body string) (*SupportMessageResponse, error) {
	// Verify ownership
	var ownerID uuid.UUID
	if err := r.db.Pool.QueryRow(ctx,
		"SELECT user_id FROM support_chats WHERE id = $1", chatID,
	).Scan(&ownerID); err != nil {
		return nil, fmt.Errorf("support chat not found")
	}
	if ownerID != userID {
		return nil, fmt.Errorf("not authorized")
	}

	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	var m SupportMessageResponse
	if err := tx.QueryRow(ctx, `
		INSERT INTO support_messages (id, support_chat_id, sender_id, sender_kind, body, created_at)
		VALUES (gen_random_uuid(), $1, $2, 'user', $3, $4)
		RETURNING id, support_chat_id, sender_id, sender_kind, body, created_at
	`, chatID, userID, body, now).Scan(
		&m.ID, &m.SupportChatID, &m.SenderID, &m.SenderKind, &m.Body, &m.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("insert support message: %w", err)
	}

	if _, err := tx.Exec(ctx,
		"UPDATE support_chats SET last_message_at = $2, updated_at = $2 WHERE id = $1",
		chatID, now,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &m, nil
}

// MarkUserRead stamps user_last_read_at = NOW() for this chat.
func (r *SupportRepository) MarkUserRead(ctx context.Context, chatID, userID uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx,
		"UPDATE support_chats SET user_last_read_at = NOW() WHERE id = $1 AND user_id = $2",
		chatID, userID,
	)
	return err
}
