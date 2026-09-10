package handlers

// Driver-facing rolling-billing surface (batch 4): the billing status card,
// on-session Pay-now recovery, and the card-update flow (the
// consent_revoked exit). Everything here is reachable only for a lease
// whose billing_mode is 'rolling' — fixed-term leases 404/409 by predicate,
// never by flag.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	stripeService "github.com/drivebai/backend/internal/stripe"
)

// loadRollingLeaseForParticipant loads the lease and enforces rolling +
// participant. Driver-only endpoints pass driverOnly=true.
func (h *LeaseRequestHandler) loadRollingLeaseForParticipant(w http.ResponseWriter, r *http.Request, driverOnly bool) (*models.LeaseRequest, uuid.UUID, bool) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return nil, uuid.Nil, false
	}
	leaseID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid lease request ID"))
		return nil, uuid.Nil, false
	}
	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil || lr == nil {
		if err != nil && !isNotFoundErr(err) {
			h.logger.Error("rolling billing: load lease", "error", err, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return nil, uuid.Nil, false
		}
		httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
		return nil, uuid.Nil, false
	}
	if userID != lr.DriverID && (driverOnly || userID != lr.OwnerID) {
		httputil.WriteError(w, http.StatusForbidden, models.ErrNotLeaseDriver)
		return nil, uuid.Nil, false
	}
	if lr.BillingMode != models.BillingModeRolling || h.billingRepo == nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NOT_ROLLING",
			"this rental is a fixed-term lease — weekly billing does not apply"))
		return nil, uuid.Nil, false
	}
	return lr, userID, true
}

func isNotFoundErr(err error) bool {
	if err == pgx.ErrNoRows {
		return true
	}
	apiErr := models.GetAPIError(err)
	return apiErr != nil && apiErr == models.ErrLeaseRequestNotFound
}

