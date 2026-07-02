package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/push"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// pushFanOutConcurrency caps simultaneous APNs sends for one Notify call.
// 5 is well under the APNs HTTP/2 stream limit (1000 per connection) and
// keeps total fan-out time bounded even for power users with many devices.
const pushFanOutConcurrency = 5

// NotificationHandler exposes in-app notification endpoints.
// It is also the central helper called by other handlers (lease, payment) to
// create a notification + fire WS + attempt a push in one place.
type NotificationHandler struct {
	notifRepo       *repository.NotificationRepository
	deviceTokenRepo *repository.DeviceTokenRepository
	wsHub           *ws.Hub
	pushSvc         *push.Service // may be nil if APNs not configured
	logger          *slog.Logger
}

func NewNotificationHandler(
	notifRepo *repository.NotificationRepository,
	deviceTokenRepo *repository.DeviceTokenRepository,
	wsHub *ws.Hub,
	pushSvc *push.Service,
	logger *slog.Logger,
) *NotificationHandler {
	h := &NotificationHandler{
		notifRepo:       notifRepo,
		deviceTokenRepo: deviceTokenRepo,
		wsHub:           wsHub,
		pushSvc:         pushSvc,
		logger:          logger,
	}
	// Wire the repo as the push service's token-pruner so APNs 410/400 errors
	// drop the dead row without a separate cron. The push package can't
	// import repository directly (cycle), so it accepts an interface; the
	// repo's DeleteByToken satisfies it.
	if pushSvc != nil && deviceTokenRepo != nil {
		pushSvc.SetTokenInvalidator(deviceTokenRepo)
	}
	return h
}

// Notify creates a DB notification, broadcasts a WS event, and fires a push
// (best-effort). Uses a detached background context so a cancelled HTTP request
// doesn't drop the notification. Safe to call from any goroutine.
//
// Push delivery:
//   - The PushRequest is built per-type via buildPushRequest so iOS gets the
//     right category, collapse-id, and deep-link payload to route on tap.
//   - Fan-out across the user's devices is parallel (errgroup, bounded
//     concurrency); each send already retries internally on 5xx and prunes
//     dead tokens on 410/BadDeviceToken.
//   - Failures are logged but never returned — the in-app notification +
//     WS event are the source of truth; APNs is a notification surface.
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

	// Chat pushes are suppressed when the recipient is already foregrounded
	// over WS — they'll see the in-app banner instead. Saves a redundant
	// buzz on iOS for the (common) case where the user is in the app.
	if notifType == models.NotificationTypeChatMessage && relatedChatID != nil {
		if h.wsHub.IsSubscribedToChat(userID, *relatedChatID) {
			h.logger.Debug("notify: skipping chat push, recipient is online via WS",
				"user_id", userID, "chat_id", *relatedChatID)
			return
		}
	}

	// Push — non-blocking, best-effort. Even when pushSvc is nil we don't
	// short-circuit before logging the WS broadcast above.
	if h.pushSvc == nil {
		return
	}

	go h.dispatchPush(bgCtx, userID, n, unread, relatedChatID, relatedLeaseRequestID)
}

// dispatchPush builds the per-type PushRequest, looks up the user's device
// tokens, and fans out the send across them with bounded parallelism.
// Runs in its own goroutine (caller is non-blocking).
func (h *NotificationHandler) dispatchPush(
	ctx context.Context,
	userID uuid.UUID,
	n *models.Notification,
	unread int,
	relatedChatID *uuid.UUID,
	relatedLeaseRequestID *uuid.UUID,
) {
	tokens, err := h.deviceTokenRepo.ListByUser(ctx, userID)
	if err != nil {
		h.logger.Warn("notify: list device tokens", "error", err, "user_id", userID)
		return
	}
	if len(tokens) == 0 {
		return
	}

	baseReq := buildPushRequest(n.Type, n.Title, n.Body, unread, relatedChatID, relatedLeaseRequestID)

	g := new(errgroup.Group)
	g.SetLimit(pushFanOutConcurrency)
	for i := range tokens {
		dt := tokens[i]
		req := baseReq
		req.IsSandbox = dt.Sandbox
		g.Go(func() error {
			if err := h.pushSvc.Send(dt.Token, req); err != nil {
				// Token-unregistered is already pruned + logged inside Send;
				// 4xx is logged; 5xx after retries is logged. We swallow
				// here so other devices' sends still fire.
				h.logger.Debug("notify: push send failed",
					"error", err, "user_id", userID, "type", n.Type)
			}
			return nil
		})
	}
	_ = g.Wait()
}

