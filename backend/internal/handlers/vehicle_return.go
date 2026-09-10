package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

// VehicleReturnHandler serves the user-facing return endpoints + runs the
// stuck-refund background scanner. Mirrors KeyHandoverHandler's shape so
// the auth helpers, response envelopes, and WS broadcasts stay consistent
// across the post-payment surfaces.
type VehicleReturnHandler struct {
	repo         *repository.VehicleReturnRepository
	leaseRepo    *repository.LeaseRequestRepository
	carRepo      *repository.CarRepository
	userRepo     *repository.UserRepository
	chatRepo     *repository.ChatRepository
	ticketRepo   *repository.TicketRepository
	stripe       *stripeService.Service
	wsHub        *ws.Hub
	notifHandler *NotificationHandler
	payoutH      *PayoutHandler
	logger       *slog.Logger
	// disputeRepoForBilling lets the return flow respect open disputes when
	// juggling the single-slot renewal halt (wired via setter; nil-safe).
	disputeRepoForBilling *repository.ChargeDisputeRepository
	// Rolling-settlement wiring (batch 3): a rolling return settles per
	// CYCLE — pro-rata out of the final week's own charge, full refund of
	// an unentered overshoot week — with the owner's share rewritten in
	// the cycle ledger instead of the legacy settleOwnerPayout row.
	billingRepo       *repository.BillingRepository
	billingPayoutRepo *repository.PayoutRepository
	// debtRepo records an uncollected final week as a driver-level debt.
	// Optional: nil leaves the return flow exactly as it was.
	debtRepo      *repository.DriverDebtRepository
	billingFeeBPS int
}

// SetDisputeRepository wires the dispute mirror for halt juggling.
func (h *VehicleReturnHandler) SetDisputeRepository(r *repository.ChargeDisputeRepository) {
	h.disputeRepoForBilling = r
}

// SetBillingDependencies wires the rolling cycle ledger (batch 3). Setter,
// per the house pattern; nil-safe — without it every lease settles legacy.
func (h *VehicleReturnHandler) SetBillingDependencies(b *repository.BillingRepository, p *repository.PayoutRepository, feeBPS int) {
	h.billingRepo = b
	h.billingPayoutRepo = p
	h.billingFeeBPS = feeBPS
}

// SetDebtRepository wires the driver-level debt ledger so an uncollected
// final week is recorded against the driver, not only against the cycle.
func (h *VehicleReturnHandler) SetDebtRepository(d *repository.DriverDebtRepository) {
	h.debtRepo = d
}

// SetPayoutHandler wires the owner-payout engine so a completed return
// settles the owner's share (Stripe Connect batch). Setter, per the house
// pattern.
func (h *VehicleReturnHandler) SetPayoutHandler(p *PayoutHandler) {
	h.payoutH = p
}

// SetTicketRepository wires the support-ticket repo so a dispute opens a
// real ticket (lifecycle batch, defect 4 — the "our team will reach out"
// copy used to promise contact that nothing delivered). Setter, not a ctor
// arg, per the house pattern so existing construction sites don't churn.
func (h *VehicleReturnHandler) SetTicketRepository(t *repository.TicketRepository) {
	h.ticketRepo = t
}

func NewVehicleReturnHandler(
	repo *repository.VehicleReturnRepository,
	leaseRepo *repository.LeaseRequestRepository,
	carRepo *repository.CarRepository,
	userRepo *repository.UserRepository,
	chatRepo *repository.ChatRepository,
	stripe *stripeService.Service,
	wsHub *ws.Hub,
	notifHandler *NotificationHandler,
	logger *slog.Logger,
) *VehicleReturnHandler {
	return &VehicleReturnHandler{
		repo:         repo,
		leaseRepo:    leaseRepo,
		carRepo:      carRepo,
		userRepo:     userRepo,
		chatRepo:     chatRepo,
		stripe:       stripe,
		wsHub:        wsHub,
		notifHandler: notifHandler,
		logger:       logger,
	}
}

// returnStuckRefundStaleAfter sets how long a refund-pending row may sit
// before the scanner replays the Stripe call. Mirrors the pickup-expiry
// scanner's 2-minute window for parity.
const returnStuckRefundStaleAfter = 2 * time.Minute

// ─── Driver endpoints ───────────────────────────────────────────────────────

// Initiate — POST /api/v1/lease-requests/{id}/vehicle-return
// Driver marks the rental as returned. Snapshots the rental clock + paid
// amount onto the new vehicle_returns row so the refund formula is
// deterministic regardless of when the owner finally confirms.
func (h *VehicleReturnHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	leaseID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid lease request ID"))
		return
	}

	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || models.GetAPIError(err) == models.ErrLeaseRequestNotFound {
			httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
			return
		}
		h.logger.Error("vehicle return: load lease", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if userID != lr.DriverID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrNotLeaseDriver)
		return
	}
	if lr.Status != models.LeaseStatusPaid || lr.PickupConfirmedAt == nil {
		httputil.WriteError(w, http.StatusConflict, models.ErrReturnNotAllowed)
		return
	}

	// If a return already exists, what happens depends on its state:
	//   - active or completed → return it idempotently (driver double-tap).
	//   - CANCELLED → revive it as a fresh submission. Without this branch,
	//     cancelled is lease-fatal: UNIQUE(lease_request_id) means a driver
	//     who undid a mistap — or lost a dispute — could never return the
	//     car again, and the listing stayed 'rented' forever.
	if existing, err := h.repo.GetByLeaseRequestID(r.Context(), leaseID); err == nil && existing != nil {
		if existing.Status != models.VehicleReturnCancelled {
			httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), existing, userID))
			return
		}
		h.reviveCancelledReturn(w, r, lr, existing, userID)
		return
	}

	// Look up the payment so the refund formula has a real paid amount.
	// Missing payment row (shouldn't happen for a paid lease, but defend
	// anyway) → treat as paid_amount_cents=0 and let the formula mark it
	// not_applicable.
	var paidCents int64
	if payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID); err == nil && payment != nil {
		paidCents = payment.Amount
	}

	now := time.Now().UTC()
	calc := models.ComputeReturnRefund(paidCents, lr.Weeks, *lr.PickupConfirmedAt, now)
	usedDays, refundCents := calc.UsedDays, calc.RefundAmountCents

	// Rolling leases preview per CYCLE, not against the week-1 payment
	// (batch 3). Falls back to the legacy numbers pre-bootstrap, where
	// weeks=1 makes the legacy formula exactly the cycle-1 math.
	if lr.BillingMode == models.BillingModeRolling && h.billingRepo != nil {
		p, u, rf, ok, rerr := h.rollingReturnSnapshot(r.Context(), lr, now)
		if rerr != nil {
			h.logger.Error("vehicle return: rolling snapshot", "error", rerr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if ok {
			paidCents, usedDays, refundCents = p, u, rf
		}
	}

	created, err := h.repo.CreateForLease(r.Context(), repository.CreateForLeaseParams{
		LeaseRequestID:    leaseID,
		CarID:             lr.ListingID,
		OwnerID:           lr.OwnerID,
		DriverID:          lr.DriverID,
		PickupConfirmedAt: *lr.PickupConfirmedAt,
		ReturnedAt:        now,
		RentalWeeks:       lr.Weeks,
		PaidAmountCents:   paidCents,
		UsedDays:          usedDays,
		RefundAmountCents: refundCents,
	})
	if err != nil {
		h.logger.Error("vehicle return: create", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), created, userID)
	// Rolling leases: a live return pauses the billing ladder explicitly
	// (review H3: nothing wrote 'return_initiated' before). Cleared on
	// cancel; superseded by vehicle_returned_at on completion.
	if _, herr := h.leaseRepo.HaltRenewals(r.Context(), leaseID, "return_initiated"); herr != nil {
		h.logger.Warn("return initiate: halt renewals", "error", herr, "lease_request_id", leaseID)
	}

	httputil.WriteJSON(w, http.StatusCreated, resp)

	h.broadcast("vehicle_return_initiated", created)
	h.postSystemMessage(r.Context(), created, "driver_initiated", resp)

	// Notify the owner — they need to confirm before the refund moves.
	chatID := resp.ChatID
	leaseRef := created.LeaseRequestID
	driverName := nameOr(resp.DriverName, "The driver")
	carTitle := carTitleOr(resp.CarTitle)
	go h.notifHandler.Notify(created.OwnerID, models.NotificationTypeLeaseRequest,
		"Return requested",
		fmt.Sprintf("%s requested to return %s. Confirm receipt to release the refund.", driverName, carTitle),
		chatID, &leaseRef)
}

// reviveCancelledReturn re-opens a cancelled return as a fresh
// driver_initiated submission with the refund recomputed at the new return
// time. Same side effects as a first Initiate — the owner is notified and
// must confirm again.
func (h *VehicleReturnHandler) reviveCancelledReturn(w http.ResponseWriter, r *http.Request, lr *models.LeaseRequest, existing *models.VehicleReturn, userID uuid.UUID) {
	var paidCents int64
	if payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), existing.LeaseRequestID); err == nil && payment != nil {
		paidCents = payment.Amount
	}
	now := time.Now().UTC()
	calc := models.ComputeReturnRefund(paidCents, lr.Weeks, *lr.PickupConfirmedAt, now)
	usedDays, refundCents := calc.UsedDays, calc.RefundAmountCents

	// Same per-cycle preview as a fresh Initiate (batch 3).
	if lr.BillingMode == models.BillingModeRolling && h.billingRepo != nil {
		p, u, rf, ok, rerr := h.rollingReturnSnapshot(r.Context(), lr, now)
		if rerr != nil {
			h.logger.Error("vehicle return: rolling snapshot (revive)", "error", rerr, "lease_request_id", lr.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if ok {
			paidCents, usedDays, refundCents = p, u, rf
		}
	}

	revived, err := h.repo.ReviveCancelled(r.Context(), existing.ID, userID, now, usedDays, refundCents, paidCents)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("vehicle return: revive cancelled", "error", err, "return_id", existing.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// The revive path pauses rolling billing exactly like a fresh initiate
	// (verify-pass H3: initiate→cancel→revive previously left the ladder
	// live during the second handshake).
	if _, herr := h.leaseRepo.HaltRenewals(r.Context(), revived.LeaseRequestID, "return_initiated"); herr != nil {
		h.logger.Warn("return revive: halt renewals", "error", herr, "lease_request_id", revived.LeaseRequestID)
	}

	resp := h.buildResponse(r.Context(), revived, userID)
	httputil.WriteJSON(w, http.StatusCreated, resp)

	h.broadcast("vehicle_return_initiated", revived)
	h.postSystemMessage(r.Context(), revived, "driver_initiated", resp)

	chatID := resp.ChatID
	leaseRef := revived.LeaseRequestID
	go h.notifHandler.Notify(revived.OwnerID, models.NotificationTypeLeaseRequest,
		"Return requested",
		fmt.Sprintf("%s requested to return %s. Confirm receipt to release the refund.",
			nameOr(resp.DriverName, "The driver"), carTitleOr(resp.CarTitle)),
		chatID, &leaseRef)
}

// Cancel — POST /api/v1/vehicle-returns/{id}/cancel
// Driver-only undo, allowed inside VehicleReturnDriverCancelWindow.
func (h *VehicleReturnHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid return id"))
		return
	}

	// Existence + participation check (404 for non-participants).
	if _, err := h.repo.GetByIDForUser(r.Context(), id, userID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrVehicleReturnNotFound)
		return
	}

	updated, err := h.repo.Cancel(r.Context(), id, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusConflict
			switch apiErr.Code {
			case models.ErrCodeVehicleReturnNotFound:
				status = http.StatusNotFound
			case models.ErrCodeReturnCancelExpired, models.ErrCodeInvalidReturnState:
				status = http.StatusConflict
			}
			httputil.WriteError(w, status, apiErr)
			return
		}
		h.logger.Error("vehicle return: cancel", "error", err, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), updated, userID)

	// Undo the rolling-billing pause the initiate set (claim-scoped: only
	// clears the 'return_initiated' reason, never a delinquency/dispute halt)
	// — then RE-halt if a dispute is live on the lease (the single-slot
	// reason column can only hold one; a dispute opening mid-return would
	// otherwise resume billing here — verify-pass H2).
	if _, herr := h.leaseRepo.ClearRenewalHalt(r.Context(), updated.LeaseRequestID, "return_initiated"); herr != nil {
		h.logger.Warn("return cancel: clear renewal halt", "error", herr, "lease_request_id", updated.LeaseRequestID)
	}
	if h.disputeRepoForBilling != nil {
		if n, derr := h.disputeRepoForBilling.CountOtherOpenForLease(r.Context(), updated.LeaseRequestID, uuid.Nil); derr == nil && n > 0 {
			if _, herr := h.leaseRepo.HaltRenewals(r.Context(), updated.LeaseRequestID, "dispute"); herr != nil {
				h.logger.Warn("return cancel: re-halt for open dispute", "error", herr)
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, resp)

	h.broadcast("vehicle_return_cancelled", updated)
	h.postSystemMessage(r.Context(), updated, "driver_cancelled", resp)

	// Notify the owner so a backgrounded "waiting to confirm return" sheet
	// gets the rug pulled out cleanly. The driver pulled out before the
	// owner acted — no Stripe state changed, so this is purely informational.
	chatID := resp.ChatID
	leaseRef := updated.LeaseRequestID
	driverName := nameOr(resp.DriverName, "The driver")
	carTitle := carTitleOr(resp.CarTitle)
	go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypeLeaseRequest,
		"Return cancelled",
		fmt.Sprintf("%s cancelled the return of %s. The rental is still active.", driverName, carTitle),
		chatID, &leaseRef)
}

