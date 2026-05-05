package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

type NotificationRepository struct {
	db *database.DB
}

func NewNotificationRepository(db *database.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

// Create inserts a new notification and returns the populated record.
func (r *NotificationRepository) Create(
	ctx context.Context,
	userID uuid.UUID,
	notifType models.NotificationType,
	title, body string,
	relatedChatID *uuid.UUID,
	relatedLeaseRequestID *uuid.UUID,
) (*models.Notification, error) {
	n := &models.Notification{
		ID:                    uuid.New(),
		UserID:                userID,
		Type:                  notifType,
		Title:                 title,
		Body:                  body,
		RelatedChatID:         relatedChatID,
		RelatedLeaseRequestID: relatedLeaseRequestID,
		IsRead:                false,
		CreatedAt:             time.Now().UTC(),
	}

	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO notifications
		    (id, user_id, type, title, body, related_chat_id, related_lease_request_id, is_read, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, n.ID, n.UserID, n.Type, n.Title, n.Body,
		n.RelatedChatID, n.RelatedLeaseRequestID, n.IsRead, n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return n, nil
}

// ListByUser returns the most recent notifications for a user (limit 50).
func (r *NotificationRepository) ListByUser(ctx context.Context, userID uuid.UUID) ([]models.Notification, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, user_id, type, title, body,
		       related_chat_id, related_lease_request_id, is_read, created_at
		FROM notifications
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT 50
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Notification
	for rows.Next() {
		var n models.Notification
		if err := rows.Scan(
			&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body,
			&n.RelatedChatID, &n.RelatedLeaseRequestID, &n.IsRead, &n.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCount returns the count of unread notifications for a user.
func (r *NotificationRepository) UnreadCount(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	err := r.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications WHERE user_id = $1 AND is_read = FALSE
	`, userID).Scan(&count)
	return count, err
}

// MarkRead marks a single notification as read (only if owned by userID).
func (r *NotificationRepository) MarkRead(ctx context.Context, notifID, userID uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE notifications SET is_read = TRUE
		WHERE id = $1 AND user_id = $2
	`, notifID, userID)
	return err
}

// MarkAllRead marks all notifications as read for a user.
func (r *NotificationRepository) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE notifications SET is_read = TRUE WHERE user_id = $1 AND is_read = FALSE
	`, userID)
	return err
}