// buildPushRequest is the per-NotificationType dispatcher. Each type gets:
//   - a category (drives iOS notification-action UI)
//   - a thread-id (groups Notification Center)
//   - a collapse-id (replaces a stack of related banners with one)
//   - deep-link data keys iOS reads on tap to route to the right screen
//
// Exposed (lowercase, package-private) for the table-test in notifications_test.go.
func buildPushRequest(
	notifType models.NotificationType,
	title, body string,
	unreadCount int,
	relatedChatID *uuid.UUID,
	relatedLeaseRequestID *uuid.UUID,
) push.PushRequest {
	badge := unreadCount
	req := push.PushRequest{
		Title:    title,
		Body:     body,
		Sound:    "default",
		Badge:    &badge,
		Priority: 10,
		Data:     map[string]string{"type": string(notifType)},
	}

	// Route the same relatedLeaseRequestID param to the correct payload
	// key based on the notification type family. Purchase-flow types
	// need `purchase_request_id` (that's what iOS DeepLinkRouter reads
	// for purchase_* taps); everything else keeps `lease_request_id`.
	// Without this branch, purchase pushes serialized the id under
	// `lease_request_id`, DeepLinkRouter never found `purchase_request_id`,
	// and every purchase-notification tap silently dropped on the floor.
	refStr := ""
	if relatedLeaseRequestID != nil {
		refStr = relatedLeaseRequestID.String()
		switch notifType {
		case models.NotificationTypePurchaseRequest,
			models.NotificationTypePurchasePayment,
			models.NotificationTypePurchaseHandover,
			models.NotificationTypePurchaseRejection:
			req.Data["purchase_request_id"] = refStr
		default:
			req.Data["lease_request_id"] = refStr
		}
	}
	// leaseRefStr kept as an alias for the rest of this function so the
	// existing collapse-id lines don't need to change per family.
	leaseRefStr := refStr
	chatRefStr := ""
	if relatedChatID != nil {
		chatRefStr = relatedChatID.String()
		req.Data["chat_id"] = chatRefStr
	}

	switch notifType {
	case models.NotificationTypeLeaseRequest:
		req.Category = "LEASE_REQUEST"
		req.ThreadID = "lease-requests"
		if leaseRefStr != "" {
			req.CollapseID = "lease:" + leaseRefStr
		}

	case models.NotificationTypePayment:
		req.Category = "PAYMENT"
		req.ThreadID = "payments"
		if leaseRefStr != "" {
			req.CollapseID = "payment:" + leaseRefStr
		}

	case models.NotificationTypeKeyHandover:
		req.Category = "KEY_HANDOVER"
		req.ThreadID = "key-handovers"
		if leaseRefStr != "" {
			req.CollapseID = "handover:" + leaseRefStr
		}

	case models.NotificationTypeChatMessage:
		req.Category = "CHAT_MESSAGE"
		req.Priority = 5 // chat is conversational, not time-critical
		if chatRefStr != "" {
			req.ThreadID = "chat:" + chatRefStr
			req.CollapseID = "chat:" + chatRefStr
		}

	case models.NotificationTypePurchaseRequest:
		req.Category = "PURCHASE_REQUEST"
		req.ThreadID = "purchase-requests"
		if leaseRefStr != "" {
			req.CollapseID = "purchase:" + leaseRefStr
		}

	case models.NotificationTypePurchasePayment:
		req.Category = "PURCHASE_PAYMENT"
		req.ThreadID = "purchase-payments"
		if leaseRefStr != "" {
			req.CollapseID = "purchase-payment:" + leaseRefStr
		}

	case models.NotificationTypePurchaseHandover:
		req.Category = "PURCHASE_HANDOVER"
		req.ThreadID = "purchase-handovers"
		if leaseRefStr != "" {
			req.CollapseID = "purchase-handover:" + leaseRefStr
		}

	case models.NotificationTypePurchaseRejection:
		req.Category = "PURCHASE_REJECTION"
		req.ThreadID = "purchase-rejections"
		if leaseRefStr != "" {
			req.CollapseID = "purchase-rejection:" + leaseRefStr
		}

	case models.NotificationTypeSystem:
		req.Category = "SYSTEM"
		req.ThreadID = "system"
		req.Priority = 5
		// No collapse-id: system events are distinct (support reply vs
		// admin profile change vs accident) and shouldn't overwrite each
		// other in the banner queue.

	default:
		// Unknown type — still deliver the alert but with no special grouping.
		req.Category = "SYSTEM"
		req.Priority = 5
	}

	return req
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

// MarkChatMessagesRead clears unread chat_message notifications for a
// single chat. Called by ChatHandler.MarkRead so opening a chat (which
// already nukes chat_participants.unread_count) also drains the matching
// notification rows — otherwise the bell counter + APNs badge inflate
// every time the user reads a backgrounded chat and never come back down.
//
// Exposed as a handler method (not a raw repo passthrough) so any future
// follow-up — push of an updated unread badge, an audit log, etc. — has
// a single hook. Best-effort: failures are logged + non-fatal so the
// chat MarkRead request still succeeds even if the notification update
// trips on a transient DB error.
func (h *NotificationHandler) MarkChatMessagesRead(ctx context.Context, userID, chatID uuid.UUID) {
	if err := h.notifRepo.MarkChatMessagesRead(ctx, userID, chatID); err != nil {
		h.logger.Warn("mark chat-message notifications read",
			"user_id", userID, "chat_id", chatID, "error", err)
	}
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

// formatChatPreview keeps push body length under iOS' practical 178-char
// banner cap and strips control bytes. Multi-line messages collapse to
// the first line + "…" so the lock-screen preview stays readable. Callers
// in chat.go pass message bodies through this before building the Notify
// call so the iOS banner is single-line.
func formatChatPreview(body string) string {
	const maxLen = 140
	first := body
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' || body[i] == '\r' {
			first = body[:i]
			break
		}
	}
	if len(first) > maxLen {
		first = first[:maxLen] + "…"
	} else if len(first) < len(body) {
		first = first + "…"
	}
	return first
}
