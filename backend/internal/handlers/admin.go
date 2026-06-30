package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// AdminHandler exposes admin-panel-only endpoints.
// All endpoints assume the caller has already passed AuthMiddleware + RequireRole(admin).
type AdminHandler struct {
	adminRepo *repository.AdminRepository
	// userRepo is used for the narrow profile-field update on
	// PATCH /admin/users/{id}/profile. We deliberately do NOT call the
	// broader UserRepository.Update from admin paths — see the
	// UpdateProfileFields docstring for the mass-assignment rationale.
	userRepo *repository.UserRepository
	wsHub    *ws.Hub
	// notifHandler is optional. When wired (via SetNotificationHandler),
	// admin-initiated state changes (support replies, profile edits) push
	// the affected user so they don't have to re-open the app to find out.
	// Nil in tests that don't need push.
	notifHandler *NotificationHandler
	logger       *slog.Logger
}

func NewAdminHandler(adminRepo *repository.AdminRepository, userRepo *repository.UserRepository, wsHub *ws.Hub, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{adminRepo: adminRepo, userRepo: userRepo, wsHub: wsHub, logger: logger}
}

// SetNotificationHandler wires the central NotificationHandler so admin
// actions can produce notifications + pushes. Setter rather than ctor arg
// to avoid breaking existing tests that build AdminHandler directly.
func (h *AdminHandler) SetNotificationHandler(n *NotificationHandler) {
	h.notifHandler = n
}

func parsePage(r *http.Request) (page, limit int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return
}

// ===== USERS =====

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	page, limit := parsePage(r)
	q := r.URL.Query().Get("query")
	role := r.URL.Query().Get("role")
	status := r.URL.Query().Get("status")

	res, err := h.adminRepo.ListUsers(r.Context(), q, role, status, page, limit)
	if err != nil {
		h.logger.Error("admin list users", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

func (h *AdminHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	u, err := h.adminRepo.GetUserDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "user not found"))
			return
		}
		h.logger.Error("admin get user", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, u)
}

type blockUserBody struct {
	Blocked bool `json:"blocked"`
}

func (h *AdminHandler) BlockUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body blockUserBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid body"))
		return
	}
	if err := h.adminRepo.SetUserBlocked(r.Context(), id, body.Blocked); err != nil {
		if errors.Is(err, models.ErrUserNotFound) {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "user not found"))
			return
		}
		h.logger.Error("admin block user", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "blocked": body.Blocked})

	// Notify the user only on UNblock — blocked users have their JWT cut
	// and can't act on a push anyway; alerting them they're blocked also
	// invites support spam. Unblock is the case worth notifying.
	if h.notifHandler != nil && !body.Blocked {
		go h.notifHandler.Notify(id, models.NotificationTypeSystem,
			"Account access restored",
			"Your DriveBai account has been unblocked. You can now sign back in.",
			nil, nil)
	}
}

// updateUserProfileBody is the explicit allow-list for admin profile edits.
// Every field is a pointer so omitted keys mean "leave unchanged". Adding a
// new sensitive column to `users` does NOT make it editable from admin —
// it has to be added here AND to UpdateProfileFields explicitly.
type updateUserProfileBody struct {
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
	Phone     *string `json:"phone,omitempty"`
}

