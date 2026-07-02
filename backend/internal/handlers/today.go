package handlers

import (
	"log/slog"
	"net/http"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

type TodayHandler struct {
	leaseRepo    *repository.LeaseRequestRepository
	userRepo     *repository.UserRepository
	purchaseRepo *repository.PurchaseRequestRepository
	logger       *slog.Logger
}

func NewTodayHandler(
	leaseRepo *repository.LeaseRequestRepository,
	userRepo *repository.UserRepository,
	logger *slog.Logger,
) *TodayHandler {
	return &TodayHandler{
		leaseRepo: leaseRepo,
		userRepo:  userRepo,
		logger:    logger,
	}
}

// SetPurchaseRepository wires the purchase repo so Today aggregates buyer
// + seller purchase cards alongside the lease-side ones. Setter (not ctor
// arg) so existing tests don't break.
func (h *TodayHandler) SetPurchaseRepository(p *repository.PurchaseRequestRepository) {
	h.purchaseRepo = p
}

// GetActions returns actionable items for the current user's Today tab.
//
// A single user can be both an owner and a driver, so we always fetch both
// sides and concatenate. The driver side is the "your request was accepted —
// pay now" card; the owner side is the "Approve lease request" card. iOS
// distinguishes them via `action.type` (lease_request vs lease_payment).
func (h *TodayHandler) GetActions(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	ownerActions, err := h.leaseRepo.ListTodayActionsForOwner(r.Context(), userID)
	if err != nil {
		h.logger.Error("get owner today actions", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	driverActions, err := h.leaseRepo.ListTodayActionsForDriver(r.Context(), userID)
	if err != nil {
		h.logger.Error("get driver today actions", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	actions := make([]models.TodayAction, 0, len(ownerActions)+len(driverActions))
	actions = append(actions, ownerActions...)
	actions = append(actions, driverActions...)

	// Purchase actions (buyer + seller) — same user can be both, mirroring
	// the driver + owner concat above.
	if h.purchaseRepo != nil {
		buyerActions, err := h.purchaseRepo.ListTodayActionsForBuyer(r.Context(), userID)
		if err != nil {
			h.logger.Error("get buyer today actions", "error", err)
		} else {
			actions = append(actions, buyerActions...)
		}
		sellerActions, err := h.purchaseRepo.ListTodayActionsForSeller(r.Context(), userID)
		if err != nil {
			h.logger.Error("get seller today actions", "error", err)
		} else {
			actions = append(actions, sellerActions...)
		}
	}

	// Unread badge — true if either side has activity since last seen.
	lastSeen, err := h.userRepo.GetLastSeenActionsAt(r.Context(), userID)
	if err != nil {
		h.logger.Error("get last seen actions at", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	hasUnreadOwner, err := h.leaseRepo.HasUnreadActions(r.Context(), userID, lastSeen)
	if err != nil {
		h.logger.Error("has unread owner actions", "error", err)
		hasUnreadOwner = false
	}
	hasUnreadDriver, err := h.leaseRepo.HasUnreadActionsForDriver(r.Context(), userID, lastSeen)
	if err != nil {
		h.logger.Error("has unread driver actions", "error", err)
		hasUnreadDriver = false
	}

	httputil.WriteJSON(w, http.StatusOK, models.TodayActionsResponse{
		Actions:          actions,
		HasUnreadActions: hasUnreadOwner || hasUnreadDriver,
	})
}

// MarkActionsSeen sets the user's last_seen_actions_at to now.
func (h *TodayHandler) MarkActionsSeen(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	if err := h.userRepo.UpdateLastSeenActionsAt(r.Context(), userID); err != nil {
		h.logger.Error("mark actions seen", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}
