package handlers

// Rolling amendments (price / interval): OFFER → DRIVER ACCEPTS → applies
// from the NEXT cycle. The recorded disclosure promises "this amount never
// changes without a new agreement from you" — so nothing here ever writes
// the mandate on the owner's word alone. Acceptance mints a successor
// consent (fresh evidence, same card) in one transaction; the current
// paid week is never touched. Interval changes are proposed through the
// same machinery but acceptance is GATED until the monthly bounds ship —
// 28-day cycles need their own derived limits before real money runs on
// them.

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// intervalAmendmentsEnabled gates weekly↔monthly acceptance until the
// 28-day bounds batch lands (dunning copy, per-day divisors, disclosure
// text, notice lead — see the amendment checkpoint's derivation).
const intervalAmendmentsEnabled = false

// ProposeAmendment — POST /api/v1/lease-requests/{id}/billing/amendments
// Owner-only. Body: {kind: "price"|"interval", new_amount_cents, new_interval?}.
func (h *LeaseRequestHandler) ProposeAmendment(w http.ResponseWriter, r *http.Request) {
	lr, userID, ok := h.loadRollingLeaseForParticipant(w, r, false)
	if !ok {
		return
	}
	if userID != lr.OwnerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "only the car owner can propose a billing change"))
		return
	}
	var body struct {
		Kind           string `json:"kind"`
		NewAmountCents int64  `json:"new_amount_cents"`
		NewInterval    string `json:"new_interval"`
	}
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if body.Kind != "price" && body.Kind != "interval" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("kind must be 'price' or 'interval'"))
		return
	}
	if body.Kind == "interval" && !intervalAmendmentsEnabled {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("INTERVAL_CHANGE_NOT_READY",
			"switching between weekly and monthly billing isn't available yet"))
		return
	}
	if body.NewAmountCents <= 0 || body.NewAmountCents > 100000000 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("new_amount_cents must be a positive amount"))
		return
	}
	interval := body.NewInterval
	if interval == "" {
		interval = "weekly"
	}
	if interval != "weekly" && interval != "monthly" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("new_interval must be 'weekly' or 'monthly'"))
		return
	}
	// Amendments belong to a LIVE tenancy: not returned, not stopping, no
	// live return handshake, and an activated mandate to amend.
	if lr.Status != models.LeaseStatusPaid || lr.VehicleReturnedAt != nil || lr.RenewalStoppedAt != nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RENTAL_NOT_LIVE",
			"billing changes apply to an active rental — this one is ending or has ended"))
		return
	}
	ctx := r.Context()
	consent, cerr := h.billingRepo.GetActiveConsent(ctx, lr.ID)
	if cerr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if consent == nil || !consent.Active() {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_MANDATE",
			"weekly billing isn't active on this rental — there is nothing to amend"))
		return
	}
	if body.Kind == "price" && body.NewAmountCents == consent.AmountCents {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_CHANGE", "that is already the current amount"))
		return
	}

	offer, oerr := h.billingRepo.CreateAmendmentOffer(ctx, lr.ID, userID, body.Kind,
		body.NewAmountCents, interval, models.AmendmentOfferTTL)
	if oerr != nil {
		h.logger.Error("amendment: create", "error", oerr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if offer == nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("OFFER_OPEN",
			"a proposal is already waiting for the driver — withdraw it before making another"))
		return
	}
	chatID := lr.ChatID
	leaseRef := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
		"Price change proposed",
		fmt.Sprintf("The owner proposes $%.2f/week starting from your next rental week (currently $%.2f). Nothing changes unless you accept — the offer expires %s.",
			float64(offer.NewAmountCents)/100, float64(consent.AmountCents)/100,
			offer.ExpiresAt.Format("Mon, Jan 2")),
		&chatID, &leaseRef)
	h.logger.Info("amendment proposed", "lease_request_id", lr.ID, "kind", offer.Kind,
		"new_amount_cents", offer.NewAmountCents)
	httputil.WriteJSON(w, http.StatusCreated, map[string]interface{}{"amendment": offer})
}

// loadAmendmentForLease resolves an offer id + its rolling lease.
func (h *LeaseRequestHandler) loadAmendmentForLease(w http.ResponseWriter, r *http.Request) (*models.BillingAmendmentOffer, *models.LeaseRequest, uuid.UUID, bool) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return nil, nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid amendment id"))
		return nil, nil, uuid.Nil, false
	}
	if h.billingRepo == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("BILLING_DISABLED", "billing engine not configured"))
		return nil, nil, uuid.Nil, false
	}
	offer, oerr := h.billingRepo.GetAmendment(r.Context(), id)
	if oerr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return nil, nil, uuid.Nil, false
	}
	if offer == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("AMENDMENT_NOT_FOUND", "no such proposal"))
		return nil, nil, uuid.Nil, false
	}
	lr, lerr := h.leaseRepo.GetByID(r.Context(), offer.LeaseRequestID)
	if lerr != nil || lr == nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return nil, nil, uuid.Nil, false
	}
	return offer, lr, userID, true
}

