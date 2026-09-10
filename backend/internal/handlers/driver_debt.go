package handlers

import (
	"net/http"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// GetMyBalance — GET /api/v1/me/balance
//
// The one number the driver sees. Returns zero rather than 404 when nothing
// is owed, so the app can render the clear state without special-casing.
func (h *LeaseRequestHandler) GetMyBalance(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	if h.debtRepo == nil {
		httputil.WriteJSON(w, http.StatusOK, models.DriverBalance{DriverID: userID, Currency: "USD"})
		return
	}
	bal, err := h.debtRepo.BalanceFor(r.Context(), userID)
	if err != nil {
		h.logger.Error("balance: read", "error", err, "driver_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	debts, derr := h.debtRepo.ListOpenFor(r.Context(), userID)
	if derr != nil {
		h.logger.Error("balance: list debts", "error", derr, "driver_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	bal.Debts = debts
	httputil.WriteJSON(w, http.StatusOK, bal)
}
