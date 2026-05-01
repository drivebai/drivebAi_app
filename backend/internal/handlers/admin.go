package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// AdminHandler exposes admin-panel-only endpoints.
// All endpoints assume the caller has already passed AuthMiddleware + RequireRole(admin).
type AdminHandler struct {
	adminRepo *repository.AdminRepository
	logger    *slog.Logger
}

func NewAdminHandler(adminRepo *repository.AdminRepository, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{adminRepo: adminRepo, logger: logger}
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("body is required"))
		return
	}
	adminID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	msg, err := h.adminRepo.PostSupportMessage(r.Context(), id, adminID, "admin", body.Body)
	if err != nil {
		h.logger.Error("admin send support msg", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, msg)
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

func (h *AdminHandler) ListAccidents(w http.ResponseWriter, r *http.Request) { emptyPage(w) }
func (h *AdminHandler) GetAccident(w http.ResponseWriter, r *http.Request) {
	httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "accidents module not yet implemented"))
}
func (h *AdminHandler) ListCarSells(w http.ResponseWriter, r *http.Request) { emptyPage(w) }
func (h *AdminHandler) GetCarSell(w http.ResponseWriter, r *http.Request) {
	httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "car sell module not yet implemented"))
}
