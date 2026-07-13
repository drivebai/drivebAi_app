package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// SupportHandler serves the user-facing support chat endpoints.
// All routes assume the caller has already passed AuthMiddleware.
type SupportHandler struct {
	supportRepo *repository.SupportRepository
	adminRepo   *repository.AdminRepository
	wsHub       *ws.Hub
	// notifHandler is optional. When wired (via SetNotificationHandler), an
	// inbound user support message creates a durable notification for each
	// admin so staff see the request even with the web console closed. Nil in
	// tests / local runs that don't need push — those paths skip the notify.
	notifHandler *NotificationHandler
	logger       *slog.Logger
}

func NewSupportHandler(
	supportRepo *repository.SupportRepository,
	adminRepo *repository.AdminRepository,
	wsHub *ws.Hub,
	logger *slog.Logger,
) *SupportHandler {
	return &SupportHandler{
		supportRepo: supportRepo,
		adminRepo:   adminRepo,
		wsHub:       wsHub,
		logger:      logger,
	}
}

// SetNotificationHandler wires the central NotificationHandler so inbound
// support messages produce durable per-admin notifications + pushes. Setter
// rather than a ctor arg so main.go can inject it after construction without
// breaking existing test constructors.
func (h *SupportHandler) SetNotificationHandler(n *NotificationHandler) {
	h.notifHandler = n
}

// GetOrCreate — POST /support/chats
// Returns the caller's support chat (creating it on first call).
func (h *SupportHandler) GetOrCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	chat, err := h.supportRepo.GetOrCreateChat(r.Context(), userID)
	if err != nil {
		h.logger.Error("support get-or-create chat", "user_id", userID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, chat)
}

// ListMessages — GET /support/chats/{chatId}/messages
func (h *SupportHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid chatId"))
		return
	}
	msgs, err := h.supportRepo.ListMessages(r.Context(), chatID, userID)
	if err != nil {
		if err.Error() == "support chat not found" {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "support chat not found"))
			return
		}
		if err.Error() == "not authorized" {
			httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "not authorized"))
			return
		}
		h.logger.Error("support list messages", "chat_id", chatID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

type sendSupportMessageBody struct {
	Body string `json:"body"`
}

// SendMessage — POST /support/chats/{chatId}/messages
// User sends a message; broadcasts support_message_created to all online admins.
func (h *SupportHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid chatId"))
		return
	}
	var body sendSupportMessageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Body) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("body is required"))
		return
	}
	body.Body = strings.TrimSpace(body.Body)

	msg, err := h.supportRepo.PostMessage(r.Context(), chatID, userID, body.Body)
	if err != nil {
		if err.Error() == "support chat not found" {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "support chat not found"))
			return
		}
		if err.Error() == "not authorized" {
			httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "not authorized"))
			return
		}
		h.logger.Error("support send message", "chat_id", chatID, "user_id", userID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Broadcast to all admins so their Support console updates instantly.
	adminIDs, aErr := h.adminRepo.GetAdminUserIDs(r.Context())
	if aErr != nil {
		h.logger.Warn("support send: could not fetch admin IDs for WS broadcast", "error", aErr)
	} else if len(adminIDs) > 0 {
		h.wsHub.Broadcast(&ws.Event{
			Type:          "support_message_created",
			Payload:       msg,
			TargetUserIDs: adminIDs,
		})

		// Durable fan-out: a WS broadcast only reaches admins with the console
		// open. Create a per-admin notification (+ push) so staff still see the
		// request with the console closed. System type keeps it grouped under
		// "system" in Notification Center. related_chat_id is left nil — it FKs
		// the chats table, not support_chats, so passing the support chat ID
		// would violate the constraint. Guarded so nil-handler runs skip notify.
		if h.notifHandler != nil {
			preview := body.Body
			if len(preview) > 140 {
				preview = preview[:140] + "…"
			}
			for _, adminID := range adminIDs {
				go h.notifHandler.Notify(adminID, models.NotificationTypeSystem,
					"New support message", preview, nil, nil)
			}
		}
	}

	h.logger.Info("support message sent", "chat_id", chatID, "user_id", userID, "msg_id", msg.ID)
	httputil.WriteJSON(w, http.StatusCreated, msg)
}

// MarkRead — POST /support/chats/{chatId}/read
func (h *SupportHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid chatId"))
		return
	}
	if err := h.supportRepo.MarkUserRead(r.Context(), chatID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "support chat not found"))
			return
		}
		h.logger.Error("support mark read", "chat_id", chatID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
