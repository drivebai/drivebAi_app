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
	logger       *slog.Logger
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

	created, err := h.repo.CreateForLease(r.Context(), repository.CreateForLeaseParams{
		LeaseRequestID:    leaseID,
		CarID:             lr.ListingID,
		OwnerID:           lr.OwnerID,
		DriverID:          lr.DriverID,
		PickupConfirmedAt: *lr.PickupConfirmedAt,
		ReturnedAt:        now,
		RentalWeeks:       lr.Weeks,
		PaidAmountCents:   paidCents,
		UsedDays:          calc.UsedDays,
		RefundAmountCents: calc.RefundAmountCents,
	})
	if err != nil {
		h.logger.Error("vehicle return: create", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), created, userID)
	httputil.WriteJSON(w, http.StatusCreated, resp)

	h.broadcast("vehicle_return_initiated", created)
	h.postSystemMessage(r.Context(), created, "driver_initiated", resp)

	// Notify the owner — they need to confirm before the refund moves.
	chatID := resp.ChatID
	leaseRef := created.LeaseRequestID
	driverName := nameOr(resp.DriverName, "The driver")
	carTitle := carTitleOr(resp.CarTitle)
	go h.notifHandler.Notify(created.OwnerID, models.NotificationTypeLeaseRequest,
		"Driver returned the car",
		fmt.Sprintf("%s marked %s as returned. Confirm receipt to release the refund.", driverName, carTitle),
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

	revived, err := h.repo.ReviveCancelled(r.Context(), existing.ID, userID, now, calc.UsedDays, calc.RefundAmountCents, paidCents)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("vehicle return: revive cancelled", "error", err, "return_id", existing.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), revived, userID)
	httputil.WriteJSON(w, http.StatusCreated, resp)

	h.broadcast("vehicle_return_initiated", revived)
	h.postSystemMessage(r.Context(), revived, "driver_initiated", resp)

	chatID := resp.ChatID
	leaseRef := revived.LeaseRequestID
	go h.notifHandler.Notify(revived.OwnerID, models.NotificationTypeLeaseRequest,
		"Driver returned the car",
		fmt.Sprintf("%s marked %s as returned. Confirm receipt to release the refund.",
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
		return completed
	}

	payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, v.LeaseRequestID)
	if err != nil || payment == nil || payment.PaymentIntentID == nil {
		h.logger.Error("vehicle return: missing payment intent",
			"error", err, "id", v.ID, "lease_request_id", v.LeaseRequestID)
		_ = h.repo.MarkRefundFailed(ctx, v.ID, "payment intent unavailable")
		h.notifyRefundDelay(ctx, v)
		return nil
	}

	idemKey := fmt.Sprintf("vehicle-return-refund-%s", v.ID.String())
	refund, err := h.stripe.CreateRefund(*payment.PaymentIntentID, idemKey, "requested_by_customer", v.RefundAmountCents)
	if err != nil {
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
			fmt.Sprintf("%s marked %s as returned two days ago and is still waiting. Confirm receipt to release their refund, or dispute it if something is wrong.",
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
		body = fmt.Sprintf("%s marked the car as returned. %s, please confirm receipt.", driverName, ownerName)
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
