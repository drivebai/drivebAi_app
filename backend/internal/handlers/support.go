package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	// uploadDir + urlSigner support in-chat attachments (000040): files land on
	// the private /uploads/support path and are served only via signed URLs.
	uploadDir string
	urlSigner *PrivateURLSigner
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
	uploadDir string,
	urlSigner *PrivateURLSigner,
	logger *slog.Logger,
) *SupportHandler {
	return &SupportHandler{
		supportRepo: supportRepo,
		adminRepo:   adminRepo,
		wsHub:       wsHub,
		uploadDir:   uploadDir,
		urlSigner:   urlSigner,
		logger:      logger,
	}
}

// signMessages signs every attachment URL on a batch of messages on the way
// out. The DB stores raw /uploads paths; production requires a signature to
// serve them. Nil-signer safe (local/test runs without private serving).
func (h *SupportHandler) signMessages(msgs []repository.SupportMessageResponse) {
	if h.urlSigner == nil {
		return
	}
	for i := range msgs {
		for j := range msgs[i].Attachments {
			msgs[i].Attachments[j].FileURL = h.urlSigner.Sign(msgs[i].Attachments[j].FileURL)
		}
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
	h.signMessages(msgs)
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

// UploadAttachment — POST /support/chats/{chatId}/attachments
// Multipart in-chat attachment (photo from camera/library, or a document).
// Owner-or-admin: the chat's owner posts as 'user'; any admin posts as 'admin'
// (so support can send a screenshot back). Creates a message carrying the file
// with an optional caption ("body" form field), stores it on the private
// /uploads/support path, signs the URL, and fans out exactly like a text send.
func (h *SupportHandler) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	callerID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid chatId"))
		return
	}

	owner, err := h.supportRepo.GetChatOwner(r.Context(), chatID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "support chat not found"))
		return
	}
	role, _ := httputil.GetRole(r.Context())
	senderKind := ""
	switch {
	case callerID == owner:
		senderKind = "user"
	case role == models.RoleAdmin:
		senderKind = "admin"
	default:
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "not authorized"))
		return
	}

	if err := r.ParseMultipartForm(25 << 20); err != nil { // 25 MB
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data"))
		return
	}
	caption := strings.TrimSpace(r.FormValue("body"))
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		buf := make([]byte, 512)
		file.Read(buf)
		contentType = http.DetectContentType(buf)
		file.Seek(0, 0)
	}
	validTypes := map[string]string{
		"image/jpeg":      ".jpg",
		"image/jpg":       ".jpg",
		"image/png":       ".png",
		"image/heic":      ".heic",
		"image/heif":      ".heif",
		"application/pdf": ".pdf",
	}
	ext, valid := validTypes[contentType]
	if !valid {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("unsupported file type: "+contentType))
		return
	}

	dir := filepath.Join(h.uploadDir, "support", chatID.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.logger.Error("mkdir support attachments", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	filename := fmt.Sprintf("attach_%s%s", uuid.New().String(), ext)
	filePath := filepath.Join(dir, filename)
	data, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		h.logger.Error("write support attachment", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	fileURL := fmt.Sprintf("/uploads/support/%s/%s", chatID.String(), filename)
	msg, chatUserID, err := h.supportRepo.CreateAttachmentMessage(
		r.Context(), chatID, callerID, senderKind, caption, fileURL, filePath, contentType, int64(len(data)),
	)
	if err != nil {
		os.Remove(filePath)
		h.logger.Error("create support attachment message", "chat_id", chatID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Sign before we broadcast/return — every consumer (admin console, the other
	// participant's device) needs a serve-able URL, not the raw path.
	h.signMessages([]repository.SupportMessageResponse{*msg})

	h.fanOutAttachment(r, senderKind, chatUserID, msg, caption)

	h.logger.Info("support attachment sent", "chat_id", chatID, "sender", senderKind, "msg_id", msg.ID)
	httputil.WriteJSON(w, http.StatusCreated, msg)
}

// fanOutAttachment mirrors the text-send fan-out for an attachment message:
// a user upload notifies admins; an admin upload notifies the chat's user.
func (h *SupportHandler) fanOutAttachment(r *http.Request, senderKind string, chatUserID uuid.UUID, msg *repository.SupportMessageResponse, caption string) {
	preview := caption
	if preview == "" {
		preview = "📎 Attachment"
	}
	if len(preview) > 140 {
		preview = preview[:140] + "…"
	}

	if senderKind == "user" {
		adminIDs, aErr := h.adminRepo.GetAdminUserIDs(r.Context())
		if aErr != nil || len(adminIDs) == 0 {
			return
		}
		h.wsHub.Broadcast(&ws.Event{
			Type:          "support_message_created",
			Payload:       msg,
			TargetUserIDs: adminIDs,
		})
		if h.notifHandler != nil {
			for _, adminID := range adminIDs {
				go h.notifHandler.Notify(adminID, models.NotificationTypeSystem,
					"New support message", preview, nil, nil)
			}
		}
		return
	}

	// Admin -> user.
	if chatUserID == uuid.Nil {
		return
	}
	h.wsHub.Broadcast(&ws.Event{
		Type:          "support_message_created",
		Payload:       msg,
		TargetUserIDs: []uuid.UUID{chatUserID},
	})
	if h.notifHandler != nil {
		go h.notifHandler.Notify(chatUserID, models.NotificationTypeSupportMessage,
			"Message from DriveBai support", preview, nil, nil)
	}
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
