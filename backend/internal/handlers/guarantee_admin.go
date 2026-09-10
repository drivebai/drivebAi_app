package handlers

import (
	"net/http"
	"strconv"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// Bands for the trailing failure rate. Every guaranteed week is funded by
// nine collected weeks of platform fee, so the scheme is exactly underwater
// at 10%: that is the break-even, not a scare number. Amber starts where the
// guarantee begins eating a fifth of fee income.
const (
	guaranteeRateGreenMax  = 0.02
	guaranteeRateAmberMax  = 0.05
	guaranteeRateBreakEven = 0.10
	guaranteeRateWindow    = 200 // terminal weekly cycles
)

// AdminGuaranteeExposure — GET /api/v1/admin/payouts/guarantee-exposure
//
// What DriveBai has paid out of its own pocket against debt it has not
// collected: total, per owner, and the trailing rate that predicts the cost.
// The denominator is CYCLES, not rentals: the guarantee triggers per week, so
// each terminal week is one opportunity to fail and a per-rental rate does
// not predict spend.
func (h *PayoutHandler) AdminGuaranteeExposure(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	total, err := h.payoutRepo.GuaranteeExposureTotal(ctx)
	if err != nil {
		h.logger.Error("guarantee exposure: total", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	byOwner, err := h.payoutRepo.GuaranteeExposureByOwner(ctx, 50)
	if err != nil {
		h.logger.Error("guarantee exposure: by owner", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	window := guaranteeRateWindow
	if q := r.URL.Query().Get("window"); q != "" {
		if n, cerr := strconv.Atoi(q); cerr == nil && n > 0 && n <= 5000 {
			window = n
		}
	}
	guaranteed, terminal, err := h.payoutRepo.GuaranteeFailureRate(ctx, window)
	if err != nil {
		h.logger.Error("guarantee exposure: rate", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	rate := 0.0
	if terminal > 0 {
		rate = float64(guaranteed) / float64(terminal)
	}
	band := "green"
	switch {
	case rate > guaranteeRateBreakEven:
		band = "underwater"
	case rate > guaranteeRateAmberMax:
		band = "red"
	case rate > guaranteeRateGreenMax:
		band = "amber"
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":           h.guaranteeEnabled,
		"outstanding_cents": total.OutstandingCents,
		"paid_cents":        total.PaidCents,
		"recovered_cents":   total.RecoveredCents,
		"guarantee_count":   total.Count,
		"by_owner":          byOwner,
		"failure_rate":      rate,
		"failure_rate_band": band,
		"guaranteed_cycles": guaranteed,
		"terminal_cycles":   terminal,
		"window":            window,
		"break_even_rate":   guaranteeRateBreakEven,
		// Stated so the reader does not have to derive it: this is why 10%
		// is the line.
		"break_even_explanation": "a guaranteed week pays out 90% of a week; a collected week earns 10%, so one guarantee is funded by nine collected weeks",
	})
}