// GetBillingStatus — GET /api/v1/lease-requests/{id}/billing
// The iOS billing card's source: mandate summary, next charge, the open
// cycle (with its client secret for the DRIVER ONLY — it authorizes an
// on-session confirm), and any post-return arrears.
func (h *LeaseRequestHandler) GetBillingStatus(w http.ResponseWriter, r *http.Request) {
	lr, userID, ok := h.loadRollingLeaseForParticipant(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	consent, err := h.billingRepo.GetActiveConsent(ctx, lr.ID)
	if err != nil {
		h.logger.Error("billing status: consent", "error", err, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := map[string]interface{}{
		"billing_mode":          lr.BillingMode,
		"rental_ends_at":        lr.RentalEndsAt,
		"renewal_stopped_at":    lr.RenewalStoppedAt,
		"renewal_halted_reason": lr.RenewalHaltedReason,
		"delinquent_since":      lr.DelinquentSince,
		"vehicle_returned_at":   lr.VehicleReturnedAt,
	}
	if consent != nil {
		resp["amount_cents"] = consent.AmountCents
		resp["card_brand"] = consent.CardBrand
		resp["card_last4"] = consent.CardLast4
		resp["consent_active"] = consent.Active()
		resp["terms_version"] = consent.TermsVersion
	}
	// Next charge: T−24h before paid-through, only while renewals are live.
	if lr.RentalEndsAt != nil && lr.RenewalStoppedAt == nil && lr.RenewalHaltedReason == nil &&
		lr.VehicleReturnedAt == nil && consent != nil && consent.Active() {
		resp["next_charge_at"] = lr.RentalEndsAt.Add(-models.BillingChargeLead)
	}
	// The single open cycle (structural invariant: at most one).
	if open, oerr := h.billingRepo.GetOpenCycleForLease(ctx, lr.ID); oerr == nil && open != nil {
		cycleOut := map[string]interface{}{
			"id":           open.ID,
			"status":       open.Status,
			"amount_cents": open.AmountCents,
			"period_start": open.PeriodStart,
			"period_end":   open.PeriodEnd,
		}
		// The client secret authorizes a confirm — driver's hands only,
		// only for states a driver action can rescue, and never on a lease
		// where a success must not land (stopped, or return in flight —
		// batch-4 verification: a confirm there extends or erases what the
		// settlement machinery owns).
		if userID == lr.DriverID && h.stripe != nil && open.StripePaymentIntentID != nil &&
			lr.RenewalStoppedAt == nil && lr.RenewalHaltedReason == nil && lr.VehicleReturnedAt == nil &&
			(open.Status == models.CycleNeedsAction || open.Status == models.CycleRetrying || open.Status == models.CycleFailedFinal) {
			if pi, perr := h.stripe.RetrievePaymentIntent(*open.StripePaymentIntentID); perr == nil &&
				pi.Status != "succeeded" && pi.Status != "canceled" {
				cycleOut["client_secret"] = pi.ClientSecret
			}
		}
		resp["open_cycle"] = cycleOut
	} else if oerr != nil {
		h.logger.Error("billing status: open cycle", "error", oerr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// A pending amendment is part of the billing picture for BOTH parties —
	// including the EXACT disclosure acceptance would record, so the driver
	// sees what they are agreeing to before they agree to it.
	if amendment, aerr := h.billingRepo.GetOpenAmendmentForLease(ctx, lr.ID); aerr == nil && amendment != nil {
		out := map[string]interface{}{"offer": amendment}
		if consent != nil && amendment.Kind == "price" {
			out["disclosure_preview"] = models.RollingAmendmentDisclosure(amendment.NewAmountCents, consent.AmountCents)
		}
		resp["pending_amendment"] = out
	}
	// Post-return arrears (its own bucket — the open-cycle query excludes it).
	if latest, lerr := h.billingRepo.GetOpenOrLatestPaidCycle(ctx, lr.ID); lerr == nil && latest != nil &&
		latest.Status == models.CycleArrearsDue {
		resp["arrears"] = map[string]interface{}{
			"cycle_id":     latest.ID,
			"amount_cents": latest.AmountCents,
			"period_start": latest.PeriodStart,
			"period_end":   latest.PeriodEnd,
		}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// PayNow — POST /api/v1/lease-requests/{id}/billing/pay-now
// Driver-only, on-session recovery. Two shapes:
//   - LIVE lease with an unpaid open cycle: hand back the cycle's OWN
//     intent's client secret — the design's §5 recovery is an on-session
//     confirm of the same PI, so success rides the normal webhook →
//     handleCyclePaid → paid-through advance + delinquency clears.
//   - RETURNED lease with arrears_due: mint a fresh ON-SESSION intent for
//     exactly the pro-rata owed (metadata kind=arrears). Success routes to
//     the dedicated arrears webhook branch — deliberately NOT
//     handleCyclePaid, whose occupancy guard would auto-refund any charge
//     landing after a return.
func (h *LeaseRequestHandler) PayNow(w http.ResponseWriter, r *http.Request) {
	lr, _, ok := h.loadRollingLeaseForParticipant(w, r, true)
	if !ok {
		return
	}
	if h.stripe == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("PAYMENTS_DISABLED", "payments not configured"))
		return
	}
	ctx := r.Context()

	// Returned lease → arrears path.
	if lr.VehicleReturnedAt != nil {
		latest, lerr := h.billingRepo.GetOpenOrLatestPaidCycle(ctx, lr.ID)
		if lerr != nil {
			h.logger.Error("pay-now: arrears lookup", "error", lerr, "lease_request_id", lr.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if latest == nil || latest.Status != models.CycleArrearsDue {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NOTHING_DUE", "there is no outstanding balance on this rental"))
			return
		}
		user, uerr := h.userRepo.GetByID(ctx, lr.DriverID)
		if uerr != nil || user == nil {
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		customer, cerr := customerForUser(ctx, h.stripe, h.userRepo, user, h.logger)
		if cerr != nil {
			h.logger.Error("pay-now: resolve customer", "error", cerr, "lease_request_id", lr.ID)
			httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "could not reach the payment provider"))
			return
		}
		// Stable key per (cycle, owed): a waive+re-arrears would change the
		// amount and must not collide with a cached create.
		idemKey := fmt.Sprintf("arrears-%s-%d", latest.ID, latest.AmountCents)
		pi, perr := h.stripe.CreatePaymentIntentWithOptions(latest.AmountCents, "usd", customer.ID, 0, idemKey,
			stripeService.PaymentIntentOptions{Metadata: map[string]string{
				"kind":             "arrears",
				"billing_cycle_id": latest.ID.String(),
				"lease_request_id": lr.ID.String(),
			}})
		if perr != nil {
			h.logger.Error("pay-now: create arrears intent", "error", perr, "cycle_id", latest.ID)
			httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "could not start the payment"))
			return
		}
		// The stable key returns the SAME intent for 24h — if it already
		// succeeded (webhook delayed or lost), settle inline instead of
		// showing a payable sheet for money already taken (batch-4
		// verification MEDIUM).
		if pi.Status == "succeeded" {
			if !h.handleArrearsPaid(ctx, latest.ID, pi.ID) {
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_PAID",
				"this balance was already paid — it may take a moment to reflect"))
			return
		}
		ek := ""
		if k, ekErr := h.stripe.CreateEphemeralKey(customer.ID); ekErr == nil {
			ek = k.Secret
		}
		httputil.WriteJSON(w, http.StatusOK, models.PaymentIntentResponse{
			PaymentIntentClientSecret: pi.ClientSecret,
			PaymentIntentID:           pi.ID,
			PublishableKey:            h.stripe.PublishableKey(),
			CustomerID:                customer.ID,
			EphemeralKeySecret:        ek,
			Amount:                    latest.AmountCents,
			Currency:                  "USD",
		})
		return
	}

	// A live return handshake pauses everything — paying a week mid-return
	// would just buy a refund (the settlement treats it as overshoot).
	if lr.RenewalHaltedReason != nil && *lr.RenewalHaltedReason == "return_initiated" {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RETURN_IN_PROGRESS",
			"a vehicle return is in progress — payments resume if the return is cancelled"))
		return
	}
	// Belt beyond the single-slot halt (batch-4 verification HIGH): the
	// halt slot may be owned by another reason while a return is live.
	if h.returnRepoForDisputes != nil {
		if ret, rerr := h.returnRepoForDisputes.GetByLeaseRequestID(ctx, lr.ID); rerr == nil && ret != nil &&
			ret.Status != models.VehicleReturnCompleted && ret.Status != models.VehicleReturnCancelled {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RETURN_IN_PROGRESS",
				"a vehicle return is in progress — payments resume if the return is cancelled"))
			return
		}
	}
	// A stopped rental takes no more money — a success here would extend
	// a termination the owner or driver already chose (batch-4 verification).
	if lr.RenewalStoppedAt != nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RENEWALS_STOPPED",
			"auto-renew was ended on this rental — no further weekly charges apply"))
		return
	}

	// Live lease → rescue the open cycle's own intent.
	open, oerr := h.billingRepo.GetOpenCycleForLease(ctx, lr.ID)
	if oerr != nil {
		h.logger.Error("pay-now: open cycle", "error", oerr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if open == nil || open.StripePaymentIntentID == nil || *open.StripePaymentIntentID == "" {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NOTHING_DUE", "there is no payment waiting on this rental"))
		return
	}
	pi, perr := h.stripe.RetrievePaymentIntent(*open.StripePaymentIntentID)
	if perr != nil {
		h.logger.Error("pay-now: retrieve intent", "error", perr, "cycle_id", open.ID)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "could not reach the payment provider"))
		return
	}
	switch pi.Status {
	case "succeeded":
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_PAID", "this week's payment already went through — it may take a moment to reflect"))
		return
	case "canceled":
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NOTHING_DUE", "this week's charge is no longer collectable — contact support"))
		return
	}
	user, uerr := h.userRepo.GetByID(ctx, lr.DriverID)
	if uerr != nil || user == nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	customerID := ""
	ek := ""
	if cid, cerr := h.userRepo.GetStripeCustomerID(ctx, user.ID); cerr == nil && cid != nil {
		customerID = *cid
		if k, ekErr := h.stripe.CreateEphemeralKey(customerID); ekErr == nil {
			ek = k.Secret
		}
	}
	httputil.WriteJSON(w, http.StatusOK, models.PaymentIntentResponse{
		PaymentIntentClientSecret: pi.ClientSecret,
		PaymentIntentID:           pi.ID,
		PublishableKey:            h.stripe.PublishableKey(),
		CustomerID:                customerID,
		EphemeralKeySecret:        ek,
		Amount:                    open.AmountCents,
		Currency:                  "USD",
	})
}

