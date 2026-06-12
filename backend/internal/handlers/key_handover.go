package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// KeyHandoverHandler serves the user-facing key-handover endpoints.
// All routes require AuthMiddleware.
type KeyHandoverHandler struct {
	repo         *repository.KeyHandoverRepository
	leaseRepo    *repository.LeaseRequestRepository
	carRepo      *repository.CarRepository
	userRepo     *repository.UserRepository
	wsHub        *ws.Hub
	notifHandler *NotificationHandler
	logger       *slog.Logger
}

func NewKeyHandoverHandler(
	repo *repository.KeyHandoverRepository,
	leaseRepo *repository.LeaseRequestRepository,
	carRepo *repository.CarRepository,
	userRepo *repository.UserRepository,
	wsHub *ws.Hub,
	notifHandler *NotificationHandler,
	logger *slog.Logger,
) *KeyHandoverHandler {
	return &KeyHandoverHandler{
		repo:         repo,
		leaseRepo:    leaseRepo,
		carRepo:      carRepo,
		userRepo:     userRepo,
		wsHub:        wsHub,
		notifHandler: notifHandler,
		logger:       logger,
	}
}

// Today — GET /key-handovers/today
// Active handovers (pending + owner_confirmed) where the user is owner or driver.
func (h *KeyHandoverHandler) Today(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	handovers, err := h.repo.ListActiveForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("list key handovers", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	out := make([]models.KeyHandoverResponse, 0, len(handovers))
	for i := range handovers {
		kh := h.expireIfOverdue(r.Context(), &handovers[i])
		// Expiry may have dropped it out of "active" — only include while actionable.
		if !kh.IsActive() {
			continue
		}
		out = append(out, h.buildResponse(r.Context(), kh, userID))
	}

	httputil.WriteJSON(w, http.StatusOK, models.KeyHandoversListResponse{KeyHandovers: out})
}

// Get — GET /key-handovers/{id}
func (h *KeyHandoverHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid handover id"))
		return
	}

	kh, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrKeyHandoverNotFound)
		return
	}
	kh = h.expireIfOverdue(r.Context(), kh)

	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), kh, userID))
}

// OwnerConfirm — POST /key-handovers/{id}/owner-confirm
// Owner marks the keys handed over. Starts the driver's confirmation window.
func (h *KeyHandoverHandler) OwnerConfirm(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid handover id"))
		return
	}

	kh, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrKeyHandoverNotFound)
		return
	}
	if userID != kh.OwnerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the car owner can confirm handover"))
		return
	}

	// Idempotent: already moved past pending → return current state.
	if kh.Status == models.KeyHandoverOwnerConfirmed || kh.Status == models.KeyHandoverCompleted {
		httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), kh, userID))
		return
	}
	if kh.Status != models.KeyHandoverPending {
		httputil.WriteError(w, http.StatusConflict, models.ErrInvalidHandoverAction)
		return
	}

	deadline := time.Now().Add(models.KeyHandoverConfirmWindow)
	updated, err := h.repo.OwnerConfirm(r.Context(), id, userID, deadline)
	if err != nil {
		if models.GetAPIError(err) == models.ErrInvalidHandoverAction {
			httputil.WriteError(w, http.StatusConflict, models.ErrInvalidHandoverAction)
			return
		}
		h.logger.Error("owner confirm handover", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), updated, userID)
	h.broadcast("key_handover_owner_confirmed", updated)

	// Notify the driver to confirm receipt before the window closes.
	chatID := resp.ChatID
	leaseID := updated.LeaseRequestID
	go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeKeyHandover,
		"Keys handed over",
		fmt.Sprintf("The owner marked the keys for %s as handed over. Confirm receipt within 15 minutes to start your rental.", carTitleOr(resp.CarTitle)),
		chatID, &leaseID)

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// DriverConfirm — POST /key-handovers/{id}/driver-confirm
// Driver confirms receipt. Completes the handover and starts the rental clock.
func (h *KeyHandoverHandler) DriverConfirm(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid handover id"))
		return
	}

	kh, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrKeyHandoverNotFound)
		return
	}
	if userID != kh.DriverID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the driver can confirm receipt"))
		return
	}

	// Expire first if the window already closed (also notifies both parties).
	kh = h.expireIfOverdue(r.Context(), kh)

	// Idempotent: already completed → return current state.
	if kh.Status == models.KeyHandoverCompleted {
		httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), kh, userID))
		return
	}
	if kh.Status == models.KeyHandoverExpired {
		httputil.WriteError(w, http.StatusConflict, models.ErrHandoverExpired)
		return
	}
	if kh.Status != models.KeyHandoverOwnerConfirmed {
		httputil.WriteError(w, http.StatusConflict, models.ErrInvalidHandoverAction)
		return
	}

	updated, err := h.repo.DriverConfirm(r.Context(), id, userID)
	if err != nil {
		// Lost the race against the deadline — expire and report it.
		if models.GetAPIError(err) == models.ErrInvalidHandoverAction {
			expired := h.expireIfOverdue(r.Context(), kh)
			if expired.Status == models.KeyHandoverExpired {
				httputil.WriteError(w, http.StatusConflict, models.ErrHandoverExpired)
				return
			}
			httputil.WriteError(w, http.StatusConflict, models.ErrInvalidHandoverAction)
			return
		}
		h.logger.Error("driver confirm handover", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), updated, userID)
	h.broadcast("key_handover_completed", updated)

	// Notify the owner that the rental has started.
	chatID := resp.ChatID
	leaseID := updated.LeaseRequestID
	go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypeKeyHandover,
		"Handover complete",
		fmt.Sprintf("%s confirmed receiving the keys for %s. The rental has now started.", nameOr(resp.DriverName, "The driver"), carTitleOr(resp.CarTitle)),
		chatID, &leaseID)

	httputil.WriteJSON(w, http.StatusOK, resp)
}