// UpdateUserProfile — PATCH /admin/users/{id}/profile
//
// Admin can edit a target user's first_name, last_name, and phone only.
// Email is excluded because it doubles as the login identifier and would
// need OTP re-verification; role is excluded because the app uses a
// dedicated profile-switch flow; is_blocked has its own /block endpoint;
// password_hash and verification flags are never admin-editable from
// here. Mass-assignment-safe by construction: the body struct names only
// the safe fields, and UpdateProfileFields only writes those columns.
func (h *AdminHandler) UpdateUserProfile(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body updateUserProfileBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid body"))
		return
	}

	// Normalize + validate. Trim whitespace, enforce DB column limits,
	// reject "empty after trim" for required fields (first/last name).
	if body.FirstName != nil {
		trimmed := strings.TrimSpace(*body.FirstName)
		if trimmed == "" {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("first_name cannot be empty"))
			return
		}
		if len(trimmed) > 100 {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("first_name too long"))
			return
		}
		body.FirstName = &trimmed
	}
	if body.LastName != nil {
		trimmed := strings.TrimSpace(*body.LastName)
		if trimmed == "" {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("last_name cannot be empty"))
			return
		}
		if len(trimmed) > 100 {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("last_name too long"))
			return
		}
		body.LastName = &trimmed
	}
	if body.Phone != nil {
		trimmed := strings.TrimSpace(*body.Phone)
		if len(trimmed) > 20 {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("phone too long"))
			return
		}
		body.Phone = &trimmed
	}

	if err := h.userRepo.UpdateProfileFields(r.Context(), id, body.FirstName, body.LastName, body.Phone); err != nil {
		if errors.Is(err, models.ErrUserNotFound) {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "user not found"))
			return
		}
		h.logger.Error("admin update user profile", "error", err, "user_id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Return the refreshed AdminUser so the admin UI can swap the row
	// without a separate fetch.
	updated, err := h.adminRepo.GetUserDetail(r.Context(), id)
	if err != nil {
		h.logger.Error("admin reload user after update", "error", err, "user_id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, updated)

	// Notify the user that their profile changed. This catches the case
	// where admin edits the user's name/phone — the user otherwise has no
	// signal until they next open the app.
	if h.notifHandler != nil {
		go h.notifHandler.Notify(id, models.NotificationTypeSystem,
			"Profile updated",
			"An admin updated your profile information. Open the app to review the changes.",
			nil, nil)
	}
}

// ===== CARS =====

func (h *AdminHandler) ListCars(w http.ResponseWriter, r *http.Request) {
	page, limit := parsePage(r)
	q := r.URL.Query().Get("query")
	res, err := h.adminRepo.ListCars(r.Context(), q, page, limit)
	if err != nil {
		h.logger.Error("admin list cars", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

func (h *AdminHandler) GetCar(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	c, err := h.adminRepo.GetCarDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "car not found"))
			return
		}
		h.logger.Error("admin get car", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, c)
}

type approveCarBody struct {
	IsApproved bool `json:"is_approved"`
}

func (h *AdminHandler) ApproveCar(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body approveCarBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid body"))
		return
	}
	if err := h.adminRepo.SetCarApproved(r.Context(), id, body.IsApproved); err != nil {
		h.logger.Error("admin approve car", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "is_approved": body.IsApproved})
}

// ===== CHATS =====

func (h *AdminHandler) ListChats(w http.ResponseWriter, r *http.Request) {
	page, limit := parsePage(r)
	q := r.URL.Query().Get("query")
	res, err := h.adminRepo.ListChats(r.Context(), q, page, limit)
	if err != nil {
		h.logger.Error("admin list chats", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

func (h *AdminHandler) ListChatMessages(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	msgs, err := h.adminRepo.ListChatMessages(r.Context(), id, limit)
	if err != nil {
		h.logger.Error("admin list chat messages", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

type sendAdminChatMessageBody struct {
	Text string `json:"text"`
}

func (h *AdminHandler) SendChatMessage(w http.ResponseWriter, r *http.Request) {
	chatID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body sendAdminChatMessageBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("text is required"))
		return
	}
	adminID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	msg, driverID, ownerID, err := h.adminRepo.AdminSendChatMessage(r.Context(), chatID, adminID, strings.TrimSpace(body.Text))
	if err != nil {
		if err == models.ErrChatNotFound {
			httputil.WriteError(w, http.StatusNotFound, models.ErrChatNotFound)
			return
		}
		h.logger.Error("admin send chat message", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := models.MessageResponse{
		ID:          msg.ID,
		ChatID:      msg.ChatID,
		SenderID:    msg.SenderID,
		SenderName:  msg.SenderName,
		SenderKind:  msg.SenderKind,
		Type:        models.MessageTypeText,
		Body:        msg.Body,
		Attachments: make([]models.AttachmentResponse, 0),
		CreatedAt:   models.RFC3339Time(msg.CreatedAt),
	}

	h.wsHub.Broadcast(&ws.Event{
		Type:          "new_message",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{driverID, ownerID},
	})

	httputil.WriteJSON(w, http.StatusCreated, resp)
}

// ===== RENTS =====

func (h *AdminHandler) ListRents(w http.ResponseWriter, r *http.Request) {
	page, limit := parsePage(r)
	q := r.URL.Query().Get("query")
	status := r.URL.Query().Get("status")
	res, err := h.adminRepo.ListRents(r.Context(), q, status, page, limit)
	if err != nil {
		h.logger.Error("admin list rents", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, res)
}

func (h *AdminHandler) GetRent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	rent, err := h.adminRepo.GetRentDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "rent not found"))
			return
		}
		h.logger.Error("admin get rent", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, rent)
}

// ===== SUPPORT =====

func (h *AdminHandler) ListSupportChats(w http.ResponseWriter, r *http.Request) {
	chats, err := h.adminRepo.ListSupportChats(r.Context())
	if err != nil {
		h.logger.Error("admin list support", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

func (h *AdminHandler) ListSupportMessages(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	msgs, err := h.adminRepo.ListSupportMessages(r.Context(), id)
	if err != nil {
		h.logger.Error("admin list support msgs", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

type sendSupportBody struct {
	Body string `json:"body"`
}

func (h *AdminHandler) SendSupportMessage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body sendSupportBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Body) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("body is required"))
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	adminID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	msg, chatUserID, err := h.adminRepo.PostSupportMessage(r.Context(), id, adminID, "admin", body.Body)
	if err != nil {
		h.logger.Error("admin send support msg", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Push the admin reply to the user in real-time.
	if chatUserID != uuid.Nil {
		h.wsHub.Broadcast(&ws.Event{
			Type:          "support_message_created",
			Payload:       msg,
			TargetUserIDs: []uuid.UUID{chatUserID},
		})

		// Push notification so backgrounded users see the support reply
		// without having to refresh. System type keeps it grouped under
		// "system" in Notification Center rather than mixed with chats.
		if h.notifHandler != nil {
			preview := body.Body
			if len(preview) > 140 {
				preview = preview[:140] + "…"
			}
			go h.notifHandler.Notify(chatUserID, models.NotificationTypeSystem,
				"Support replied", preview, nil, nil)
		}
	}

	h.logger.Info("admin support message sent", "chat_id", id, "admin_id", adminID, "msg_id", msg.ID)
	httputil.WriteJSON(w, http.StatusCreated, msg)
}

// MarkSupportChatRead — POST /admin/support/chats/{id}/read
// Admin marks a support chat as read; resets the unread badge for this chat.
func (h *AdminHandler) MarkSupportChatRead(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	if err := h.adminRepo.MarkSupportChatAdminRead(r.Context(), id); err != nil {
		h.logger.Error("admin mark support read", "chat_id", id, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ===== ACCIDENTS / CAR SELL =====
// Tables not yet defined. Return empty paginated result so the UI works
// today and ships before backend schema is finalized. Future migrations
// will introduce `accident_reports` and `car_sell_forms` tables and these
// handlers will start returning real data.

func emptyPage(w http.ResponseWriter) {
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"items": []any{},
		"total": 0,
		"page":  1,
		"limit": 50,
	})
}

func (h *AdminHandler) ListAccidents(w http.ResponseWriter, r *http.Request) {
	page, limit := parsePage(r)
	status := r.URL.Query().Get("status")
	result, err := h.adminRepo.ListAccidents(r.Context(), page, limit, status)
	if err != nil {
		h.logger.Error("admin list accidents", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func (h *AdminHandler) GetAccident(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	accident, err := h.adminRepo.GetAccident(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "accident not found"))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, accident)
}

func (h *AdminHandler) UpdateAccidentStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("status is required"))
		return
	}
	validStatuses := map[string]bool{"draft": true, "submitted": true, "in_review": true, "resolved": true}
	if !validStatuses[body.Status] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid status"))
		return
	}
	if err := h.adminRepo.UpdateAccidentStatus(r.Context(), id, models.AccidentStatus(body.Status)); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
func (h *AdminHandler) ListCarSells(w http.ResponseWriter, r *http.Request) { emptyPage(w) }
func (h *AdminHandler) GetCarSell(w http.ResponseWriter, r *http.Request) {
	httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "car sell module not yet implemented"))
}
