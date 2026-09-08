package handlers

// Admin surface for the rolling cycle ledger (batch 3): read a lease's
// weeks + their payout rows, and waive an unpaid week. Waiving the week
// that made a driver delinquent lifts the delinquency so billing resumes —
// admin forgiveness is the failed_final exit besides return/support.

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// AdminListBillingCycles — GET /api/v1/admin/rents/{id}/billing-cycles
// The billing drawer's source: consent, every cycle, and the per-cycle
// payout ledger rows, oldest first.
func (h *LeaseRequestHandler) AdminListBillingCycles(w http.ResponseWriter, r *http.Request) {
	if h.billingRepo == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("BILLING_DISABLED", "billing engine not configured"))
		return
	}
	leaseID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid rent id"))
		return
	}
	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil || lr == nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
		return
	}
	cycles, err := h.billingRepo.ListCyclesForLease(r.Context(), leaseID)
	if err != nil {
		h.logger.Error("admin billing cycles: list", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	consent, err := h.billingRepo.GetActiveConsent(r.Context(), leaseID)
	if err != nil {
		h.logger.Error("admin billing cycles: consent", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	var ledger []*models.OwnerPayout
	if h.payoutRepo != nil {
		if ledger, err = h.payoutRepo.ListCycleLedgerForLease(r.Context(), leaseID); err != nil {
			h.logger.Error("admin billing cycles: ledger", "error", err, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	}
	openAmendment, _ := h.billingRepo.GetOpenAmendmentForLease(r.Context(), leaseID)
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"open_amendment":        openAmendment,
		"billing_mode":          lr.BillingMode,
		"rental_ends_at":        lr.RentalEndsAt,
		"renewal_halted_reason": lr.RenewalHaltedReason,
		"delinquent_since":      lr.DelinquentSince,
		"consent":               consent,
		"cycles":                cycles,
		"payouts":               ledger,
	})
}

// AdminWaiveBillingCycle — POST /api/v1/admin/billing-cycles/{id}/waive
// Body: {note}. Forgives an unpaid week (claimed-once in the repo — paid
// weeks refuse). If that week is what made the driver delinquent, the
// delinquency and its halt lift so renewals resume next sweep.
func (h *LeaseRequestHandler) AdminWaiveBillingCycle(w http.ResponseWriter, r *http.Request) {
	if h.billingRepo == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("BILLING_DISABLED", "billing engine not configured"))
		return
	}
	cycleID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid cycle id"))
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	// Waiving forgives money the owner would otherwise be owed — the
	// reasoning is non-optional, same bounds as every settlement note.
	note := strings.TrimSpace(body.Note)
	if n := utf8.RuneCountInString(note); n < 5 || n > 500 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("a waive note is required (5–500 characters)"))
		return
	}

	cycle, err := h.billingRepo.GetCycle(r.Context(), cycleID)
	if err != nil {
		h.logger.Error("admin waive cycle: load", "error", err, "cycle_id", cycleID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if cycle == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("CYCLE_NOT_FOUND", "no such billing cycle"))
		return
	}
	// Only provably-dead cycles may be waived from here (batch-3 review
	// HIGH): charging/retrying/needs_action still carry a Stripe intent
	// that can succeed — waiving over it tells the driver "you won't be
	// charged" moments before they are (the webhook backstop would refund
	// it, but the endpoint shouldn't manufacture that collision). Safe:
	// dunning exhausted, arrears, or scheduled-with-no-attempt-and-no-intent.
	switch {
	case cycle.Status == models.CycleFailedFinal || cycle.Status == models.CycleArrearsDue:
	case cycle.Status == models.CycleScheduled && cycle.AttemptCount == 0 &&
		(cycle.StripePaymentIntentID == nil || *cycle.StripePaymentIntentID == ""):
	default:
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CYCLE_IN_FLIGHT",
			"this week's charge is still resolving — wait for it to land or fail, then waive or refund the result"))
		return
	}

	waived, err := h.billingRepo.WaiveUnpaidCycle(r.Context(), cycleID, "admin waive: "+note)
	if err != nil {
		h.logger.Error("admin waive cycle: waive", "error", err, "cycle_id", cycleID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !waived {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CYCLE_NOT_WAIVABLE",
			"only an unpaid cycle can be waived — paid weeks settle through refunds, and this one may already be closed"))
		return
	}

	// Forgiving the blocking week lifts the delinquency (both claimed-once;
	// a halt owned by another reason — dispute, return, stop — stays put).
	delinquencyCleared := false
	if cleared, cerr := h.leaseRepo.ClearDelinquency(r.Context(), cycle.LeaseRequestID); cerr != nil {
		h.logger.Error("admin waive cycle: clear delinquency", "error", cerr, "lease_request_id", cycle.LeaseRequestID)
	} else if cleared {
		delinquencyCleared = true
	}
	if _, herr := h.leaseRepo.ClearRenewalHalt(r.Context(), cycle.LeaseRequestID, "delinquent"); herr != nil {
		h.logger.Error("admin waive cycle: clear halt", "error", herr, "lease_request_id", cycle.LeaseRequestID)
	}
	// On a LIVE rental the forgiven week must also ADVANCE paid-through
	// (batch-3 review HIGH: without it, the resumed engine re-mints — and
	// re-bills — the exact period just forgiven). Guarded to live rolling
	// occupancy in the repo; a returned lease no-ops.
	advanced := false
	if adv, aerr := h.leaseRepo.AdvanceOnCycleWaived(r.Context(), cycle.LeaseRequestID, cycle.PeriodEnd); aerr != nil {
		h.logger.Error("admin waive cycle: advance paid-through", "error", aerr, "lease_request_id", cycle.LeaseRequestID)
	} else if adv {
		advanced = true
	}

	updated, _ := h.billingRepo.GetCycle(r.Context(), cycleID)
	if updated == nil {
		updated = cycle
	}
	if lr, gerr := h.leaseRepo.GetByID(r.Context(), cycle.LeaseRequestID); gerr == nil && lr != nil {
		chatID := lr.ChatID
		leaseRef := lr.ID
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"A rental week was waived",
			"Support waived an unpaid week of your rental — you won't be charged for it.",
			&chatID, &leaseRef)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"cycle":                updated,
		"delinquency_cleared":  delinquencyCleared,
		"paid_through_advanced": advanced,
	})
}
