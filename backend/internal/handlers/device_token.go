package handlers

import (
	"log/slog"
	"net/http"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

type DeviceTokenHandler struct {
	repo   *repository.DeviceTokenRepository
	logger *slog.Logger
}

func NewDeviceTokenHandler(repo *repository.DeviceTokenRepository, logger *slog.Logger) *DeviceTokenHandler {
	return &DeviceTokenHandler{repo: repo, logger: logger}
}

// RegisterDeviceToken handles POST /api/v1/me/device-token
func (h *DeviceTokenHandler) RegisterDeviceToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var body models.RegisterDeviceTokenBody
	if err := httputil.DecodeJSON(r, &body); err != nil || body.Token == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("token is required"))
		return
	}

	platform := body.Platform
	if platform == "" {
		platform = "ios"
	}

	if err := h.repo.Upsert(r.Context(), userID, body.Token, platform, body.Sandbox); err != nil {
		h.logger.Error("register device token", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// DeleteDeviceToken handles DELETE /api/v1/me/device-token
func (h *DeviceTokenHandler) DeleteDeviceToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var body models.DeleteDeviceTokenBody
	if err := httputil.DecodeJSON(r, &body); err != nil || body.Token == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("token is required"))
		return
	}

	if err := h.repo.Delete(r.Context(), userID, body.Token); err != nil {
		h.logger.Error("delete device token", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