// CardUpdateStart — POST /api/v1/lease-requests/{id}/billing/card-update
// Driver-only. Mints a SetupIntent so PaymentSheet (setup mode) can save a
// replacement card. Allowed while halted — fixing the card IS the
// consent_revoked exit — but not after the rental ended.
func (h *LeaseRequestHandler) CardUpdateStart(w http.ResponseWriter, r *http.Request) {
	lr, _, ok := h.loadRollingLeaseForParticipant(w, r, true)
	if !ok {
		return
	}
	if h.stripe == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("PAYMENTS_DISABLED", "payments not configured"))
		return
	}
	if lr.VehicleReturnedAt != nil || lr.Status != models.LeaseStatusPaid {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RENTAL_ENDED", "this rental has ended — there is no future charge to update a card for"))
		return
	}
	ctx := r.Context()
	consent, cerr := h.billingRepo.GetActiveConsent(ctx, lr.ID)
	if cerr != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if consent == nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_MANDATE", "weekly billing was never set up on this rental"))
		return
	}
	// An UNACTIVATED consent is deliberately allowed through (batch-4
	// verification MEDIUM: the consent_revoked halt's realistic producer is
	// exactly the unactivated crash-window consent — refusing here left
	// the halt with no exit). Completion activates it from the verified
	// SetupIntent.
	user, uerr := h.userRepo.GetByID(ctx, lr.DriverID)
	if uerr != nil || user == nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	customer, custErr := customerForUser(ctx, h.stripe, h.userRepo, user, h.logger)
	if custErr != nil {
		h.logger.Error("card update: resolve customer", "error", custErr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "could not reach the payment provider"))
		return
	}
	si, serr := h.stripe.CreateSetupIntent(customer.ID, map[string]string{
		"kind":             "consent_card_update",
		"lease_request_id": lr.ID.String(),
	}, "")
	if serr != nil {
		h.logger.Error("card update: create setup intent", "error", serr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "could not start the card update"))
		return
	}
	ek := ""
	if k, ekErr := h.stripe.CreateEphemeralKey(customer.ID); ekErr == nil {
		ek = k.Secret
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"setup_intent_client_secret": si.ClientSecret,
		"setup_intent_id":            si.ID,
		"publishable_key":            h.stripe.PublishableKey(),
		"customer_id":                customer.ID,
		"ephemeral_key_secret":       ek,
	})
}