// AcceptAmendment — POST /api/v1/billing/amendments/{id}/accept
// Driver-only. The acceptance IS the new agreement: the successor consent
// records the freshly-rendered disclosure verbatim, and the new amount
// applies from the next cycle — never the current paid week.
func (h *LeaseRequestHandler) AcceptAmendment(w http.ResponseWriter, r *http.Request) {
	offer, lr, userID, ok := h.loadAmendmentForLease(w, r)
	if !ok {
		return
	}
	if userID != lr.DriverID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "only the driver can accept a billing change"))
		return
	}
	if offer.Kind == "interval" && !intervalAmendmentsEnabled {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("INTERVAL_CHANGE_NOT_READY",
			"switching between weekly and monthly billing isn't available yet"))
		return
	}
	if lr.VehicleReturnedAt != nil || lr.RenewalStoppedAt != nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RENTAL_NOT_LIVE",
			"this rental is ending — the proposal no longer applies"))
		return
	}
	// The successor consent records the v2 package rendered at the NEW
	// amount — the same text shape the driver originally agreed to.
	disclosure := models.RollingDriverDisclosureV2(offer.NewAmountCents)
	consent, err := h.billingRepo.AcceptAmendment(r.Context(), offer.ID, models.TermsVersionRollingV2, disclosure)
	if err != nil {
		switch err {
		case repository.ErrAmendmentGone:
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("AMENDMENT_GONE", "this proposal is no longer open"))
		case repository.ErrAmendmentCycleOpen:
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CYCLE_IN_FLIGHT",
				"this week's charge is still resolving — accept once it completes (the offer stays open)"))
		case repository.ErrAmendmentNoMandate:
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_MANDATE", "weekly billing is not active on this rental"))
		default:
			h.logger.Error("amendment: accept", "error", err, "amendment_id", offer.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}
	chatID := lr.ChatID
	leaseRef := lr.ID
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
		"Price change accepted",
		fmt.Sprintf("The driver accepted $%.2f/week — it applies from the next rental week.", float64(offer.NewAmountCents)/100),
		&chatID, &leaseRef)
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"New weekly amount agreed",
		fmt.Sprintf("From your next rental week you'll be charged $%.2f/week. Your current week is unaffected.", float64(offer.NewAmountCents)/100),
		&chatID, &leaseRef)
	h.logger.Info("amendment accepted", "lease_request_id", lr.ID, "amendment_id", offer.ID,
		"new_amount_cents", offer.NewAmountCents)
	if fresh, gerr := h.leaseRepo.GetByID(r.Context(), lr.ID); gerr == nil && fresh != nil {
		h.broadcastLeaseUpdate(r.Context(), fresh)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"amount_cents":  consent.AmountCents,
		"interval":      consent.BillingInterval,
		"terms_version": consent.TermsVersion,
	})
}

// DeclineAmendment — POST /api/v1/billing/amendments/{id}/decline
// Driver-only. The rental CONTINUES at the agreed terms — declining a
// proposal never ends anything; the owner's recourse is ending auto-renew.
func (h *LeaseRequestHandler) DeclineAmendment(w http.ResponseWriter, r *http.Request) {
	offer, lr, userID, ok := h.loadAmendmentForLease(w, r)
	if !ok {
		return
	}
	if userID != lr.DriverID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "only the driver can decline a billing change"))
		return
	}
	closed, err := h.billingRepo.CloseAmendment(r.Context(), offer.ID, "declined")
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !closed {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("AMENDMENT_GONE", "this proposal is no longer open"))
		return
	}
	consent, _ := h.billingRepo.GetActiveConsent(r.Context(), lr.ID)
	cur := ""
	if consent != nil {
		cur = fmt.Sprintf(" at $%.2f/week", float64(consent.AmountCents)/100)
	}
	chatID := lr.ChatID
	leaseRef := lr.ID
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
		"Price change declined",
		fmt.Sprintf("The driver declined $%.2f — the rental continues%s. If you don't want to continue at that price, you can end auto-renew from the rental card.",
			float64(offer.NewAmountCents)/100, cur),
		&chatID, &leaseRef)
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"declined": true})
}

// WithdrawAmendment — POST /api/v1/billing/amendments/{id}/withdraw
// Owner-only undo of a pending proposal.
func (h *LeaseRequestHandler) WithdrawAmendment(w http.ResponseWriter, r *http.Request) {
	offer, lr, userID, ok := h.loadAmendmentForLease(w, r)
	if !ok {
		return
	}
	if userID != lr.OwnerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "only the proposer can withdraw"))
		return
	}
	closed, err := h.billingRepo.CloseAmendment(r.Context(), offer.ID, "withdrawn")
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !closed {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("AMENDMENT_GONE", "this proposal is no longer open"))
		return
	}
	chatID := lr.ChatID
	leaseRef := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
		"Proposal withdrawn",
		"The owner withdrew the proposed billing change — your rental continues unchanged.",
		&chatID, &leaseRef)
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"withdrawn": true})
}