// ─── Owner endpoints ────────────────────────────────────────────────────────

// OwnerConfirm — POST /api/v1/vehicle-returns/{id}/owner-confirm
// Owner confirms receipt. Immediately runs the refund pipeline (or the
// zero-refund fast-path) so the row reaches `completed` within the same
// HTTP request whenever Stripe is healthy.
func (h *VehicleReturnHandler) OwnerConfirm(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid return id"))
		return
	}

	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrVehicleReturnNotFound)
		return
	}
	if userID != existing.OwnerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the car owner can confirm a return"))
		return
	}

	// Idempotent: already past driver_initiated → return current state.
	if existing.Status == models.VehicleReturnOwnerConfirmed || existing.Status == models.VehicleReturnCompleted {
		httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), existing, userID))
		return
	}
	// driver_initiated is the normal confirm; disputed is the owner
	// WITHDRAWING their dispute by confirming receipt after all — the
	// self-service exit from a state that otherwise only the admin queue
	// could unfreeze.
	wasDisputed := existing.Status == models.VehicleReturnDisputed
	if existing.Status != models.VehicleReturnDriverInitiated && !wasDisputed {
		httputil.WriteError(w, http.StatusConflict, models.ErrInvalidReturnState)
		return
	}

	confirmed, err := h.repo.OwnerConfirm(r.Context(), id, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("vehicle return: owner confirm", "error", err, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	h.broadcast("vehicle_return_owner_confirmed", confirmed)
	respConfirmed := h.buildResponse(r.Context(), confirmed, userID)
	h.postSystemMessage(r.Context(), confirmed, "owner_confirmed", respConfirmed)

	// A withdrawn dispute closes its support case and tells the driver the
	// freeze is over — the confirm itself already fires the standard
	// notifications further down the refund pipeline.
	if wasDisputed {
		if h.ticketRepo != nil {
			if terr := h.ticketRepo.ResolveForVehicleReturn(r.Context(), confirmed.ID); terr != nil {
				h.logger.Error("vehicle return: resolve dispute ticket on withdraw", "error", terr, "return_id", confirmed.ID)
			}
		}
		chatID := respConfirmed.ChatID
		leaseRef := confirmed.LeaseRequestID
		go h.notifHandler.Notify(confirmed.DriverID, models.NotificationTypeLeaseRequest,
			"Dispute withdrawn",
			fmt.Sprintf("The owner confirmed the return of %s after all — the dispute is closed and your refund is on its way.", carTitleOr(respConfirmed.CarTitle)),
			chatID, &leaseRef)
	}

	// Run the refund pipeline. On success we re-broadcast the now-completed
	// row; on failure the row stays at owner_confirmed and the scanner
	// retries on its next tick.
	finalized := h.issueRefund(r.Context(), confirmed)

	// Use whichever row is the freshest; finalized may be nil if Stripe
	// failed and we didn't transition out of owner_confirmed.
	out := confirmed
	if finalized != nil {
		out = finalized
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), out, userID))
}

// Dispute — POST /api/v1/vehicle-returns/{id}/dispute
func (h *VehicleReturnHandler) Dispute(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid return id"))
		return
	}

	var body models.DisputeVehicleReturnBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	// Rune count, not len(): the bounds are advertised as characters and
	// the composer counts characters — a 300-char Cyrillic reason is 600
	// bytes and must not bounce.
	reason := strings.TrimSpace(body.Reason)
	if n := utf8.RuneCountInString(reason); n < 5 || n > 500 {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrDisputeReasonRequired)
		return
	}

	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrVehicleReturnNotFound)
		return
	}
	if userID != existing.OwnerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the car owner can dispute a return"))
		return
	}
	if existing.Status != models.VehicleReturnDriverInitiated {
		httputil.WriteError(w, http.StatusConflict, models.ErrInvalidReturnState)
		return
	}

	updated, err := h.repo.Dispute(r.Context(), id, userID, reason)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("vehicle return: dispute", "error", err, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Open a REAL support ticket before promising anyone contact — this is
	// what makes "our team will reach out" true (defect 4). Idempotent via
	// the partial unique index on vehicle_return_id; reporter is the
	// disputing owner. Best-effort: a ticket failure must not unwind the
	// dispute itself, but it is logged loudly because the promise below
	// depends on it.
	if h.ticketRepo != nil {
		returnRef := updated.ID
		subject := fmt.Sprintf("Return dispute — %s", carTitleOr(h.buildResponse(r.Context(), updated, userID).CarTitle))
		description := fmt.Sprintf(
			"Owner disputed the driver's vehicle return.\n\nReason: %s\n\nRefund on file: %s (%d of %d days used).\nLease request: %s\nVehicle return: %s",
			reason, formatMoney(updated.RefundAmountCents), updated.UsedDays, updated.RentalWeeks*7,
			updated.LeaseRequestID, updated.ID)
		if created, terr := h.ticketRepo.CreateSystemTicket(r.Context(), updated.OwnerID,
			models.TicketCategoryRenting, subject, description, nil, &returnRef); terr != nil {
			h.logger.Error("vehicle return: dispute ticket create FAILED — the 'team will reach out' copy is now unbacked",
				"error", terr, "return_id", updated.ID)
		} else if created == nil {
			// The live-ticket unique index says one is already open for this
			// return — the promise below is still backed, just not by a new
			// row. Logged so a queue investigation can see it.
			h.logger.Info("vehicle return: dispute ticket already open", "return_id", updated.ID)
		}
	} else {
		h.logger.Error("vehicle return: ticket repo not wired — dispute created no ticket", "return_id", updated.ID)
	}

	resp := h.buildResponse(r.Context(), updated, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.broadcast("vehicle_return_disputed", updated)
	h.postSystemMessage(r.Context(), updated, "disputed", resp)

	chatID := resp.ChatID
	leaseRef := updated.LeaseRequestID
	go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeLeaseRequest,
		"Return disputed",
		"The owner disputed your return. A support case has been opened — our team will review it and follow up within 24 hours.",
		chatID, &leaseRef)
	// The owner acted, but a dispute freezing their refund/car deserves a
	// written trace of what happens next on their side too.
	go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypeLeaseRequest,
		"Dispute received",
		"Your dispute was filed and a support case opened. Our team will review it and follow up within 24 hours. You can withdraw the dispute by confirming the return from the chat.",
		chatID, &leaseRef)
}

// ─── Shared read endpoints ──────────────────────────────────────────────────

// Today — GET /api/v1/vehicle-returns/today
func (h *VehicleReturnHandler) Today(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	returns, err := h.repo.ListActiveForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("vehicle return: today", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	out := make([]models.VehicleReturnResponse, 0, len(returns))
	for i := range returns {
		out = append(out, h.buildResponse(r.Context(), &returns[i], userID))
	}
	httputil.WriteJSON(w, http.StatusOK, models.VehicleReturnsListResponse{VehicleReturns: out})
}

// GetForLease — GET /api/v1/lease-requests/{id}/vehicle-return
//
// Returns the vehicle_return row for this lease (any status — includes
// terminal rows so iOS can render the "Return completed" history after a
// chat refetch). 404 when no return has ever been initiated, which the iOS
// fetchVehicleReturnForLease helper maps to nil → "Start return" CTA.
//
// Auth via the lease (owner or driver) rather than via the return row,
// because the return row may not exist yet and we still want 404 (not 401).
func (h *VehicleReturnHandler) GetForLease(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	leaseID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid lease request id"))
		return
	}

	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil || lr == nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
		return
	}
	if lr.OwnerID != userID && lr.DriverID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only lease participants can view its vehicle return"))
		return
	}

	v, err := h.repo.GetByLeaseRequestID(r.Context(), leaseID)
	if err != nil {
		// ErrVehicleReturnNotFound (or any other lookup error) → 404, so
		// the iOS helper can return nil and the "Start return" CTA stays
		// clean.
		httputil.WriteError(w, http.StatusNotFound, models.ErrVehicleReturnNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), v, userID))
}

// Get — GET /api/v1/vehicle-returns/{id}
func (h *VehicleReturnHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid return id"))
		return
	}
	v, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrVehicleReturnNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), v, userID))
}

// ─── Admin endpoints ────────────────────────────────────────────────────────