// CardUpdateComplete — POST /api/v1/lease-requests/{id}/billing/card-update/complete
// Body: {setup_intent_id}. Verifies the SetupIntent AT STRIPE (fail-closed
// — the client's word is not evidence), swaps the mandate's card, and
// lifts a consent_revoked halt so renewals resume next sweep.
func (h *LeaseRequestHandler) CardUpdateComplete(w http.ResponseWriter, r *http.Request) {
	lr, _, ok := h.loadRollingLeaseForParticipant(w, r, true)
	if !ok {
		return
	}
	if h.stripe == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("PAYMENTS_DISABLED", "payments not configured"))
		return
	}
	var body struct {
		SetupIntentID string `json:"setup_intent_id"`
	}
	if err := httputil.DecodeJSON(r, &body); err != nil || body.SetupIntentID == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("setup_intent_id is required"))
		return
	}
	ctx := r.Context()
	si, card, serr := h.stripe.RetrieveSetupIntent(body.SetupIntentID)
	if serr != nil {
		h.logger.Error("card update: retrieve setup intent", "error", serr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "could not verify the card update"))
		return
	}
	// The SI must be succeeded, carry a payment method, and BELONG to this
	// lease (metadata) — otherwise any driver could replay someone else's
	// setup id.
	if si.Status != "succeeded" || si.PaymentMethod == "" {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("SETUP_INCOMPLETE",
			"the card wasn't saved — finish the card form and try again"))
		return
	}
	if card.Metadata["kind"] != "consent_card_update" || card.Metadata["lease_request_id"] != lr.ID.String() {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("SETUP_MISMATCH", "this card update does not belong to this rental"))
		return
	}
	// Activate-or-update: an unactivated consent (the crash-window halt
	// state) is ACTIVATED from the verified SetupIntent; an active one has
	// its card swapped. Both claimed/status-scoped in the repo.
	updated, uerr := h.billingRepo.UpdateConsentPaymentMethod(ctx, lr.ID, si.PaymentMethod, card.Brand, card.Last4, card.Fingerprint)
	if uerr != nil {
		h.logger.Error("card update: swap consent pm", "error", uerr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !updated {
		activated, aerr := h.billingRepo.ActivateConsent(ctx, lr.ID, si.PaymentMethod, card.Brand, card.Last4, card.Fingerprint)
		if aerr != nil {
			h.logger.Error("card update: activate consent", "error", aerr, "lease_request_id", lr.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if !activated {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_MANDATE", "weekly billing is not active on this rental"))
			return
		}
	}
	// Claim-scoped: only the consent_revoked reason lifts — a delinquency,
	// dispute, or live-return halt is not cured by a new card. A FAILURE
	// here fails the whole request (batch-4 verification MEDIUM: this
	// endpoint is the halt's ONLY clearer and is safely re-runnable — a
	// 200 with the halt stuck would end the rental after a success push).
	if _, herr := h.leaseRepo.ClearRenewalHalt(ctx, lr.ID, "consent_revoked"); herr != nil {
		h.logger.Error("card update: clear halt failed — failing request for retry", "error", herr, "lease_request_id", lr.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	h.logger.Info("rolling consent card updated", "lease_request_id", lr.ID, "brand", card.Brand, "last4", card.Last4)
	chatID := lr.ChatID
	leaseRef := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Card updated",
		fmt.Sprintf("Weekly payments for your rental now use the %s card ending %s.", card.Brand, card.Last4),
		&chatID, &leaseRef)
	if fresh, gerr := h.leaseRepo.GetByID(ctx, lr.ID); gerr == nil && fresh != nil {
		h.broadcastLeaseUpdate(ctx, fresh)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"card_brand": card.Brand,
		"card_last4": card.Last4,
		"updated":    true,
	})
}

// handleArrearsPaid processes a kind=arrears success (batch 4): the driver
// settled a post-return debt on-session. Deliberately separate from
// handleCyclePaid — the occupancy guard there auto-refunds any charge
// landing after a return, which is exactly wrong for arrears. H2 rule:
// false = webhook 500s until every write is durable.
func (h *LeaseRequestHandler) handleArrearsPaid(ctx context.Context, cycleID uuid.UUID, intentID string) bool {
	cycle, gerr := h.billingRepo.GetCycle(ctx, cycleID)
	if gerr != nil {
		h.logger.Error("arrears paid: load cycle", "error", gerr, "cycle_id", cycleID)
		return false
	}
	if cycle == nil {
		return true // intent points at a deleted cycle — nothing to do
	}
	switch cycle.Status {
	case models.CycleArrearsDue:
		claimed, cerr := h.billingRepo.ArrearsPaidClaim(ctx, cycle.ID, intentID)
		if cerr != nil {
			h.logger.Error("arrears paid: claim", "error", cerr, "cycle_id", cycle.ID)
			return false
		}
		if !claimed {
			return false // racing writer owns the transition; redelivery re-checks
		}
	case models.CyclePaid:
		// Identity check (batch-4 review HIGH): the debt may have been
		// settled by a DIFFERENT arrears intent — a stale PaymentSheet
		// confirmed after its idempotency key expired mints a second
		// charge. Refund the foreign one; never silently keep it.
		if intentID != "" && (cycle.StripePaymentIntentID == nil || *cycle.StripePaymentIntentID != intentID) {
			return h.refundLateChargeOnSettledCycle(ctx, cycle, intentID)
		}
		// Redelivery after a crash between the claim and the payout write —
		// fall through and finish the ledger idempotently.
	case models.CycleWaived, models.CycleRefunded, models.CyclePartiallyRefunded:
		// The debt was written off (or money already returned) before this
		// landed — same discipline as any late charge on a settled cycle:
		// refund in full, never silently keep.
		return h.refundLateChargeOnSettledCycle(ctx, cycle, intentID)
	default:
		h.logger.Error("arrears paid: unexpected cycle status", "cycle_id", cycle.ID, "status", cycle.Status)
		return false
	}

	lr, lerr := h.leaseRepo.GetByID(ctx, cycle.LeaseRequestID)
	if lerr != nil || lr == nil {
		h.logger.Error("arrears paid: load lease", "error", lerr, "lease_request_id", cycle.LeaseRequestID)
		return false
	}
	// Credit the driver-level balance. Idempotent on the intent id, so a
	// redelivered webhook moves nothing; a partial payment simply lowers the
	// balance and leaves the debt open.
	// Credit the driver-level balance. Idempotent on the intent id, so a
	// redelivered webhook moves nothing; a partial payment simply lowers the
	// balance and leaves the debt open.
	//
	// H2 rule: this must NOT be best-effort. The balance blocks new bookings,
	// so swallowing a failure here leaves a driver who has paid in full
	// blocked forever, with the webhook ACKed and no redelivery coming.
	// Return false and let Stripe retry.
	if h.debtRepo != nil {
		debt, derr := h.debtRepo.GetByCycle(ctx, cycle.ID)
		if derr != nil {
			h.logger.Error("arrears paid: load debt", "error", derr, "cycle_id", cycle.ID)
			return false
		}
		if debt != nil {
			_, applied, aerr := h.debtRepo.ApplyPayment(ctx, debt.ID, cycle.AmountCents, intentID, "driver")
			if aerr != nil {
				h.logger.Error("arrears paid: apply to debt", "error", aerr, "debt_id", debt.ID)
				return false
			}
			if applied {
				h.logger.Info("driver debt paid down", "debt_id", debt.ID, "amount_cents", cycle.AmountCents)
			}
		}
	}
	// Pure redelivery (payout already written): ACK without re-sending
	// the settlement notifications (batch-4 verification LOW).
	if cycle.Status == models.CyclePaid {
		if row, rerr := h.payoutRepo.GetByBillingCycleID(ctx, cycle.ID); rerr != nil {
			return false
		} else if row != nil {
			return true
		}
	}
	// The owner's share of collected arrears settles directly to pending —
	// promotion's consumed-week guards don't apply to a week that already
	// ended (design: "owner's share settles from collected cents only").
	fee, ownerShare := models.ComputePayoutSplit(cycle.AmountCents, h.billingFeeBPS)
	cycleRef, ps, pe := cycle.ID, cycle.PeriodStart, cycle.PeriodEnd
	if perr := h.payoutRepo.FinalizeCyclePayoutRow(ctx, &models.OwnerPayout{
		LeaseRequestID:   &lr.ID,
		OwnerID:          lr.OwnerID,
		GrossKeptCents:   cycle.AmountCents,
		FeeBPS:           h.billingFeeBPS,
		FeeCents:         fee,
		OwnerAmountCents: ownerShare,
		Currency:         "USD",
		BillingCycleID:   &cycleRef,
		PeriodStart:      &ps,
		PeriodEnd:        &pe,
	}, "pending", "arrears collected on-session"); perr != nil {
		h.logger.Error("arrears paid: payout write", "error", perr, "cycle_id", cycle.ID)
		return false
	}
	// The collection ticket is finished work now (batch-4 verification LOW).
	if h.ticketRepo != nil {
		if terr := h.ticketRepo.ResolveForLeaseRequest(ctx, lr.ID); terr != nil {
			h.logger.Error("arrears paid: resolve ticket", "error", terr, "lease_request_id", lr.ID)
		}
	}
	h.logger.Info("arrears settled on-session", "cycle_id", cycle.ID, "amount_cents", cycle.AmountCents, "intent_id", intentID)
	chatID := lr.ChatID
	leaseRef := lr.ID
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Balance settled",
		fmt.Sprintf("Your payment of $%.2f settled the remaining balance on your rental. Thank you!", float64(cycle.AmountCents)/100),
		&chatID, &leaseRef)
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
		"Final week collected",
		fmt.Sprintf("The outstanding balance for your rental's final week was collected — your share of $%.2f transfers with the next payout run.", float64(ownerShare)/100),
		&chatID, &leaseRef)
	return true
}
