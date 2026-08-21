package handlers

import (
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"

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
		ActiveRentals:    h.buildActiveRentals(r, userID),
	})
}

// buildActiveRentals assembles the driver's rentals-in-progress cards
// (lifecycle batch, defect 3) — the standing "you have this car until X"
// view that existed for the owner but never for the driver. Errors degrade
// to no card rather than failing the whole feed: the actions above are the
// actionable half of Today.
func (h *TodayHandler) buildActiveRentals(r *http.Request, userID uuid.UUID) []models.DriverActiveRental {
	rows, err := h.leaseRepo.ListActiveRentalsForDriver(r.Context(), userID)
	if err != nil {
		h.logger.Error("today: list active rentals", "error", err, "user_id", userID)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	now := time.Now().UTC()
	out := make([]models.DriverActiveRental, 0, len(rows))
	for _, row := range rows {
		// Round, don't truncate: DECIMAL 349.90 arrives as 349.8999…;
		// int64() truncation would show the driver a cent less than paid.
		weeklyCents := int64(math.Round(row.WeeklyPriceDollar * 100))
		paidCents := weeklyCents * int64(row.Weeks)
		if row.PaidAmountCents != nil {
			paidCents = *row.PaidAmountCents
		}

		// Return location: the key-handover pickup snapshot when present
		// (that is where the parties actually met), else the car's listed
		// location. The source string drives the honest label — nothing in
		// the system stores a negotiated return point.
		area, lat, lng := row.PickupArea, row.PickupLat, row.PickupLng
		source := "pickup"
		if area == nil && lat == nil {
			area, lat, lng = row.ListingArea, row.ListingLat, row.ListingLng
			source = "listing"
		}

		out = append(out, models.DriverActiveRental{
			LeaseRequestID:       row.LeaseRequestID,
			CarID:                row.ListingID,
			CarTitle:             row.CarTitle,
			CarPhotoURL:          row.CoverPhotoPath,
			OwnerID:              row.OwnerID,
			OwnerName:            row.OwnerName,
			ChatID:               row.ChatID,
			PickupConfirmedAt:    models.RFC3339Time(row.PickupConfirmedAt),
			RentalEndsAt:         models.RFC3339Time(row.RentalEndsAt),
			DaysRemaining:        models.RentalDaysRemaining(row.RentalEndsAt, now),
			TermState:            models.ComputeRentalTermState(row.RentalEndsAt, now),
			Weeks:                row.Weeks,
			WeeklyCents:          weeklyCents,
			PaidCents:            paidCents,
			ReturnLocationArea:   area,
			ReturnLocationLat:    lat,
			ReturnLocationLng:    lng,
			ReturnLocationSource: source,
		})
	}
	return out
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
