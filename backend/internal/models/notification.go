package models

import (
	"time"

	"github.com/google/uuid"
)

// NotificationType identifies the semantic category of a notification.
type NotificationType string

const (
	NotificationTypeLeaseRequest NotificationType = "lease_request"
	NotificationTypePayment      NotificationType = "payment"
	NotificationTypeKeyHandover  NotificationType = "key_handover"
	NotificationTypeSystem       NotificationType = "system"
)

// Notification is the DB-backed record stored per user.
type Notification struct {
	ID                    uuid.UUID        `db:"id"`
	UserID                uuid.UUID        `db:"user_id"`
	Type                  NotificationType `db:"type"`
	Title                 string           `db:"title"`
	Body                  string           `db:"body"`
	RelatedChatID         *uuid.UUID       `db:"related_chat_id"`
	RelatedLeaseRequestID *uuid.UUID       `db:"related_lease_request_id"`
	IsRead                bool             `db:"is_read"`
	CreatedAt             time.Time        `db:"created_at"`
}

// NotificationResponse is the API response shape.
type NotificationResponse struct {
	ID                    uuid.UUID        `json:"id"`
	Type                  NotificationType `json:"type"`
	Title                 string           `json:"title"`
	Body                  string           `json:"body"`
	RelatedChatID         *uuid.UUID       `json:"related_chat_id,omitempty"`
	RelatedLeaseRequestID *uuid.UUID       `json:"related_lease_request_id,omitempty"`
	IsRead                bool             `json:"is_read"`
	CreatedAt             RFC3339Time      `json:"created_at"`
}

// NotificationsListResponse is returned by GET /api/v1/notifications.
type NotificationsListResponse struct {
	Notifications []NotificationResponse `json:"notifications"`
	UnreadCount   int                    `json:"unread_count"`
}

// DeviceToken is stored when iOS registers for push notifications.
type DeviceToken struct {
	ID        uuid.UUID `db:"id"`
	UserID    uuid.UUID `db:"user_id"`
	Token     string    `db:"token"`
	Platform  string    `db:"platform"`
	Sandbox   bool      `db:"sandbox"`
	CreatedAt time.Time `db:"created_at"`
}

// RegisterDeviceTokenBody is the request body for POST /api/v1/me/device-token.
type RegisterDeviceTokenBody struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
	Sandbox  bool   `json:"sandbox"`
}

// DeleteDeviceTokenBody is the request body for DELETE /api/v1/me/device-token.
type DeleteDeviceTokenBody struct {
	Token string `json:"token"`
}