// Dismiss — POST /key-handovers/{id}/dismiss
// Per-user "Got it" for a terminal pickup-refunded card. Idempotent.
// Allowed only when the backing lease is in a terminal pickup state
// (expired_refunded, declined, cancelled) so users can't dismiss an
// active card to hide it from themselves.
func (h *KeyHandoverHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid handover id"))
		return
	}

	// 1. Must be a participant. GetByIDForUser returns ErrLeaseRequestNotFound-
	//    shaped 404 for non-participants so we don't leak existence.
	kh, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrKeyHandoverNotFound)
		return
	}

	// 2. Gate on the backing lease's terminal pickup state. The handover row
	//    itself may still be 'pending' at this point (we never auto-expire
	//    handovers when the lease is refunded — the dismiss IS the cleanup).
	lr, err := h.leaseRepo.GetByID(r.Context(), kh.LeaseRequestID)
	if err != nil || lr == nil {
		h.logger.Error("dismiss: fetch lease", "error", err, "handover_id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !isPickupTerminal(lr.Status) {
		httputil.WriteError(w, http.StatusConflict, models.ErrHandoverNotDismissable)
		return
	}

	// 3. Idempotent upsert. Repeated calls just return success.
	if err := h.repo.DismissForUser(r.Context(), id, userID); err != nil {
		h.logger.Error("dismiss handover", "error", err, "handover_id", id, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

// isPickupTerminal reports whether a lease is in a state that retired the
// pickup flow (refund issued, request rescinded, etc.). These are the only
// states that allow the Today card to be dismissed.
func isPickupTerminal(s models.LeaseRequestStatus) bool {
	switch s {
	case models.LeaseStatusExpiredRefunded,
		models.LeaseStatusCancelled,
		models.LeaseStatusDeclined,
		models.LeaseStatusExpired:
		return true
	}
	return false
}

// expireIfOverdue transitions an owner_confirmed handover to expired once its
// deadline has passed, notifying both parties exactly once.
func (h *KeyHandoverHandler) expireIfOverdue(ctx context.Context, kh *models.KeyHandover) *models.KeyHandover {
	if kh.Status != models.KeyHandoverOwnerConfirmed || kh.ConfirmationDeadline == nil {
		return kh
	}
	if kh.ConfirmationDeadline.After(time.Now()) {
		return kh
	}

	updated, didExpire, err := h.repo.Expire(ctx, kh.ID)
	if err != nil {
		h.logger.Error("expire handover", "error", err, "handover_id", kh.ID)
		return kh
	}
	if didExpire {
		h.broadcast("key_handover_expired", updated)
		carTitle := carTitleOr(h.carTitle(ctx, updated.CarID))
		chatID := h.chatIDForLease(ctx, updated.LeaseRequestID)
		leaseID := updated.LeaseRequestID
		body := fmt.Sprintf("The key handover for %s expired because receipt wasn't confirmed in time. Please coordinate in chat or contact support.", carTitle)
		go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypeKeyHandover, "Key handover expired", body, chatID, &leaseID)
		go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeKeyHandover, "Key handover expired", body, chatID, &leaseID)
	}
	return updated
}

// broadcast notifies both participants over WebSocket so their Today tabs refetch.
func (h *KeyHandoverHandler) broadcast(eventType string, kh *models.KeyHandover) {
	h.wsHub.Broadcast(&ws.Event{
		Type:          eventType,
		Payload:       map[string]any{"id": kh.ID, "lease_request_id": kh.LeaseRequestID, "status": kh.Status},
		TargetUserIDs: []uuid.UUID{kh.OwnerID, kh.DriverID},
	})
}

func (h *KeyHandoverHandler) buildResponse(ctx context.Context, kh *models.KeyHandover, viewerID uuid.UUID) models.KeyHandoverResponse {
	resp := models.KeyHandoverResponse{
		ID:                   kh.ID,
		LeaseRequestID:       kh.LeaseRequestID,
		CarID:                kh.CarID,
		OwnerID:              kh.OwnerID,
		DriverID:             kh.DriverID,
		PickupLatitude:       kh.PickupLatitude,
		PickupLongitude:      kh.PickupLongitude,
		Status:               kh.Status,
		OwnerConfirmedAt:     models.NewRFC3339TimePtr(kh.OwnerConfirmedAt),
		DriverConfirmedAt:    models.NewRFC3339TimePtr(kh.DriverConfirmedAt),
		ConfirmationDeadline: models.NewRFC3339TimePtr(kh.ConfirmationDeadline),
		StartedAt:            models.NewRFC3339TimePtr(kh.StartedAt),
		CreatedAt:            models.RFC3339Time(kh.CreatedAt),
		UpdatedAt:            models.RFC3339Time(kh.UpdatedAt),
	}
	if kh.PickupArea != nil {
		resp.PickupArea = *kh.PickupArea
	}

	if owner, err := h.userRepo.GetByID(ctx, kh.OwnerID); err == nil {
		resp.OwnerName = owner.FullName()
	}
	if driver, err := h.userRepo.GetByID(ctx, kh.DriverID); err == nil {
		resp.DriverName = driver.FullName()
	}
	resp.CarTitle = h.carTitle(ctx, kh.CarID)

	// Single lease fetch: source the chat id AND mirror the pickup-deadline
	// + extension fields onto the response so the Today tab can drive its
	// countdown + "Add more time" UI without a second round-trip.
	if lr, err := h.leaseRepo.GetByID(ctx, kh.LeaseRequestID); err == nil && lr != nil {
		chatID := lr.ChatID
		resp.ChatID = &chatID

		status := lr.Status
		resp.LeaseStatus = &status
		resp.PickupDeadlineAt = models.NewRFC3339TimePtr(lr.PickupDeadlineAt)
		resp.PickupConfirmedAt = models.NewRFC3339TimePtr(lr.PickupConfirmedAt)
		resp.PickupExtensionTotalMinutes = lr.PickupExtensionTotalMinutes
		resp.PickupExtensionCount = lr.PickupExtensionCount
		resp.PickupExtensionRemainingMin = lr.RemainingExtensionMinutes()
		resp.PickupLastExtendedAt = models.NewRFC3339TimePtr(lr.PickupLastExtendedAt)
	}

	if viewerID == kh.OwnerID {
		resp.ViewerRole = "owner"
		resp.CounterpartyName = resp.DriverName
	} else {
		resp.ViewerRole = "driver"
		resp.CounterpartyName = resp.OwnerName
	}
	return resp
}

func (h *KeyHandoverHandler) carTitle(ctx context.Context, carID uuid.UUID) string {
	if car, err := h.carRepo.GetByID(ctx, carID); err == nil {
		return car.Title
	}
	return ""
}

func (h *KeyHandoverHandler) chatIDForLease(ctx context.Context, leaseID uuid.UUID) *uuid.UUID {
	if lr, err := h.leaseRepo.GetByID(ctx, leaseID); err == nil {
		id := lr.ChatID
		return &id
	}
	return nil
}

func carTitleOr(title string) string {
	if title == "" {
		return "the car"
	}
	return title
}

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}