// AdminList — GET /api/v1/admin/vehicle-returns?status=&limit=&offset=
func (h *VehicleReturnHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := atoiOr(r.URL.Query().Get("limit"), 50)
	offset := atoiOr(r.URL.Query().Get("offset"), 0)

	rows, err := h.repo.ListByStatus(r.Context(), status, limit, offset)
	if err != nil {
		h.logger.Error("vehicle return: admin list", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	out := make([]models.VehicleReturnResponse, 0, len(rows))
	for i := range rows {
		// Admin viewer — use owner role label by convention so the UI
		// knows which side to mirror counterparty names against.
		out = append(out, h.buildResponse(r.Context(), &rows[i], rows[i].OwnerID))
	}
	httputil.WriteJSON(w, http.StatusOK, models.VehicleReturnsListResponse{VehicleReturns: out})
}

// AdminResolve — POST /api/v1/admin/vehicle-returns/{id}/resolve
// Body: {resolution: "accept"|"reject", note?}.
func (h *VehicleReturnHandler) AdminResolve(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid return id"))
		return
	}
	var body models.ResolveVehicleReturnBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	resolution := strings.ToLower(strings.TrimSpace(body.Resolution))
	if resolution != "accept" && resolution != "reject" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("resolution must be 'accept' or 'reject'"))
		return
	}
	// The note is REQUIRED — a resolution both parties only ever see the
	// outcome of must arrive with its reasoning. Same bounds as the
	// dispute reason it answers.
	note := strings.TrimSpace(body.Note)
	if n := utf8.RuneCountInString(note); n < 5 || n > 500 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("a resolution note is required (5–500 characters) — both parties see the outcome"))
		return
	}

	// Optional refund override, applied BEFORE the accept flips the row —
	// once refund_status goes pending the amount is committed. Bounds are
	// enforced against the row's own paid snapshot in the repo guard.
	if resolution == "accept" && body.DriverRefundCents != nil {
		if *body.DriverRefundCents < 0 {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("driver_refund_cents must be non-negative"))
			return
		}
		// A rolling return refunds per CYCLE out of each week's own charge —
		// a whole-row override would desync the row from the cycle ledger
		// and there is no single charge it could come out of (batch 3).
		// Fails CLOSED on lookup errors, like every money guard.
		if h.billingRepo != nil {
			ret, rerr := h.repo.GetByID(r.Context(), id)
			if rerr != nil && models.GetAPIError(rerr) == models.ErrVehicleReturnNotFound {
				httputil.WriteError(w, http.StatusNotFound, models.ErrVehicleReturnNotFound)
				return
			}
			if rerr != nil || ret == nil {
				h.logger.Error("admin resolve: load return for rolling guard", "error", rerr, "id", id)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
			lrRow, lerr := h.leaseRepo.GetByID(r.Context(), ret.LeaseRequestID)
			if lerr != nil || lrRow == nil {
				h.logger.Error("admin resolve: load lease for rolling guard", "error", lerr, "id", id)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
			if lrRow.BillingMode == models.BillingModeRolling {
				httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ROLLING_LEASE",
					"this rental bills weekly — resolve without a refund override; refunds settle per cycle, and individual weeks can be waived from the billing-cycle tools"))
				return
			}
		}
		// M4: same one-charge-one-direction rule as the settle close arm —
		// an owner payout already on the ledger means a driver refund now
		// pays out more than was collected. Fails CLOSED on any lookup
		// error (review R2).
		if *body.DriverRefundCents > 0 && h.payoutH != nil {
			ret, rerr := h.repo.GetByID(r.Context(), id)
			if rerr != nil || ret == nil {
				h.logger.Error("admin resolve: load return for ledger guard", "error", rerr, "id", id)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
			existing, lerr := h.payoutH.LedgerRow(r.Context(), ret.LeaseRequestID)
			if lerr != nil {
				h.logger.Error("admin resolve: refund ledger guard read", "error", lerr, "id", id)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
			if existing != nil {
				httputil.WriteError(w, http.StatusConflict, models.NewAPIError("REFUND_AFTER_PAYOUT",
					fmt.Sprintf("the payout ledger already has a row for this rent (status %q) — resolve with driver_refund_cents 0 and record any manual repayment in the note", existing.Status)))
				return
			}
		}
		if _, uerr := h.repo.UpdateRefundAmount(r.Context(), id, *body.DriverRefundCents); uerr != nil {
			if apiErr := models.GetAPIError(uerr); apiErr != nil {
				httputil.WriteError(w, http.StatusConflict, apiErr)
				return
			}
			h.logger.Error("vehicle return: refund override", "error", uerr, "id", id)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	}

	resolved, err := h.repo.ResolveDispute(r.Context(), id, resolution, note)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("vehicle return: admin resolve", "error", err, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// The dispute's support ticket is finished work now, whichever way it
	// went. Best-effort.
	if h.ticketRepo != nil {
		if terr := h.ticketRepo.ResolveForVehicleReturn(r.Context(), resolved.ID); terr != nil {
			h.logger.Error("vehicle return: resolve dispute ticket", "error", terr, "return_id", resolved.ID)
		}
	}

	// On accept, run the same refund pipeline so the row moves on to
	// 'completed' (or stays at owner_confirmed for scanner retry).
	out := resolved
	if resolution == "accept" {
		if finalized := h.issueRefund(r.Context(), resolved); finalized != nil {
			out = finalized
		}
		h.broadcast("vehicle_return_owner_confirmed", out)
	} else {
		// Reject = the rental continues, so un-freeze rolling billing the
		// same way a driver cancel does (batch 3: this arm previously left
		// 'return_initiated' parked forever — a halt with no exit). Both
		// halt writes are billing_mode-gated, so fixed-term is a no-op.
		if _, herr := h.leaseRepo.ClearRenewalHalt(r.Context(), resolved.LeaseRequestID, "return_initiated"); herr != nil {
			h.logger.Warn("admin resolve: clear renewal halt", "error", herr, "lease_request_id", resolved.LeaseRequestID)
		}
		if h.disputeRepoForBilling != nil {
			if n, derr := h.disputeRepoForBilling.CountOtherOpenForLease(r.Context(), resolved.LeaseRequestID, uuid.Nil); derr == nil && n > 0 {
				if _, herr := h.leaseRepo.HaltRenewals(r.Context(), resolved.LeaseRequestID, "dispute"); herr != nil {
					h.logger.Warn("admin resolve: re-halt for open dispute", "error", herr)
				}
			}
		}
		h.broadcast("vehicle_return_cancelled", out)
	}

	resp := h.buildResponse(r.Context(), out, out.OwnerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	kind := "admin_accept_dispute"
	if resolution == "reject" {
		kind = "admin_reject_dispute"
	}
	h.postSystemMessage(r.Context(), out, kind, resp)

	// Both parties are told the outcome WITH the note — a dispute is the
	// one transition where silence reads as the house taking sides.
	chatID := resp.ChatID
	leaseRef := out.LeaseRequestID
	carTitle := carTitleOr(resp.CarTitle)
	// The subject line adapts: a resolution of an owner-ignored (never
	// disputed) return isn't a "dispute resolved". And money is only
	// promised when money will actually move — the zero-refund path runs
	// FinalizeNoRefund and no cents ever reach Stripe.
	title := "Dispute resolved"
	if out.DisputedAt == nil {
		title = "Return resolved by support"
	}
	if resolution == "accept" {
		refundLine := "No refund applies — the full rental period was used."
		if out.RefundAmountCents > 0 {
			refundLine = "Your refund is being processed."
		}
		go h.notifHandler.Notify(out.DriverID, models.NotificationTypeLeaseRequest,
			title+" — return confirmed",
			fmt.Sprintf("Support reviewed %s and confirmed your return. %s %s", carTitle, note, refundLine),
			chatID, &leaseRef)
		go h.notifHandler.Notify(out.OwnerID, models.NotificationTypeLeaseRequest,
			title+" — return confirmed",
			fmt.Sprintf("Support confirmed the return of %s on your behalf. %s", carTitle, note),
			chatID, &leaseRef)
	} else {
		go h.notifHandler.Notify(out.DriverID, models.NotificationTypeLeaseRequest,
			title+" — return not accepted",
			fmt.Sprintf("Support reviewed %s and did not accept this return. %s The rental continues — submit the return again when you hand the car back.", carTitle, note),
			chatID, &leaseRef)
		go h.notifHandler.Notify(out.OwnerID, models.NotificationTypeLeaseRequest,
			title+" — return not accepted",
			fmt.Sprintf("The return of %s was not accepted and the rental continues. %s", carTitle, note),
			chatID, &leaseRef)
	}
}

// AdminSettleRent — POST /api/v1/admin/rents/{id}/settle
// Body: {resolution: "close"|"payout_only"|"withhold", driver_refund_cents?, note}.
//
// The settlement path for rentals that never reach a clean return
// (amendment ⑥): every terminal state must resolve to either "owner paid"
// or "explicitly not paid, with the reason recorded".
//
//   - close: an admin-authored return completion. Creates (or revives) the
//     return row with the admin-chosen driver refund — defaulting to the
//     standard formula — and drives it through the SAME resolve→refund→
//     payout pipeline as a normal return: driver refunded, car released,
//     tickets resolved, owner paid, one ledger row. Nothing bespoke.
//   - payout_only: pays the owner their share of the full amount while the
//     rental stays open — the overdue case, where the term is exhausted
//     (the refund formula yields $0 past term) but the car isn't back.
//   - withhold: deliberate non-payment, note required — fraud, forfeiture,
//     or a policy decision. Refuses if the money already moved.
func (h *VehicleReturnHandler) AdminSettleRent(w http.ResponseWriter, r *http.Request) {
	if h.payoutH == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("PAYOUTS_DISABLED", "payout engine not configured"))
		return
	}
	leaseID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid rent id"))
		return
	}
	var body struct {
		Resolution        string `json:"resolution"`
		DriverRefundCents *int64 `json:"driver_refund_cents"`
		Note              string `json:"note"`
	}
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	resolution := strings.ToLower(strings.TrimSpace(body.Resolution))
	if resolution != "close" && resolution != "payout_only" && resolution != "withhold" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("resolution must be 'close', 'payout_only' or 'withhold'"))
		return
	}
	// Settlement moves (or deliberately holds) money — the reasoning is
	// non-optional, same bounds as dispute resolution.
	note := strings.TrimSpace(body.Note)
	if n := utf8.RuneCountInString(note); n < 5 || n > 500 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("a settlement note is required (5–500 characters)"))
		return
	}

	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil || lr == nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
		return
	}
	// Rolling leases settle per CYCLE — this endpoint's whole-rent model
	// (one payment, one payout row, one refund) mismodels weekly money in
	// every arm: close would refund out of the week-1 charge, payout_only
	// would double-pay weeks the cycle ledger already accrued, withhold
	// would hold a number no single charge matches (batch 3).
	if lr.BillingMode == models.BillingModeRolling {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ROLLING_LEASE",
			"this rental bills weekly — settle individual weeks from the billing-cycle tools; rent settlement applies to fixed-term rentals only"))
		return
	}
	// M4: settlement moves (or holds) money the platform must actually
	// hold. payments.amount is written at intent CREATION — before any
	// charge — so amount alone proves nothing; the charge must have
	// SUCCEEDED. This gate is shared by all three resolutions.
	payment, perr := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID)
	if perr != nil {
		h.logger.Error("admin settle: load payment", "error", perr, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if payment == nil || payment.Amount <= 0 {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_PAYMENT", "this rent has no recorded payment — nothing to settle"))
		return
	}
	if payment.Status != models.PaymentStatusSucceeded {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYMENT_NOT_SETTLED",
			fmt.Sprintf("the charge for this rent is %q, not succeeded — there is no money to settle", payment.Status)))
		return
	}
	paidCents := payment.Amount

	switch resolution {
	case "withhold":
		row, werr := h.payoutH.AdminWithhold(r.Context(), leaseID, lr.OwnerID, paidCents, note)
		if werr != nil {
			if apiErr := models.GetAPIError(werr); apiErr != nil {
				httputil.WriteError(w, http.StatusConflict, apiErr)
				return
			}
			h.logger.Error("admin settle: withhold", "error", werr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"payout": row})
		return

	case "payout_only":
		existing, gerr := h.payoutH.LedgerRow(r.Context(), leaseID)
		if gerr != nil {
			// Money guards fail CLOSED (review R2): an unreadable ledger is
			// not an absent ledger.
			h.logger.Error("admin settle: ledger read", "error", gerr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if existing != nil {
			switch existing.Status {
			case models.PayoutPaid:
				httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYOUT_STATE", "owner already paid for this rent"))
				return
			case models.PayoutWithheld:
				// payout_only on a withheld row is the deliberate reversal.
				row, rerr := h.payoutH.AdminReviveWithheld(r.Context(), existing.ID, note)
				if rerr != nil {
					if apiErr := models.GetAPIError(rerr); apiErr != nil {
						httputil.WriteError(w, http.StatusConflict, apiErr)
						return
					}
					h.logger.Error("admin settle: revive withheld", "error", rerr, "lease_request_id", leaseID)
					httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
					return
				}
				httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"payout": row})
				return
			}
		}
		// M4: payout_only pays the owner their share of the FULL paid
		// amount, so the rental must be a real, finished one — a lease
		// that never reached paid has no money here, and a mid-term
		// payout double-spends the moment an early return refunds the
		// driver from the same charge.
		if lr.Status != models.LeaseStatusPaid {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("SETTLE_NOT_ALLOWED",
				fmt.Sprintf("payout_only applies to a paid rental — this rent is %q", lr.Status)))
			return
		}
		nowPO := time.Now().UTC()
		rentalOver := lr.VehicleReturnedAt != nil ||
			(lr.RentalEndsAt != nil && !nowPO.Before(*lr.RentalEndsAt))
		if !rentalOver {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("SETTLE_NOT_ALLOWED",
				"payout_only applies after the rental term ends or the car is returned — paying out mid-term double-spends if the driver later returns early"))
			return
		}
		h.payoutH.SettleRentalPayout(r.Context(), leaseID, lr.OwnerID, paidCents, models.PayoutSourceAdminSettlement, &note)
		row, gerr := h.payoutH.LedgerRow(r.Context(), leaseID)
		if gerr != nil || row == nil {
			h.logger.Error("admin settle: payout_only ledger read-back", "error", gerr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"payout": row})
		return
	}

	// ── close ──
	if lr.Status != models.LeaseStatusPaid || lr.PickupConfirmedAt == nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("SETTLE_NOT_ALLOWED", "close applies to an active (paid, picked-up) rental — this rent isn't one"))
		return
	}
	now := time.Now().UTC()
	calc := models.ComputeReturnRefund(paidCents, lr.Weeks, *lr.PickupConfirmedAt, now)
	refundCents := calc.RefundAmountCents
	if body.DriverRefundCents != nil {
		refundCents = *body.DriverRefundCents
		if refundCents < 0 || refundCents > paidCents {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("driver_refund_cents must be between 0 and the paid amount"))
			return
		}
	}
	// M4: one charge cannot fund both an owner payout and a driver refund.
	// If the ledger already has a row for this lease (payout_only ran, or
	// the owner's share is queued/withheld), a refund now would pay out
	// more than was collected — the ON CONFLICT DO NOTHING in the ledger
	// would silently keep the full-amount row afterwards.
	if refundCents > 0 {
		existing, lerr := h.payoutH.LedgerRow(r.Context(), leaseID)
		if lerr != nil {
			// Fail CLOSED (review R2): refusing a refund on a transient
			// read error is recoverable; issuing one against an unseen
			// payout row is the double-spend itself.
			h.logger.Error("admin settle: refund ledger guard read", "error", lerr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if existing != nil {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("REFUND_AFTER_PAYOUT",
				fmt.Sprintf("the payout ledger already has a row for this rent (status %q) — a driver refund now would pay out more than was collected; close with driver_refund_cents 0 and record any manual repayment in the note", existing.Status)))
			return
		}
	}

	target, gerr := h.repo.GetByLeaseRequestID(r.Context(), leaseID)
	if gerr != nil {
		// Not-found is the NORMAL close case — the rental never got a
		// return row; we author one below.
		if models.GetAPIError(gerr) == models.ErrVehicleReturnNotFound {
			target = nil
		} else {
			h.logger.Error("admin settle: load return", "error", gerr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	}
	switch {
	case target == nil:
		target, err = h.repo.CreateForLease(r.Context(), repository.CreateForLeaseParams{
			LeaseRequestID:    leaseID,
			CarID:             lr.ListingID,
			OwnerID:           lr.OwnerID,
			DriverID:          lr.DriverID,
			PickupConfirmedAt: *lr.PickupConfirmedAt,
			ReturnedAt:        now,
			RentalWeeks:       lr.Weeks,
			PaidAmountCents:   paidCents,
			UsedDays:          calc.UsedDays,
			RefundAmountCents: refundCents,
		})
		if err != nil {
			h.logger.Error("admin settle: create return", "error", err, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	case target.Status == models.VehicleReturnCancelled:
		target, err = h.repo.ReviveCancelled(r.Context(), target.ID, lr.DriverID, now, calc.UsedDays, refundCents, paidCents)
		if err != nil {
			if apiErr := models.GetAPIError(err); apiErr != nil {
				httputil.WriteError(w, http.StatusConflict, apiErr)
				return
			}
			h.logger.Error("admin settle: revive return", "error", err, "return_id", target.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	case target.Status == models.VehicleReturnDriverInitiated || target.Status == models.VehicleReturnDisputed:
		// An open return already exists — resolve THAT one. The admin's
		// driver_refund_cents was previously DROPPED here, which turned
		// "close this did-not-return dispute with $0" into a silent
		// force-accept that refunded the full initiation-day snapshot
		// (client fix batch, item 1). Honor the override; without one, the
		// row's snapshot still applies.
		if body.DriverRefundCents != nil {
			if _, uerr := h.repo.UpdateRefundAmount(r.Context(), target.ID, refundCents); uerr != nil {
				if apiErr := models.GetAPIError(uerr); apiErr != nil {
					httputil.WriteError(w, http.StatusConflict, apiErr)
					return
				}
				h.logger.Error("admin settle: refund override", "error", uerr, "return_id", target.ID)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
		}
	case target.Status == models.VehicleReturnOwnerConfirmed &&
		target.RefundStatus != nil && *target.RefundStatus == models.VehicleReturnRefundUnrecoverable:
		// The product exit for a PERMANENTLY failed refund (item 3): no
		// Stripe money can move on a dead PaymentIntent, so the only
		// closable driver refund is $0 — any manual arrangement lives in
		// the required note. The row is already owner_confirmed, so no
		// dispute resolution applies; force the amount and finalize.
		if body.DriverRefundCents != nil && *body.DriverRefundCents != 0 {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("REFUND_UNRECOVERABLE",
				"this refund can never be processed by Stripe — close with driver_refund_cents 0 and record any manual repayment in the note"))
			return
		}
		if _, uerr := h.repo.UpdateRefundAmount(r.Context(), target.ID, 0); uerr != nil {
			if apiErr := models.GetAPIError(uerr); apiErr != nil {
				httputil.WriteError(w, http.StatusConflict, apiErr)
				return
			}
			h.logger.Error("admin settle: zero unrecoverable refund", "error", uerr, "return_id", target.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	default:
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("SETTLE_NOT_ALLOWED", "this rent's return is already settling — the refund scanner will complete it"))
		return
	}

	// owner_confirmed rows (the unrecoverable arm) skip dispute resolution —
	// they were already confirmed; only the finalize step remains.
	resolved := target
	if target.Status != models.VehicleReturnOwnerConfirmed {
		var rerr error
		resolved, rerr = h.repo.ResolveDispute(r.Context(), target.ID, "accept", note)
		if rerr != nil {
			if apiErr := models.GetAPIError(rerr); apiErr != nil {
				httputil.WriteError(w, http.StatusConflict, apiErr)
				return
			}
			h.logger.Error("admin settle: resolve", "error", rerr, "return_id", target.ID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	} else if reloaded, gerr2 := h.repo.GetByID(r.Context(), target.ID); gerr2 == nil {
		resolved = reloaded // pick up the zeroed refund before finalizing
	}
	if h.ticketRepo != nil {
		if terr := h.ticketRepo.ResolveForVehicleReturn(r.Context(), resolved.ID); terr != nil {
			h.logger.Error("admin settle: resolve tickets", "error", terr, "return_id", resolved.ID)
		}
	}
	out := resolved
	if finalized := h.issueRefund(r.Context(), resolved); finalized != nil {
		out = finalized
	}
	h.broadcast("vehicle_return_owner_confirmed", out)

	resp := h.buildResponse(r.Context(), out, out.OwnerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.postSystemMessage(r.Context(), out, "admin_accept_dispute", resp)

	chatID := resp.ChatID
	leaseRef := out.LeaseRequestID
	carTitle := carTitleOr(resp.CarTitle)
	refundLine := "No refund applies."
	if out.RefundAmountCents > 0 {
		refundLine = fmt.Sprintf("A refund of %s is being processed.", formatMoney(out.RefundAmountCents))
	}
	go h.notifHandler.Notify(out.DriverID, models.NotificationTypeLeaseRequest,
		"Rental closed by support",
		fmt.Sprintf("Support closed your rental of %s. %s %s", carTitle, note, refundLine),
		chatID, &leaseRef)
	go h.notifHandler.Notify(out.OwnerID, models.NotificationTypeLeaseRequest,
		"Rental closed by support",
		fmt.Sprintf("Support closed the rental of %s. %s The car is back on the market.", carTitle, note),
		chatID, &leaseRef)
}

// ─── Refund pipeline ────────────────────────────────────────────────────────

// issueRefund runs the Stripe call for a return that has reached
// owner_confirmed. Returns the latest version of the row when the
// transition was applied; nil when the row stayed at owner_confirmed
// because Stripe rejected the call.
//
// Idempotency: the stable key "vehicle-return-refund-{returnID}" lets
// the stuck-refund scanner safely replay this after a process crash or
// transient 5xx — Stripe dedupes and returns the same Refund object.
func (h *VehicleReturnHandler) issueRefund(ctx context.Context, v *models.VehicleReturn) *models.VehicleReturn {
	// Rolling leases settle per CYCLE and must branch BEFORE the zero-
	// refund fast-path — that path calls the legacy settleOwnerPayout,
	// which would double-count money the cycle ledger already owns. The
	// lease read fails CLOSED: guessing the billing mode wrong refunds out
	// of the wrong charge, and a transient DB error just costs one sweep
	// tick (the scanner replays).
	if h.billingRepo != nil {
		lr, lerr := h.leaseRepo.GetByID(ctx, v.LeaseRequestID)
		if lerr != nil || lr == nil {
			// Fail closed but QUIETLY (batch-3 review: a zero-refund
			// fixed-term return must not grow a "Refund delayed" push out
			// of a transient read blip). The row stays owner_confirmed and
			// the stuck-refund scanner replays on its next tick — the same
			// convergence the pre-batch-3 code had.
			h.logger.Error("vehicle return: lease lookup for billing mode failed",
				"error", lerr, "id", v.ID, "lease_request_id", v.LeaseRequestID)
			return nil
		}
		if lr.BillingMode == models.BillingModeRolling {
			return h.issueRollingRefund(ctx, v, lr)
		}
	}

	// Zero-refund fast-path: $0 lease or sub-cent computed refund. Skip
	// Stripe entirely and flip straight to completed with
	// refund_status='not_applicable'.
	if v.RefundAmountCents <= 0 || v.PaidAmountCents <= 0 {
		completed, err := h.repo.FinalizeNoRefund(ctx, v.ID)
		if err != nil {
			h.logger.Error("vehicle return: finalize no-refund", "error", err, "id", v.ID)
			return nil
		}
		h.broadcast("vehicle_return_completed", completed)
		resp := h.buildResponseCtx(ctx, completed, completed.OwnerID)
		h.postSystemMessage(ctx, completed, "completed_no_refund", resp)
		h.resolveLinkedTickets(ctx, completed)
		h.settleOwnerPayout(ctx, completed)
		return completed
	}

	payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, v.LeaseRequestID)
	if err != nil {
		// Transient (DB) — the sweep retries.
		h.logger.Error("vehicle return: payment lookup failed",
			"error", err, "id", v.ID, "lease_request_id", v.LeaseRequestID)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, "payment lookup failed")
		h.notifyRefundDelay(ctx, v)
		return nil
	}
	if payment == nil || payment.PaymentIntentID == nil {
		// PERMANENT (item 3): no PaymentIntent was ever recorded for this
		// lease — no automated refund can ever succeed. Stop retrying and
		// surface the admin decision instead of hammering forever.
		h.markRefundUnrecoverable(ctx, v, "no payment intent recorded for this lease")
		return nil
	}

	idemKey := fmt.Sprintf("vehicle-return-refund-%s", v.ID.String())
	refund, err := h.stripe.CreateRefund(*payment.PaymentIntentID, idemKey, "requested_by_customer", v.RefundAmountCents)
	if err != nil {
		if strings.Contains(err.Error(), "resource_missing") {
			// PERMANENT (item 3): the PaymentIntent does not exist at
			// Stripe (demonstrated live in the Aug 28 cleanup — a
			// test-era PI from a different account). Retrying forever
			// changes nothing; park as unrecoverable and surface it.
			h.markRefundUnrecoverable(ctx, v, "stripe: payment intent does not exist (resource_missing)")
			return nil
		}
		h.logger.Error("vehicle return: stripe refund failed",
			"error", err, "id", v.ID, "intent_id", *payment.PaymentIntentID, "amount_cents", v.RefundAmountCents)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, err.Error())
		h.notifyRefundDelay(ctx, v)
		return nil
	}

	// Stripe reports "succeeded" or "pending" both as acceptable terminal
	// API responses; "failed"/"canceled" need a retry.
	switch refund.Status {
	case "succeeded", "pending":
	default:
		reason := fmt.Sprintf("stripe refund status=%s", refund.Status)
		h.logger.Error("vehicle return: stripe refund unhealthy status",
			"id", v.ID, "stripe_status", refund.Status)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, reason)
		h.notifyRefundDelay(ctx, v)
		return nil
	}

	completed, err := h.repo.FinalizeRefund(ctx, v.ID, refund.ID)
	if err != nil {
		h.logger.Error("vehicle return: finalize refund", "error", err, "id", v.ID, "refund_id", refund.ID)
		return nil
	}

	h.broadcast("vehicle_return_completed", completed)
	resp := h.buildResponseCtx(ctx, completed, completed.OwnerID)
	h.postSystemMessage(ctx, completed, "completed_with_refund", resp)
	h.resolveLinkedTickets(ctx, completed)
	h.settleOwnerPayout(ctx, completed)

	chatID := resp.ChatID
	leaseRef := completed.LeaseRequestID
	body := fmt.Sprintf("Refund of %s issued for your return of %s.",
		formatMoney(completed.RefundAmountCents), carTitleOr(resp.CarTitle))
	go h.notifHandler.Notify(completed.DriverID, models.NotificationTypePayment,
		"Refund issued", body, chatID, &leaseRef)
	go h.notifHandler.Notify(completed.OwnerID, models.NotificationTypeLeaseRequest,
		"Return complete",
		fmt.Sprintf("%s confirmed. The car is back on the market.", carTitleOr(resp.CarTitle)),
		chatID, &leaseRef)

	return completed
}

// rollingReturnSnapshot computes the initiate-time preview for a rolling
// lease: which weeks are refundable per the cycle ledger, at `now`.
// ok=false ⇒ nothing is cycled yet (pre-bootstrap week 1) and the legacy
// numbers stand — for week 1 they ARE the cycle math, since rolling
// leases are pinned to weeks=1.
func (h *VehicleReturnHandler) rollingReturnSnapshot(ctx context.Context, lr *models.LeaseRequest, now time.Time) (paid int64, usedDays int, refund int64, ok bool, err error) {
	settle, serr := computeRollingSettlement(ctx, h.billingRepo, lr, now)
	if serr != nil {
		return 0, 0, 0, false, serr
	}
	if settle.CurrentCycle == nil {
		return 0, 0, 0, false, nil
	}
	cc := settle.CurrentCycle
	refund = settle.CurrentRefundCents
	if cc.RefundID != nil || cc.Status == models.CyclePaid {
		paid += cc.AmountCents
	}
	if oc := settle.OvershootCycle; oc != nil {
		switch {
		case oc.RefundID != nil:
			paid += oc.AmountCents
			refund += oc.RefundedCents
		case oc.Status == models.CyclePaid:
			paid += oc.AmountCents
			refund += oc.AmountCents
		}
	}
	return paid, settle.CurrentUsedDays, refund, true, nil
}

// issueRollingRefund settles a rolling return per CYCLE instead of against
// the week-1 lease payment: pro-rata refund out of the final week's own
// charge, full refund of an unentered overshoot week, the final week's
// owner share rewritten in the cycle ledger (accruing → pending), and an
// unpaid final week waived — those uncollected days are the owner's
// absorption under the rolling terms. The legacy settleOwnerPayout never
// runs here; cycle rows own rolling money end to end.
//
// Crash discipline mirrors the legacy path: stable per-cycle idempotency
// keys ("cycle-final-refund-<id>" / "cycle-overshoot-refund-<id>") let the
// stuck-refund scanner replay any window — Stripe dedupes, the claimed-
// once cycle UPDATEs no-op, and the ledger rewrite is status-gated. Every
// DB failure between a Stripe success and its ledger record fails CLOSED
// (MarkRefundFailed → replay) rather than finishing with drifted books.
func (h *VehicleReturnHandler) issueRollingRefund(ctx context.Context, v *models.VehicleReturn, lr *models.LeaseRequest) *models.VehicleReturn {
	// Stamp the factual return BEFORE reading the ledger (batch-3 review
	// HIGH): from this write on, any in-flight cycle charge that lands is
	// refused by AdvanceOnCyclePaid's occupancy guard and auto-refunded —
	// closing the window where a mid-settlement success could advance a
	// finished rental and strand a full week outside every reconciler.
	// Fail closed: without the stamp the window is open, so defer.
	if serr := h.leaseRepo.StampVehicleReturned(ctx, lr.ID, v.ReturnedAt); serr != nil {
		h.logger.Error("rolling return: stamp vehicle_returned_at", "error", serr, "id", v.ID)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: return stamp failed")
		return nil
	}
	settle, err := computeRollingSettlement(ctx, h.billingRepo, lr, v.ReturnedAt)
	if err != nil {
		h.logger.Error("rolling return: settlement compute", "error", err, "id", v.ID)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling settlement compute failed")
		h.notifyRefundDelay(ctx, v)
		return nil
	}
	if settle.CurrentCycle == nil {
		// Week 1 isn't cycled yet. The sweep's bootstrap phase mints it
		// within one tick (the lease is paid + picked up — exactly its
		// preconditions), so defer WITHOUT the delay notice: this is a
		// sub-minute wait, not a manual-processing episode.
		h.logger.Info("rolling return: deferring until cycle 1 is minted", "id", v.ID, "lease_request_id", lr.ID)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: cycle 1 not minted yet")
		return nil
	}

	var totalRefunded, paidBase, arrearsOwedCents int64
	var arrearsCycle *models.BillingCycle
	primaryRefundID := ""

	// Slot-claiming ledger write for refunded overshoot weeks (batch-3
	// review — see writeFinalLedger below for why a plain accruing→voided
	// UPDATE is not race-safe against the webhook's accrual insert).
	voidOvershootLedger := func(oc *models.BillingCycle) error {
		cycleRef, ps, pe := oc.ID, oc.PeriodStart, oc.PeriodEnd
		return h.billingPayoutRepo.FinalizeCyclePayoutRow(ctx, &models.OwnerPayout{
			LeaseRequestID: lr.ID,
			OwnerID:        lr.OwnerID,
			FeeBPS:         h.billingFeeBPS,
			Currency:       "USD",
			BillingCycleID: &cycleRef,
			PeriodStart:    &ps,
			PeriodEnd:      &pe,
		}, "voided", fmt.Sprintf("voided: unconsumed week refunded on return %s", v.ID))
	}

	// ── 1) Overshoot: a week the T−24h charge bought that was never entered.
	if oc := settle.OvershootCycle; oc != nil {
		switch {
		case oc.RefundID != nil:
			// A prior pass (or the post-return reconciler, or the webhook's
			// occupancy-ended branch) already returned this money.
			paidBase += oc.AmountCents
			totalRefunded += oc.RefundedCents
			primaryRefundID = *oc.RefundID
			if verr := voidOvershootLedger(oc); verr != nil {
				h.logger.Error("rolling return: void overshoot accrual (replay)", "error", verr, "cycle_id", oc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: overshoot ledger void failed")
				return nil
			}
		case oc.Status == models.CyclePaid:
			intent := ""
			if oc.StripePaymentIntentID != nil {
				intent = *oc.StripePaymentIntentID
			}
			if intent == "" {
				h.logger.Error("rolling return: overshoot cycle has no intent", "cycle_id", oc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: overshoot cycle missing payment intent")
				h.notifyRefundDelay(ctx, v)
				return nil
			}
			refund, rerr := h.stripe.CreateRefund(intent,
				"cycle-overshoot-refund-"+oc.ID.String(), "requested_by_customer", oc.AmountCents)
			if rerr != nil {
				h.logger.Error("rolling return: overshoot refund failed", "error", rerr, "cycle_id", oc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "overshoot refund: "+rerr.Error())
				h.notifyRefundDelay(ctx, v)
				return nil
			}
			if refund.Status != "succeeded" && refund.Status != "pending" {
				h.logger.Error("rolling return: overshoot refund unhealthy", "cycle_id", oc.ID, "stripe_status", refund.Status)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "overshoot refund status="+refund.Status)
				h.notifyRefundDelay(ctx, v)
				return nil
			}
			if _, cerr := h.billingRepo.RefundCycleClaim(ctx, oc.ID, refund.ID, oc.AmountCents); cerr != nil {
				h.logger.Error("rolling return: overshoot refund claim", "error", cerr, "cycle_id", oc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: overshoot refund claim failed")
				return nil
			}
			if verr := voidOvershootLedger(oc); verr != nil {
				h.logger.Error("rolling return: void overshoot accrual", "error", verr, "cycle_id", oc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: overshoot ledger void failed")
				return nil
			}
			paidBase += oc.AmountCents
			totalRefunded += oc.AmountCents
			primaryRefundID = refund.ID
		case oc.Status == models.CycleScheduled && oc.AttemptCount == 0 &&
			(oc.StripePaymentIntentID == nil || *oc.StripePaymentIntentID == ""):
			// Proven-safe waive: never attempted, no intent to neutralize.
			if _, werr := h.billingRepo.WaiveUnpaidCycle(ctx, oc.ID,
				"waived: vehicle returned before the charge was attempted"); werr != nil {
				h.logger.Error("rolling return: waive scheduled overshoot", "error", werr, "cycle_id", oc.ID)
			}
		default:
			// In flight (charging/retrying/needs_action) — hands off, per the
			// proven-neutralize rule. If it lands after completion stamps
			// vehicle_returned_at, the sweep's post-return reconciler (or the
			// webhook's occupancy-ended branch) refunds it in full.
			h.logger.Info("rolling return: overshoot cycle in flight, left to reconciler",
				"cycle_id", oc.ID, "status", oc.Status)
		}
	}

	// ── 2) The final (current) week.
	cc := settle.CurrentCycle
	// The ledger writes use the claim-the-slot upsert, NOT a plain
	// accruing→X UPDATE (batch-3 review): the webhook can still be between
	// its advance and its CreateCycleAccruing when we run — the update
	// would no-op on the missing row and the late insert would zombie as a
	// full-week 'accruing' nothing can move. The upsert takes the
	// (billing_cycle_id) slot first; the webhook's ON CONFLICT DO NOTHING
	// then yields. A dispute-'withheld' row stays with the dispute
	// machinery either way.
	writeFinalLedger := func(kept int64) bool {
		status, note := "pending", ""
		var fee, ownerShare int64
		if kept > 0 {
			fee, ownerShare = models.ComputePayoutSplit(kept, h.billingFeeBPS)
		} else {
			status, kept = "voided", 0
			note = fmt.Sprintf("voided: final week fully refunded on return %s", v.ID)
		}
		cycleRef, ps, pe := cc.ID, cc.PeriodStart, cc.PeriodEnd
		if uErr := h.billingPayoutRepo.FinalizeCyclePayoutRow(ctx, &models.OwnerPayout{
			LeaseRequestID:   lr.ID,
			OwnerID:          lr.OwnerID,
			GrossKeptCents:   kept,
			FeeBPS:           h.billingFeeBPS,
			FeeCents:         fee,
			OwnerAmountCents: ownerShare,
			Currency:         "USD",
			BillingCycleID:   &cycleRef,
			PeriodStart:      &ps,
			PeriodEnd:        &pe,
		}, status, note); uErr != nil {
			h.logger.Error("rolling return: finalize final-cycle payout", "error", uErr, "cycle_id", cc.ID)
			_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: final-cycle ledger write failed")
			return false
		}
		return true
	}
	switch {
	case cc.RefundID != nil:
		// A prior pass already moved the driver money; finish the ledger.
		paidBase += cc.AmountCents
		totalRefunded += cc.RefundedCents
		primaryRefundID = *cc.RefundID
		if !writeFinalLedger(cc.AmountCents - cc.RefundedCents) {
			return nil
		}
	case cc.Status == models.CyclePaid:
		paidBase += cc.AmountCents
		ccRefund := settle.CurrentRefundCents
		if ccRefund > 0 {
			intent := ""
			if cc.StripePaymentIntentID != nil {
				intent = *cc.StripePaymentIntentID
			}
			if intent == "" {
				h.logger.Error("rolling return: final cycle has no intent", "cycle_id", cc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: final cycle missing payment intent")
				h.notifyRefundDelay(ctx, v)
				return nil
			}
			refund, rerr := h.stripe.CreateRefund(intent,
				"cycle-final-refund-"+cc.ID.String(), "requested_by_customer", ccRefund)
			if rerr != nil {
				h.logger.Error("rolling return: final-cycle refund failed", "error", rerr, "cycle_id", cc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "final-cycle refund: "+rerr.Error())
				h.notifyRefundDelay(ctx, v)
				return nil
			}
			if refund.Status != "succeeded" && refund.Status != "pending" {
				h.logger.Error("rolling return: final-cycle refund unhealthy", "cycle_id", cc.ID, "stripe_status", refund.Status)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "final-cycle refund status="+refund.Status)
				h.notifyRefundDelay(ctx, v)
				return nil
			}
			var cerr error
			if ccRefund >= cc.AmountCents {
				_, cerr = h.billingRepo.RefundCycleClaim(ctx, cc.ID, refund.ID, ccRefund)
			} else {
				_, cerr = h.billingRepo.PartialRefundCycleClaim(ctx, cc.ID, refund.ID, ccRefund)
			}
			if cerr != nil {
				h.logger.Error("rolling return: final-cycle refund claim", "error", cerr, "cycle_id", cc.ID)
				_ = h.repo.MarkRefundFailed(ctx, v.ID, "rolling: final-cycle refund claim failed")
				return nil
			}
			totalRefunded += ccRefund
			primaryRefundID = refund.ID // the final week's refund is the row's primary
		}
		if !writeFinalLedger(cc.AmountCents - ccRefund) {
			return nil
		}
	case cc.Status == models.CycleFailedFinal ||
		(cc.Status == models.CycleScheduled && cc.AttemptCount == 0):
		// Returned during a week that was never collected. Return is NEVER
		// blocked on debt (design §7): the cycle becomes arrears_due with
		// the amount cut to the days actually used — collection is
		// on-session only from here (driver Pay-now / support), admin
		// waive is the write-off, and if it's never collected the owner
		// absorbs those days per the rolling terms.
		owed := cc.AmountCents - models.ComputeReturnRefund(cc.AmountCents, 1, cc.PeriodStart, v.ReturnedAt).RefundAmountCents
		if owed > 0 {
			if settled, aerr := h.billingRepo.SettleArrearsProRata(ctx, cc.ID, owed); aerr != nil {
				h.logger.Error("rolling return: settle arrears", "error", aerr, "cycle_id", cc.ID)
			} else if settled {
				// The ticket is opened LATER, after resolveLinkedTickets —
				// see the completion block below (rehearsal line 7).
				arrearsOwedCents = owed
				arrearsCycle = cc
				// The debt is the DRIVER's, not just this cycle's: it has to
				// sum with anything they owe elsewhere and outlive the lease.
				openDriverDebtLedger(ctx, h.logger, h.debtRepo, h.userRepo, h.billingRepo, lr, cc.ID, owed)
				h.logger.Info("rolling return: final week settled as arrears",
					"cycle_id", cc.ID, "owed_cents", owed)
			}
		} else {
			if _, werr := h.billingRepo.WaiveUnpaidCycle(ctx, cc.ID,
				"waived: vehicle returned before this week began"); werr != nil {
				h.logger.Error("rolling return: waive zero-owed final week", "error", werr, "cycle_id", cc.ID)
			}
		}
	case cc.Status == models.CycleArrearsDue:
		// Replay after a crash between the arrears flip and finalize —
		// the debt is already recorded; nothing further here.
	default:
		// charging / retrying / needs_action in flight — hands off. A late
		// success lands after vehicle_returned_at and the reconciler
		// refunds it; needs_action resolves at its 72h TTL, which is
		// return-aware and settles pro-rata arrears instead of marking a
		// finished rental delinquent.
		h.logger.Info("rolling return: final cycle in flight, left to reconciler",
			"cycle_id", cc.ID, "status", cc.Status)
	}

	// ── 3) True the row's snapshot to what actually moved, then finalize.
	if paidBase != v.PaidAmountCents || totalRefunded != v.RefundAmountCents || settle.CurrentUsedDays != v.UsedDays {
		if serr := h.repo.SyncRollingSnapshot(ctx, v.ID, paidBase, totalRefunded, settle.CurrentUsedDays); serr != nil {
			h.logger.Error("rolling return: sync snapshot", "error", serr, "id", v.ID)
		}
	}
	var completed *models.VehicleReturn
	var ferr error
	if totalRefunded > 0 && primaryRefundID != "" {
		completed, ferr = h.repo.FinalizeRefund(ctx, v.ID, primaryRefundID)
	} else {
		completed, ferr = h.repo.FinalizeNoRefund(ctx, v.ID)
	}
	if ferr != nil {
		h.logger.Error("rolling return: finalize", "error", ferr, "id", v.ID)
		return nil
	}

	h.broadcast("vehicle_return_completed", completed)
	resp := h.buildResponseCtx(ctx, completed, completed.OwnerID)
	kind := "completed_no_refund"
	if totalRefunded > 0 {
		kind = "completed_with_refund"
	}
	h.postSystemMessage(ctx, completed, kind, resp)
	h.resolveLinkedTickets(ctx, completed)
	// The collection ticket opens AFTER that cleanup: resolveLinkedTickets
	// closes every lease-linked open ticket, so a ticket opened earlier in
	// this function was resolved the instant it was created — a real debt
	// with no actor chasing it (found by the batch-5 rehearsal, line 7).
	// The debt outlives the return, so its ticket must too.
	if arrearsOwedCents > 0 && arrearsCycle != nil && h.ticketRepo != nil {
		leaseRef := lr.ID
		desc := fmt.Sprintf(
			"A rolling rental ended with %s still owed for the used days of its final week (cycle %d, %s – %s).\n\nCollection is ON-SESSION ONLY (never charge the saved card silently). The driver can settle it in the app with Pay now; otherwise arrange payment with them, or write the debt off via Admin → Rents → Billing cycles → Waive — uncollected days are borne by the owner per the rolling terms.\n\nLease request: %s",
			formatMoney(arrearsOwedCents), arrearsCycle.CycleNumber,
			arrearsCycle.PeriodStart.Format("Jan 2"), arrearsCycle.PeriodEnd.Format("Jan 2, 2006"), lr.ID)
		if _, terr := h.ticketRepo.CreateSystemTicket(ctx, lr.DriverID, models.TicketCategoryPayments,
			"Uncollected rental week — collection needed", desc, &leaseRef, nil); terr != nil {
			h.logger.Error("rolling return: arrears ticket failed", "error", terr, "cycle_id", arrearsCycle.ID)
		}
	}
	// Deliberately NO settleOwnerPayout: the cycle ledger rows written
	// above (rewrite / void / weekly accruals) are the rolling money.

	chatID := resp.ChatID
	leaseRef := completed.LeaseRequestID
	if totalRefunded > 0 {
		go h.notifHandler.Notify(completed.DriverID, models.NotificationTypePayment,
			"Refund issued",
			fmt.Sprintf("Refund of %s issued for your return of %s — the unused days of your rental week.",
				formatMoney(totalRefunded), carTitleOr(resp.CarTitle)),
			chatID, &leaseRef)
	}
	if arrearsOwedCents > 0 {
		// The debt survives the return (design §7) — say so honestly, once,
		// with the collection path that exists today.
		go h.notifHandler.Notify(completed.DriverID, models.NotificationTypePayment,
			"Balance due on your rental",
			fmt.Sprintf("Your return of %s is complete, but %s for the days used in your final week couldn't be collected. Our support team will follow up to arrange payment.",
				carTitleOr(resp.CarTitle), formatMoney(arrearsOwedCents)),
			chatID, &leaseRef)
	}
	go h.notifHandler.Notify(completed.OwnerID, models.NotificationTypeLeaseRequest,
		"Return complete",
		fmt.Sprintf("%s confirmed. The car is back on the market — your share of the final week transfers with the next payout run.", carTitleOr(resp.CarTitle)),
		chatID, &leaseRef)

	return completed
}

// markRefundUnrecoverable parks a refund in the PERMANENT failure state
// (item 3): exactly one caller wins the claimed-once flip, opens ONE
// support ticket, and tells both parties honestly. The retry sweep
// excludes 'unrecoverable', and the admin settle endpoint is the product
// exit (close with a forced $0 — no Stripe money can move on a dead PI).
func (h *VehicleReturnHandler) markRefundUnrecoverable(ctx context.Context, v *models.VehicleReturn, reason string) {
	claimed, err := h.repo.MarkRefundUnrecoverable(ctx, v.ID, reason)
	if err != nil {
		h.logger.Error("vehicle return: mark unrecoverable", "error", err, "id", v.ID)
		return
	}
	if claimed == nil {
		return // another worker won, or the state already moved
	}
	h.logger.Error("vehicle return: refund UNRECOVERABLE — admin exit required",
		"id", v.ID, "lease_request_id", v.LeaseRequestID, "reason", reason)

	leaseRef := claimed.LeaseRequestID
	if h.ticketRepo != nil {
		subject := "Refund cannot be processed automatically"
		desc := fmt.Sprintf(
			"A driver refund of %s is permanently unprocessable: %s.\n\nThe return is parked at owner_confirmed/unrecoverable. Resolve via Admin → Rents → Settle → close (driver refund is forced to $0 — arrange any manual repayment off-platform and record it in the note).\n\nLease request: %s",
			formatMoney(claimed.RefundAmountCents), reason, claimed.LeaseRequestID)
		if _, terr := h.ticketRepo.CreateSystemTicket(ctx, claimed.DriverID, models.TicketCategoryPayments, subject, desc, &leaseRef, &claimed.ID); terr != nil {
			h.logger.Error("vehicle return: unrecoverable ticket failed", "error", terr, "return_id", claimed.ID)
		}
	}

	resp := h.buildResponseCtx(ctx, claimed, claimed.OwnerID)
	chatID := resp.ChatID
	go h.notifHandler.Notify(claimed.DriverID, models.NotificationTypePayment,
		"We're on your refund",
		fmt.Sprintf("Your refund for %s couldn't be processed automatically — our team has it and will resolve it directly with you.", carTitleOr(resp.CarTitle)),
		chatID, &leaseRef)
	go h.notifHandler.Notify(claimed.OwnerID, models.NotificationTypeLeaseRequest,
		"Return with support",
		fmt.Sprintf("The return of %s hit a payment-processing issue on our side. Support is resolving it — nothing is needed from you.", carTitleOr(resp.CarTitle)),
		chatID, &leaseRef)
}

// settleOwnerPayout hands what the driver's payment left after the refund
// to the payout engine. kept = paid − refund; both completion paths land
// here, so every clean return produces exactly one ledger row (the engine
// is idempotent per lease).
func (h *VehicleReturnHandler) settleOwnerPayout(ctx context.Context, completed *models.VehicleReturn) {
	if h.payoutH == nil {
		return
	}
	refund := completed.RefundAmountCents
	if refund < 0 {
		refund = 0
	}
	// A return an admin drove to completion is admin settlement — the
	// ledger records who decided and why (the required resolution note).
	source := models.PayoutSourceReturnCompleted
	var note *string
	if completed.DisputeResolvedBy != nil && *completed.DisputeResolvedBy == "admin" {
		source = models.PayoutSourceAdminSettlement
		note = completed.ResolutionNote
	}
	h.payoutH.SettleRentalPayout(ctx, completed.LeaseRequestID, completed.OwnerID,
		completed.PaidAmountCents-refund, source, note)
}

// notifyRefundDelay tells the driver their refund is being processed
// manually. Called from every MarkRefundFailed site so the user isn't
// left in the dark when Stripe rejects the call — the stuck-refund
// scanner will retry, but the driver shouldn't have to refresh the app
// to find out. Idempotent at the user level: a follow-up retry will not
// produce duplicate banners because each call writes its own row but
// iOS collapses them via apns-collapse-id=payment:{leaseID}.
func (h *VehicleReturnHandler) notifyRefundDelay(ctx context.Context, v *models.VehicleReturn) {
	if v == nil || h.notifHandler == nil {
		return
	}
	// Claimed-once (item 2): the stuck-refund scanner re-fails every ~2
	// minutes, and each failure used to push a fresh "Refund delayed" at
	// the driver. One episode, one notice.
	claimed, err := h.repo.ClaimRefundDelayNotice(ctx, v.ID)
	if err != nil {
		h.logger.Error("vehicle return: claim refund-delay notice", "error", err, "id", v.ID)
		return
	}
	if !claimed {
		return
	}
	carTitle := "your rental"
	if h.carRepo != nil {
		if car, err := h.carRepo.GetByID(ctx, v.CarID); err == nil {
			carTitle = car.Title
		}
	}
	// Best-effort lookup of the chat so iOS can deep-link to it on tap.
	// We pull the lease row (cheap, indexed) and use its denormalized
	// chat_id rather than re-querying chats.
	var chatID *uuid.UUID
	if h.leaseRepo != nil {
		if lr, err := h.leaseRepo.GetByID(ctx, v.LeaseRequestID); err == nil && lr != nil {
			c := lr.ChatID
			chatID = &c
		}
	}
	leaseRef := v.LeaseRequestID
	go h.notifHandler.Notify(v.DriverID, models.NotificationTypePayment,
		"Refund delayed",
		fmt.Sprintf("Your refund for %s is being processed manually — we'll update you within 24h.", carTitle),
		chatID, &leaseRef)
}

// ─── Stuck-refund scanner ───────────────────────────────────────────────────

// StartReturnRefundScanner polls for vehicle_returns rows that owner-
// confirmed (or already moved to completed via the zero-refund path)
// but whose Stripe refund never landed. Replays the call with the same
// stable idempotency key so Stripe dedupes server-side.
//
// Cancelled via the supplied ctx on shutdown; failures inside one
// iteration never abort the loop.
func (h *VehicleReturnHandler) StartReturnRefundScanner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	h.logger.Info("vehicle return refund scanner started", "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("vehicle return refund scanner stopped")
			return
		case <-ticker.C:
			h.runStuckRefundSweep(ctx)
		}
	}
}

func (h *VehicleReturnHandler) runStuckRefundSweep(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-returnStuckRefundStaleAfter)
	stuck, err := h.repo.ListStuckRefunds(ctx, cutoff, 50)
	if err != nil {
		h.logger.Error("vehicle return: list stuck refunds", "error", err)
	} else if len(stuck) > 0 {
		h.logger.Info("vehicle return: stuck refund candidates", "count", len(stuck))
		for i := range stuck {
			h.issueRefund(ctx, &stuck[i])
		}
	}

	// Zero-refund rows parked at owner_confirmed were invisible to the
	// sweep above (its predicate requires refund_amount_cents > 0) — a
	// transient FinalizeNoRefund failure made them a hard dead end. Same
	// pipeline completes them; nothing to send to Stripe.
	zeroStuck, err := h.repo.ListStuckZeroRefunds(ctx, cutoff, 50)
	if err != nil {
		h.logger.Error("vehicle return: list stuck zero refunds", "error", err)
	} else if len(zeroStuck) > 0 {
		h.logger.Info("vehicle return: stuck zero-refund candidates", "count", len(zeroStuck))
		for i := range zeroStuck {
			h.issueRefund(ctx, &zeroStuck[i])
		}
	}

	// Attention sweep: a driver_initiated return the owner has ignored past
	// the reminder threshold. The owner has no deadline to confirm or
	// dispute, and their silence leaves the driver's refund in limbo — the
	// claim (owner_reminder_sent_at under IS NULL) guarantees exactly one
	// nudge per return.
	reminders, err := h.repo.ClaimOwnerReminders(ctx, time.Now().UTC().Add(-models.ReturnOwnerReminderAfter), 50)
	if err != nil {
		h.logger.Error("vehicle return: claim owner reminders", "error", err)
		return
	}
	for i := range reminders {
		v := &reminders[i]
		resp := h.buildResponseCtx(ctx, v, v.OwnerID)
		chatID := resp.ChatID
		leaseRef := v.LeaseRequestID
		go h.notifHandler.Notify(v.OwnerID, models.NotificationTypeLeaseRequest,
			"Return waiting on you",
			fmt.Sprintf("%s requested to return %s two days ago and is still waiting. Confirm receipt to release their refund, or dispute it if something is wrong.",
				nameOr(resp.DriverName, "The driver"), carTitleOr(resp.CarTitle)),
			chatID, &leaseRef)
		h.logger.Info("vehicle return: owner reminder sent", "return_id", v.ID, "owner_id", v.OwnerID)
	}

	// Escalation: the reminder didn't move the owner. Ticket-first with the
	// live-ticket unique index as the dedupe — the creator instance owns
	// the notifications; later sweeps get (nil, nil) and stay silent. The
	// admin resolves it with the same accept/reject lever as a dispute
	// (ResolveDispute accepts driver_initiated).
	if h.ticketRepo == nil {
		return
	}
	stale, err := h.repo.ListStaleInitiatedReturns(ctx, time.Now().UTC().Add(-models.ReturnEscalateAfter), 50)
	if err != nil {
		h.logger.Error("vehicle return: list stale initiated returns", "error", err)
		return
	}
	for i := range stale {
		v := &stale[i]
		resp := h.buildResponseCtx(ctx, v, v.OwnerID)
		returnRef := v.ID
		subject := fmt.Sprintf("Unconfirmed return — %s", carTitleOr(resp.CarTitle))
		description := fmt.Sprintf(
			"Driver marked the car returned %d+ days ago; the owner has not confirmed or disputed despite a reminder. The driver's refund of %s is frozen.\n\nDriver: %s\nOwner: %s\nLease request: %s\nVehicle return: %s\n\nResolve via the Rents page (accept = confirm return + refund, reject = cancel the return).",
			int(models.ReturnEscalateAfter.Hours()/24), formatMoney(v.RefundAmountCents),
			nameOr(resp.DriverName, "unknown"), nameOr(resp.OwnerName, "unknown"),
			v.LeaseRequestID, v.ID)
		created, terr := h.ticketRepo.CreateSystemTicket(ctx, v.OwnerID,
			models.TicketCategoryRenting, subject, description, nil, &returnRef)
		if terr != nil {
			h.logger.Error("vehicle return: stale-return escalation ticket failed", "error", terr, "return_id", v.ID)
			continue
		}
		if created != nil {
			chatID := resp.ChatID
			leaseRef := v.LeaseRequestID
			go h.notifHandler.Notify(v.DriverID, models.NotificationTypeLeaseRequest,
				"Your return is with support",
				fmt.Sprintf("The owner hasn't confirmed your return of %s, so a support case has been opened. Our team will resolve it and release your refund.", carTitleOr(resp.CarTitle)),
				chatID, &leaseRef)
			go h.notifHandler.Notify(v.OwnerID, models.NotificationTypeLeaseRequest,
				"Unconfirmed return escalated",
				fmt.Sprintf("The return of %s has waited %d days without your confirmation — a support case is now open and our team will step in.", carTitleOr(resp.CarTitle), int(models.ReturnEscalateAfter.Hours()/24)),
				chatID, &leaseRef)
			h.logger.Info("vehicle return: stale return escalated", "return_id", v.ID)
		}
	}
}

// resolveLinkedTickets closes every open support ticket about a return that
// just COMPLETED — the dispute ticket (vehicle_return_id) and the term
// scanner's overdue-rental escalation (lease_request_id). This is what makes
// "returning the car closes it out" true. Best-effort, both idempotent.
func (h *VehicleReturnHandler) resolveLinkedTickets(ctx context.Context, v *models.VehicleReturn) {
	if h.ticketRepo == nil {
		return
	}
	if err := h.ticketRepo.ResolveForVehicleReturn(ctx, v.ID); err != nil {
		h.logger.Error("vehicle return: resolve return-linked ticket", "error", err, "return_id", v.ID)
	}
	if err := h.ticketRepo.ResolveForLeaseRequest(ctx, v.LeaseRequestID); err != nil {
		h.logger.Error("vehicle return: resolve lease-linked ticket", "error", err, "lease_request_id", v.LeaseRequestID)
	}
}

// ─── WebSocket broadcasts ───────────────────────────────────────────────────

func (h *VehicleReturnHandler) broadcast(eventType string, v *models.VehicleReturn) {
	payload := map[string]any{
		"id":                  v.ID,
		"lease_request_id":    v.LeaseRequestID,
		"status":              v.Status,
		"refund_amount_cents": v.RefundAmountCents,
	}
	if v.RefundID != nil {
		payload["refund_id"] = *v.RefundID
	}
	if v.RefundStatus != nil {
		payload["refund_status"] = *v.RefundStatus
	}
	if v.DisputeReason != nil {
		payload["dispute_reason"] = *v.DisputeReason
	}
	h.wsHub.Broadcast(&ws.Event{
		Type:          eventType,
		Payload:       payload,
		TargetUserIDs: []uuid.UUID{v.OwnerID, v.DriverID},
	})

	// Piggy-back the existing event types so iOS publishers refetch
	// without a separate listener. lease_request_updated fires on both
	// terminal statuses (either way the lease card must re-render), but
	// car_updated {is_reserved:false} fires ONLY on completed — a
	// cancelled return (driver undo, admin reject) leaves the car rented
	// and reserved, and broadcasting otherwise told every open client the
	// car was free when the DB said it was not.
	if v.Status == models.VehicleReturnCompleted || v.Status == models.VehicleReturnCancelled {
		h.wsHub.Broadcast(&ws.Event{
			Type: "lease_request_updated",
			Payload: map[string]any{
				"id":                  v.LeaseRequestID,
				"vehicle_returned_at": v.CompletedAt,
			},
			TargetUserIDs: []uuid.UUID{v.OwnerID, v.DriverID},
		})
	}
	if v.Status == models.VehicleReturnCompleted {
		h.wsHub.Broadcast(&ws.Event{
			Type: "car_updated",
			Payload: map[string]any{
				"id":          v.CarID,
				"is_reserved": false,
			},
			TargetUserIDs: []uuid.UUID{v.OwnerID, v.DriverID},
		})
	}
}

// ─── Chat system messages ───────────────────────────────────────────────────

// postSystemMessage drops a gray "kind=system" message in the lease's
// chat. Modeled on the inline writes the ChatRepository already does for
// request_created/responded (see chat_repository.go lines 452 & 552).
// sender_id is the actor when known, otherwise the owner so the row
// satisfies the FK and lands in everyone's view.
func (h *VehicleReturnHandler) postSystemMessage(ctx context.Context, v *models.VehicleReturn, kind string, resp models.VehicleReturnResponse) {
	if resp.ChatID == nil {
		return
	}
	driverName := nameOr(resp.DriverName, "The driver")
	ownerName := nameOr(resp.OwnerName, "The owner")
	money := formatMoney(v.RefundAmountCents)

	var body string
	var senderID uuid.UUID
	switch kind {
	case "driver_initiated":
		body = fmt.Sprintf("%s requested to return the car. %s, please confirm receipt.", driverName, ownerName)
		senderID = v.DriverID
	case "driver_cancelled":
		body = fmt.Sprintf("%s cancelled the return request.", driverName)
		senderID = v.DriverID
	case "owner_confirmed":
		if v.RefundAmountCents > 0 {
			body = fmt.Sprintf("%s confirmed return. A refund of %s for %d unused day(s) will be issued.", ownerName, money, max1(int(v.RentalWeeks*7-v.UsedDays)))
		} else {
			body = fmt.Sprintf("%s confirmed return. No refund is due — full rental period used.", ownerName)
		}
		senderID = v.OwnerID
	case "disputed":
		reason := ""
		if v.DisputeReason != nil {
			reason = *v.DisputeReason
		}
		body = fmt.Sprintf("%s disputed the return: \"%s\". Our team will reach out within 24 hours.", ownerName, reason)
		senderID = v.OwnerID
	case "completed_with_refund":
		body = fmt.Sprintf("Refund of %s issued. Receipt sent to your email.", money)
		senderID = v.OwnerID
	case "completed_no_refund":
		body = "Return complete. No refund issued — full rental period used."
		senderID = v.OwnerID
	case "admin_accept_dispute":
		body = fmt.Sprintf("Support resolved the dispute and confirmed the return on %s's behalf.", ownerName)
		senderID = v.OwnerID
	case "admin_reject_dispute":
		body = fmt.Sprintf("Support resolved the dispute in %s's favor. The return has been cancelled.", ownerName)
		senderID = v.OwnerID
	default:
		return
	}

	if err := h.chatRepo.PostSystemMessage(ctx, *resp.ChatID, senderID, body); err != nil {
		h.logger.Warn("vehicle return: post system message failed",
			"error", err, "chat_id", *resp.ChatID, "kind", kind)
	}
}

// ─── Response builders ──────────────────────────────────────────────────────

func (h *VehicleReturnHandler) buildResponse(ctx context.Context, v *models.VehicleReturn, viewerID uuid.UUID) models.VehicleReturnResponse {
	return h.buildResponseCtx(ctx, v, viewerID)
}

func (h *VehicleReturnHandler) buildResponseCtx(ctx context.Context, v *models.VehicleReturn, viewerID uuid.UUID) models.VehicleReturnResponse {
	resp := models.VehicleReturnResponse{
		ID:                v.ID,
		LeaseRequestID:    v.LeaseRequestID,
		CarID:             v.CarID,
		OwnerID:           v.OwnerID,
		DriverID:          v.DriverID,
		Status:            v.Status,
		DriverInitiatedAt: models.RFC3339Time(v.DriverInitiatedAt),
		OwnerConfirmedAt:  models.NewRFC3339TimePtr(v.OwnerConfirmedAt),
		DisputedAt:        models.NewRFC3339TimePtr(v.DisputedAt),
		CompletedAt:       models.NewRFC3339TimePtr(v.CompletedAt),
		CancelledAt:       models.NewRFC3339TimePtr(v.CancelledAt),
		PickupConfirmedAt: models.RFC3339Time(v.PickupConfirmedAt),
		ReturnedAt:        models.RFC3339Time(v.ReturnedAt),
		RentalWeeks:       v.RentalWeeks,
		PaidAmountCents:   v.PaidAmountCents,
		UsedDays:          v.UsedDays,
		RefundAmountCents: v.RefundAmountCents,
		RefundStatus:      v.RefundStatus,
		RefundID:          v.RefundID,
		RefundedAt:        models.NewRFC3339TimePtr(v.RefundedAt),
		DisputeReason:     v.DisputeReason,
		CreatedAt:         models.RFC3339Time(v.CreatedAt),
		UpdatedAt:         models.RFC3339Time(v.UpdatedAt),
	}
	if cancelExp := v.CancelWindowExpiresAt(); !cancelExp.IsZero() {
		t := models.RFC3339Time(cancelExp)
		resp.CancelWindowExpiresAt = &t
	}
	if owner, err := h.userRepo.GetByID(ctx, v.OwnerID); err == nil {
		resp.OwnerName = owner.FullName()
	}
	if driver, err := h.userRepo.GetByID(ctx, v.DriverID); err == nil {
		resp.DriverName = driver.FullName()
	}
	if car, err := h.carRepo.GetByID(ctx, v.CarID); err == nil {
		resp.CarTitle = car.Title
	}
	if lr, err := h.leaseRepo.GetByID(ctx, v.LeaseRequestID); err == nil && lr != nil {
		chatID := lr.ChatID
		resp.ChatID = &chatID
	}
	if viewerID == v.OwnerID {
		resp.ViewerRole = "owner"
		resp.CounterpartyName = resp.DriverName
	} else {
		resp.ViewerRole = "driver"
		resp.CounterpartyName = resp.OwnerName
	}
	return resp
}

// ─── Misc helpers ───────────────────────────────────────────────────────────

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return fallback
		}
		v = v*10 + int(ch-'0')
		if v > 10000 {
			return 10000
		}
	}
	return v
}

func formatMoney(cents int64) string {
	if cents < 0 {
		cents = 0
	}
	dollars := cents / 100
	rem := cents % 100
	return fmt.Sprintf("$%d.%02d", dollars, rem)
}

func max1(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
