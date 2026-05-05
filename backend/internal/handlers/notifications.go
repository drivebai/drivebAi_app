package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/push"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// NotificationHandler exposes in-app notification endpoints.
// It is also the central helper called by other handlers (lease, payment) to
// create a notification + fire WS + attempt a push in one place.
type NotificationHandler struct {
	notifRepo      *repository.NotificationRepository
	deviceTokenRepo *repository.DeviceTokenRepository
	wsHub          *ws.Hub
	pushSvc        *push.Service // may be nil if APNs not configured
	logger         *slog.Logger
}

func NewNotificationHandler(
	notifRepo *repository.NotificationRepository,
	deviceTokenRepo *repository.DeviceTokenRepository,
	wsHub *ws.Hub,
	pushSvc *push.Service,
	logger *slog.Logger,
) *NotificationHandler {
	return &NotificationHandler{
		notifRepo:       notifRepo,
		deviceTokenRepo: deviceTokenRepo,
		wsHub:           wsHub,
		pushSvc:         pushSvc,
		logger:          logger,
	}
}

// Notify creates a DB notification, broadcasts a WS event, and fires a push
// (best-effort). Uses a detached background context so a cancelled HTTP request
// doesn't drop the notification. Safe to call from any goroutine.
func (h *NotificationHandler) Notify(
	userID uuid.UUID,
	notifType models.NotificationType,
	title, body string,
	relatedChatID *uuid.UUID,
	relatedLeaseRequestID *uuid.UUID,
) {
	bgCtx := context.Background()

	n, err := h.notifRepo.Create(bgCtx, userID, notifType, title, body, relatedChatID, relatedLeaseRequestID)
	if err != nil {
		h.logger.Error("notify: create notification", "error", err, "user_id", userID)
		return
	}

	// Count total unread to send in the WS event so the iOS badge updates
	unread, _ := h.notifRepo.UnreadCount(bgCtx, userID)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "notification_created",
		Payload:       map[string]int{"unread_count": unread},
		TargetUserIDs: []uuid.UUID{userID},
	})

	// Push — non-blocking, best-effort
	if h.pushSvc != nil {
		go func() {
			tokens, err := h.deviceTokenRepo.ListByUser(bgCtx, userID)
			if err != nil {
				h.logger.Warn("notify: list device tokens", "error", err)
				return
			}
			for _, dt := range tokens {
				h.pushSvc.Send(dt.Token, n.Title, n.Body, dt.Sandbox)
			}
		}()
	}
}

// ListNotifications handles GET /api/v1/notifications
func (h *NotificationHandler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	notifs, err := h.notifRepo.ListByUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("list notifications", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	unread, err := h.notifRepo.UnreadCount(r.Context(), userID)
	if err != nil {
		h.logger.Error("unread count", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	out := make([]models.NotificationResponse, 0, len(notifs))
	for _, n := range notifs {
		out = append(out, models.NotificationResponse{
			ID:                    n.ID,
			Type:                  n.Type,
			Title:                 n.Title,
			Body:                  n.Body,
			RelatedChatID:         n.RelatedChatID,
			RelatedLeaseRequestID: n.RelatedLeaseRequestID,
			IsRead:                n.IsRead,
			CreatedAt:             models.NewRFC3339Time(n.CreatedAt),
		})
	}

	httputil.WriteJSON(w, http.StatusOK, models.NotificationsListResponse{
		Notifications: out,
		UnreadCount:   unread,
	})
}

// MarkRead handles POST /api/v1/notifications/{id}/read
func (h *NotificationHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	notifID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid notification ID"))
		return
	}

	if err := h.notifRepo.MarkRead(r.Context(), notifID, userID); err != nil {
		h.logger.Error("mark notification read", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// MarkAllRead handles POST /api/v1/notifications/read-all
func (h *NotificationHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	if err := h.notifRepo.MarkAllRead(r.Context(), userID); err != nil {
		h.logger.Error("mark all notifications read", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
