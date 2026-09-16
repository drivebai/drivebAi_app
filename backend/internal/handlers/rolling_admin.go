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
	//
	// The first arm is RE-ENTRY, and it is what makes every failure below
	// recoverable. Waiving a week is not one write: it closes the debt,
	// clears the delinquency, lifts the renewal halt, advances paid-through
	// and tells the driver. Those steps live ONLY here — no scanner, no
	// driver action and no other endpoint performs them. So if the request
	// dies after the cycle flips to 'waived', an admin must be able to run
	// it again and finish the job. Without this arm the retry would be
	// refused CYCLE_IN_FLIGHT by the default case below, and the lease would
	// sit waived-but-unrepaired with SQL as the only remedy — the exact
	// shape this batch exists to remove.
	alreadyWaived := cycle.Status == models.CycleWaived
	switch {
	case alreadyWaived:
	case cycle.Status == models.CycleFailedFinal || cycle.Status == models.CycleArrearsDue:
	case cycle.Status == models.CycleScheduled && cycle.AttemptCount == 0 &&
		(cycle.StripePaymentIntentID == nil || *cycle.StripePaymentIntentID == ""):
	default:
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CYCLE_IN_FLIGHT",
			"this week's charge is still resolving — wait for it to land or fail, then waive or refund the result"))
		return
	}

	// Every step after this point is replay-safe: Close is claim-once,
	// ClearDelinquency and ClearRenewalHaltReporting are claimed-once, and
	// AdvanceOnCycleWaived uses GREATEST. A second pass therefore completes
	// what the first one dropped without double-effect.
	if !alreadyWaived {
		waived, werr := h.billingRepo.WaiveUnpaidCycle(r.Context(), cycleID, "admin waive: "+note)
		if werr != nil {
			h.logger.Error("admin waive cycle: waive", "error", werr, "cycle_id", cycleID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if !waived {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CYCLE_NOT_WAIVABLE",
				"only an unpaid cycle can be waived — paid weeks settle through refunds, and this one may already be closed"))
			return
		}
	} else {
		h.logger.Info("admin waive cycle: re-entry on an already-waived cycle — finishing the remaining steps",
			"cycle_id", cycleID, "lease_request_id", cycle.LeaseRequestID)
	}

	// Forgiving the week must also forgive the DEBT it raised. Without this
	// the driver's balance stays open, and since a balance blocks new
	// bookings the waive would leave them stuck with no exit — the opposite
	// of what an admin pressing "waive" intends.
	//
	// This is NOT best-effort, and the difference matters. The cycle is
	// already 'waived' by the time we get here, and a waived cycle is
	// invisible to every exit the driver has: pay-now answers NOTHING_DUE
	// (GetOpenOrLatestPaidCycle excludes 'waived'), a second waive answers
	// CYCLE_NOT_WAIVABLE, and a driver who somehow does pay is routed to
	// refundLateChargeOnSettledCycle, which returns the money and leaves the
	// debt open. So a swallowed failure here mints exactly the orphan debt
	// BlockingBalanceFor now has to route around — we would be fighting a
	// producer we control.
	//
	// Failing the request is safe ONLY because of the re-entry arm added to
	// the status guard above: a retry re-enters with the cycle already
	// 'waived', skips the waive itself, and lands here again to finish the
	// job. Without that arm this 500 would be a dead end — the guard would
	// answer CYCLE_IN_FLIGHT forever. If you ever remove the re-entry arm,
	// remove this fail-closed too.
	debtClosed := false
	if h.debtRepo != nil {
		debt, derr := h.debtRepo.GetByCycle(r.Context(), cycleID)
		if derr != nil {
			h.logger.Error("admin waive cycle: load debt", "error", derr, "cycle_id", cycleID)
			httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("DEBT_NOT_FORGIVEN",
				"the week was waived but its debt could not be read — retry this waive; it is safe to repeat"))
			return
		}
		if debt != nil {
			closed, cerr := h.debtRepo.Close(r.Context(), debt.ID, models.DebtWaived, "admin",
				"admin waive: "+note)
			if cerr != nil {
				h.logger.Error("admin waive cycle: close debt", "error", cerr, "debt_id", debt.ID, "cycle_id", cycleID)
				httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("DEBT_NOT_FORGIVEN",
					"the week was waived but its debt is still open — retry this waive; it is safe to repeat"))
				return
			}
			if closed {
				debtClosed = true
				h.logger.Info("driver debt waived with cycle", "debt_id", debt.ID, "cycle_id", cycleID)
			}
		}
	}

	// Forgiving the blocking week lifts the delinquency (both claimed-once;
	// a halt owned by another reason — dispute, return, stop — stays put).
	delinquencyCleared := false
	if cleared, cerr := h.leaseRepo.ClearDelinquency(r.Context(), cycle.LeaseRequestID); cerr != nil {
		h.logger.Error("admin waive cycle: clear delinquency", "error", cerr, "lease_request_id", cycle.LeaseRequestID)
	} else if cleared {
		delinquencyCleared = true
	}
	haltCleared, haltLapsed, herr := h.leaseRepo.ClearRenewalHaltReporting(r.Context(), cycle.LeaseRequestID, "delinquent")
	if haltCleared {
		noteUnbilledDays(r.Context(), h.ticketRepo, h.leaseRepo, h.logger, cycle.LeaseRequestID, "delinquent", haltLapsed)
	}
	if herr != nil {
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
	// A re-entry that repaired nothing is a no-op and must not tell the
	// driver a second time that their week was waived. A re-entry that DID
	// finish an interrupted waive still notifies, because the first attempt
	// may well have died before this line.
	// Deliberately NOT including `advanced`: AdvanceOnCycleWaived returns
	// RowsAffected()==1 whenever the lease matches its WHERE clause, so it
	// reports ELIGIBILITY, not an actual change — GREATEST makes the replay a
	// no-op while still touching the row. Only the three claim-once signals
	// above prove this pass did real work.
	//
	// Known, accepted gap: if an interrupted waive got as far as clearing the
	// halt and died before the advance, the re-entry finishes the advance but
	// stays silent, so that driver is never told. A missed notice on a rare
	// interrupted waive is a smaller harm than telling every driver twice
	// whenever support re-presses the button, and the waive is visible in the
	// app either way.
	changedSomething := !alreadyWaived || debtClosed || delinquencyCleared || haltCleared
	if lr, gerr := h.leaseRepo.GetByID(r.Context(), cycle.LeaseRequestID); gerr == nil && lr != nil && changedSomething {
		chatID := lr.ChatID
		leaseRef := lr.ID
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"A rental week was waived",
			"Support waived an unpaid week of your rental — you won't be charged for it.",
			&chatID, &leaseRef)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"cycle":                 updated,
		"delinquency_cleared":   delinquencyCleared,
		"paid_through_advanced": advanced,
		// True when this call found the week already waived. The console
		// should say "already waived — remaining steps completed" rather
		// than reporting a fresh waive.
		"already_waived": alreadyWaived,
		"repaired":       alreadyWaived && changedSomething,
	})
}
