package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

type LeaseRequestHandler struct {
	leaseRepo       *repository.LeaseRequestRepository
	carRepo         *repository.CarRepository
	carDocRepo      *repository.CarDocumentRepository
	userRepo        *repository.UserRepository
	chatRepo        *repository.ChatRepository
	docRepo         *repository.DocumentRepository
	sharedDocsRepo  *repository.SharedDocumentRepository
	keyHandoverRepo *repository.KeyHandoverRepository
	stripe          *stripeService.Service
	wsHub           *ws.Hub
	notifHandler    *NotificationHandler
	urlSigner       *PrivateURLSigner
	logger          *slog.Logger
	// purchaseHandler is optional. When wired, the shared Stripe webhook
	// dispatches PIs whose metadata carries `kind=purchase` to the
	// purchase-side state machine instead of the lease side.
	purchaseHandler *PurchaseRequestHandler
	// pickupDeadline is the grace window after payment_intent.succeeded in
	// which the driver must confirm pickup. Past this point a background
	// scanner will refund the payment and unreserve the car.
	pickupDeadline time.Duration
	// ticketRepo is optional; wired via SetTicketRepository so the term
	// scanner's sustained-overdue phase can escalate to a real support
	// ticket instead of shouting into logs.
	ticketRepo *repository.TicketRepository
	// ownerTermsRepo gates an owner's acceptance of a ROLLING lease request
	// on their recorded acceptance of the owner package. Optional (nil = no
	// gate); wired via SetOwnerTermsRepository.
	ownerTermsRepo *repository.OwnerTermsRepository
	// Dispute/refund webhook collaborators (batch 1, audit M2) — wired via
	// SetDisputeDependencies / SetReturnRepositoryForDisputes.
	disputeRepo           *repository.ChargeDisputeRepository
	payoutRepo            *repository.PayoutRepository
	returnRepoForDisputes *repository.VehicleReturnRepository
	// Rolling-billing engine (batch 2) — wired via SetBillingDependencies.
	billingRepo    *repository.BillingRepository
	billingFeeBPS  int
	rollingEnabled bool
	// recurringOnly refuses fixed-term CREATION for everyone (RECURRING_ONLY).
	recurringOnly bool
	// monthlyEnabled admits the monthly interval for NEW leases (MONTHLY_RENTALS_ENABLED).
	monthlyEnabled bool
	// rollingAllowlist confines weekly rentals to a named pilot while the
	// flag is on. Empty = open to everyone (see config.RollingAllowlistUserIDs).
	rollingAllowlist map[uuid.UUID]struct{}
	// rollingClosed: the allowlist was configured but unusable. Fail closed.
	rollingClosed bool
	// Driver debt ledger — wired via SetDebtDependencies. Optional: when
	// absent the engine behaves exactly as before, which keeps every
	// existing test constructor working untouched.
	debtRepo    *repository.DriverDebtRepository
	debtEnforce bool
}

// SetDebtDependencies wires the driver-level debt ledger and the switch that
// enforces it. Enforcement is what makes the app's existing promise — "new
// bookings are paused until this is resolved" — true; it was a false
// statement until this shipped.
func (h *LeaseRequestHandler) SetDebtDependencies(d *repository.DriverDebtRepository, enforce bool) {
	h.debtRepo = d
	h.debtEnforce = enforce
}

// openDriverDebt records an uncollected week as a debt against the DRIVER.
func (h *LeaseRequestHandler) openDriverDebt(ctx context.Context, lr *models.LeaseRequest, cycleID uuid.UUID, owedCents int64) {
	openDriverDebtLedger(ctx, h.logger, h.debtRepo, h.userRepo, h.billingRepo, lr, cycleID, owedCents)
}

// openDriverDebtLedger records an uncollected week as a debt against the
// DRIVER, not just the cycle, so it sums with anything they owe elsewhere and
// outlives the lease. Shared by the billing engine and the return flow.
//
// Claimed-once inside the repository, so the sweeps that call it may retry
// freely. Never fatal: failing to write the ledger row must not stop the
// settlement that produced it — the money question is already decided by then.
func openDriverDebtLedger(
	ctx context.Context,
	logger *slog.Logger,
	debtRepo *repository.DriverDebtRepository,
	userRepo *repository.UserRepository,
	billingRepo *repository.BillingRepository,
	lr *models.LeaseRequest,
	cycleID uuid.UUID,
	owedCents int64,
) {
	if debtRepo == nil || lr == nil || owedCents <= 0 {
		return
	}
	// Identifier snapshot: SoftDeleteUser rewrites the email, NULLs the phone
	// and replaces the name, so reading them later from users returns nothing
	// usable. A debt that outlives the account needs its own copy.
	var snap models.DriverDebtSnapshot
	if userRepo != nil {
		if u, err := userRepo.GetByID(ctx, lr.DriverID); err == nil && u != nil {
			snap.Email = u.Email
			if u.Phone != nil {
				snap.Phone = *u.Phone
			}
			snap.Name = strings.TrimSpace(u.FirstName + " " + u.LastName)
		}
	}
	if billingRepo != nil {
		if c, cerr := billingRepo.GetActiveConsent(ctx, lr.ID); cerr == nil && c != nil {
			if c.CardFingerprint != nil {
				snap.CardFingerprint = *c.CardFingerprint
			}
			if c.CardLast4 != nil {
				snap.CardLast4 = *c.CardLast4
			}
		}
	}
	currency := lr.Currency
	if currency == "" {
		currency = "USD"
	}
	debt, created, err := debtRepo.OpenForCycle(ctx, lr.DriverID, lr.ID, cycleID, owedCents, currency, snap)
	if err != nil {
		logger.Error("driver debt: open", "error", err, "cycle_id", cycleID, "lease_request_id", lr.ID)
		return
	}
	if created {
		logger.Info("driver debt opened", "debt_id", debt.ID, "driver_id", lr.DriverID,
			"cycle_id", cycleID, "amount_cents", owedCents)
	}
}

// SetTicketRepository wires the support-ticket repo for the rental-term
// scanner's escalation phase. Setter, per the house pattern.
func (h *LeaseRequestHandler) SetTicketRepository(t *repository.TicketRepository) {
	h.ticketRepo = t
}

// SetPurchaseHandler wires the purchase-side handler so the shared Stripe
// webhook can route by PI metadata. Setter (not ctor arg) to avoid
// cascading changes to existing test constructors.
func (h *LeaseRequestHandler) SetPurchaseHandler(p *PurchaseRequestHandler) {
	h.purchaseHandler = p
}

func NewLeaseRequestHandler(
	leaseRepo *repository.LeaseRequestRepository,
	carRepo *repository.CarRepository,
	carDocRepo *repository.CarDocumentRepository,
	userRepo *repository.UserRepository,
	chatRepo *repository.ChatRepository,
	docRepo *repository.DocumentRepository,
	sharedDocsRepo *repository.SharedDocumentRepository,
	keyHandoverRepo *repository.KeyHandoverRepository,
	stripe *stripeService.Service,
	wsHub *ws.Hub,
	notifHandler *NotificationHandler,
	urlSigner *PrivateURLSigner,
	pickupDeadline time.Duration,
	logger *slog.Logger,
) *LeaseRequestHandler {
	if pickupDeadline <= 0 {
		pickupDeadline = 2 * time.Hour
	}
	return &LeaseRequestHandler{
		leaseRepo:       leaseRepo,
		carRepo:         carRepo,
		carDocRepo:      carDocRepo,
		userRepo:        userRepo,
		chatRepo:        chatRepo,
		docRepo:         docRepo,
		sharedDocsRepo:  sharedDocsRepo,
		keyHandoverRepo: keyHandoverRepo,
		stripe:          stripe,
		wsHub:           wsHub,
		notifHandler:    notifHandler,
		urlSigner:       urlSigner,
		pickupDeadline:  pickupDeadline,
		logger:          logger,
	}
}

// PickupDeadline exposes the configured grace window. Used by the expiry
// worker (started from main.go) to size its ticker and for tests.
func (h *LeaseRequestHandler) PickupDeadline() time.Duration { return h.pickupDeadline }

// CreateLeaseRequest handles POST /api/v1/listings/{listingId}/lease-requests
func (h *LeaseRequestHandler) CreateLeaseRequest(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	// F2(a), locked decision: a driver whose licence was declined by admin
	// (or is missing) may not START a new rental. Scoped deliberately to
	// request-creation only — active rentals and anything already committed
	// are untouched. HasRequiredDocuments excludes rejected licences, so
	// this is the same gate onboarding and mode-switching already use.
	if hasDocs, derr := h.docRepo.HasRequiredDocuments(r.Context(), userID); derr == nil && !hasDocs {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError(
			"DRIVER_LICENSE_INVALID",
			"Your driver's license was declined or is missing. Upload a new copy in Profile → My documents to book a car."))
		return
	}

	listingID, err := uuid.Parse(chi.URLParam(r, "listingId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid listing ID"))
		return
	}

	var body models.CreateLeaseRequestBody
	if err := httputil.DecodeJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		// A body we cannot read is refused, not treated as empty. The old
		// behaviour reset the body and silently dropped billing_mode, so a
		// typo'd client produced a fixed-term lease with a 201 (review 2026-09-17).
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	// Fetch the car listing
	car, err := h.carRepo.GetByID(r.Context(), listingID)
	if err != nil {
		h.logger.Error("get car for lease request", "error", err)
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("CAR_NOT_FOUND", "Car listing not found"))
		return
	}

	// Archived listings are dead to the marketplace. GetByID deliberately
	// returns them (history flows need to resolve them), so every write
	// entry point must gate explicitly — purchase Create already does.
	// Without this mirror, a driver holding a stale car id (likes list,
	// old deep link) could open a lease against a "deleted" listing.
	if car == nil || car.IsArchived() {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("CAR_NOT_FOUND", "Car listing not found"))
		return
	}

	// Validate: car must be for rent
	if !car.IsForRent || !car.WeeklyRentPrice.Valid {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrCarNotForRent)
		return
	}

	// Availability guard (lifecycle batch, defect 1) — mirrors Discovery's
	// WHERE clause, so a car a driver can't browse can't be requested from
	// a stale list either. Split by what the driver should hear:
	//   - rented / sold / reserved → CAR_NOT_AVAILABLE 409: "someone else
	//     has it right now" (the reservation check is load-bearing — a
	//     paid-but-not-picked-up car still reads status='available').
	//   - paused / unapproved → CAR_NOT_FOR_RENT 400: the owner or admin
	//     has it out of the marketplace. Gating on IsApproved rather than
	//     status=='pending' also keeps AUTO_APPROVE_CARS environments
	//     working, where approved cars retain the 'pending' status.
	if car.Status == models.CarStatusRented || car.Status == models.CarStatusSold {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarNotAvailable)
		return
	}
	if car.IsPaused || !car.IsApproved {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrCarNotForRent)
		return
	}
	occupied, oerr := h.carRepo.IsOccupied(r.Context(), car.ID)
	if oerr != nil {
		h.logger.Error("lease create: occupancy check", "error", oerr, "car_id", car.ID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if occupied {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarNotAvailable)
		return
	}

	// Validate: driver cannot request own car
	if userID == car.OwnerID {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrCannotLeaseOwnCar)
		return
	}

	// Outstanding balance blocks a new rental. Until this shipped the app
	// told drivers "new bookings are paused until this is resolved" and
	// nothing enforced it — a false statement in a payment notification.
	// Applies to BOTH billing modes on purpose: the debt is the driver's,
	// not the rental's. It is inert for anyone who owes nothing.
	//
	// The number read here is BlockingBalanceFor, NOT BalanceFor. They are
	// deliberately different: the app shows everything owed, but only a debt
	// the driver can actually pay right now may refuse a booking. See the
	// comment on BlockingBalanceFor — the refusal and the remedy used to read
	// different tables, and two reachable states drove them apart into a dead
	// end whose only exit was hand-written SQL. Since the gate sits before the
	// billing-mode branch below, that dead end cost the driver the whole
	// marketplace, fixed-term included.
	if h.debtEnforce && h.debtRepo != nil {
		bal, berr := h.debtRepo.BlockingBalanceFor(r.Context(), userID)
		if berr != nil {
			// Fail OPEN: a ledger read failure must not stop an honest
			// driver renting. The debt does not disappear; the next attempt
			// re-checks it.
			//
			// GET /me/balance fails CLOSED on the same read (500), which is
			// the correct pairing: we refuse to SHOW a number we cannot
			// compute, and we refuse to ACT on one we cannot compute. What
			// must never happen is the inverse — telling someone here that
			// they owe money and then 500ing on the only screen that itemises
			// it. That asymmetry is why this arm logs loudly instead of
			// silently allowing.
			h.logger.Error("lease create: debt balance read failed", "error", berr, "driver_id", userID)
		} else if bal.HasBalance() {
			apiErr := models.NewAPIError("OUTSTANDING_BALANCE", fmt.Sprintf(
				"You have an unpaid balance of $%.2f from a previous rental. Clear it to start a new one.",
				float64(bal.OutstandingCents)/100))
			apiErr.Details = map[string]interface{}{
				"outstanding_cents": bal.OutstandingCents,
				"currency":          bal.Currency,
				"open_debt_count":   bal.OpenDebtCount,
			}
			// Note: every iOS build installed today discards this map —
			// APIErrorDetails decodes only missing_types. It is populated for
			// the admin console and future clients, and must never be
			// mistaken for the fix. The fix is that this arm can now only
			// fire for a debt pay-now would accept.
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
	}

	weeks := 1
	if body.Weeks != nil && *body.Weeks > 0 {
		weeks = *body.Weeks
	}

	// Recurring-only (decision 2026-09-17, docs/DESIGN_RECURRING_BILLING.md
	// §11). The SERVER decides the mode; the client's billing_mode field is
	// advisory at most. Three outcomes:
	//   eligible driver           → recurring on the listing's interval, always
	//   RECURRING_ONLY on         → fixed-term refused for everyone
	//   otherwise                 → fixed-term, exactly as before (SERVICING)
	// An eligible driver whose build predates the consent sheet, or whose
	// client asks for fixed-term explicitly, is REFUSED with an instruction in
	// the top-level message — old builds render that field and discard
	// details (ios APIClient.swift:1439-1444). A refusal is an exit; the old
	// silent downgrade to fixed-term was not.
	billingMode := models.BillingModeFixedTerm
	billingInterval := "weekly"
	explicitFixed := body.BillingMode != nil && *body.BillingMode == string(models.BillingModeFixedTerm)
	explicitRolling := body.BillingMode != nil && *body.BillingMode == string(models.BillingModeRolling)
	clientBuild := httputil.ClientBuild(r)
	// Messages. Old builds render ONLY error.message. Both must be TRUE today:
	// there is no App Store build with the consent sheet yet, so "update"
	// alone is an instruction that cannot be followed (review 2026-09-17).
	const updateMsg = "Your version of DriveBai can't show the rental terms this car needs, so this request wasn't sent and nothing was charged. Ask us for the TestFlight build, or watch for the next App Store update."

	// A NAMED pilot (non-empty allowlist) or RECURRING_ONLY is where refusals
	// are allowed to bite. With rolling open to everyone and RECURRING_ONLY
	// off, an eligible driver on an old build still gets the historical
	// fixed-term lease (now WARN-logged) — that is what keeps this deploy
	// dark for the public.
	pilot := h.rollingAllowlist != nil
	switch {
	case h.RollingOpenFor(userID):
		// A pilot names BOTH parties (review 2026-09-17, lens 3): an owner
		// outside it may be on a build with no owner-terms sheet, and a
		// recurring request would strand them at Accept. Fall to fixed-term
		// with a voice rather than refuse.
		if pilot && !h.RollingOpenFor(car.OwnerID) {
			h.logger.Warn("lease create: owner not in the weekly pilot — fixed-term created",
				"driver_id", userID, "owner_id", car.OwnerID, "listing_id", listingID, "client_build", clientBuild, "user_agent", r.UserAgent())
			break
		}
		interval, ok := models.BillingIntervalForRentPeriod(car.RentPricePeriod)
		if ok && interval == "monthly" && !h.monthlyEnabled {
			ok = false
		}
		if !ok {
			h.logger.Warn("lease create: listing period not supported for a recurring lease",
				"driver_id", userID, "listing_id", listingID, "owner_id", car.OwnerID, "period", car.RentPricePeriod)
			// Same words the listing screen shows, so the refusal never
			// contradicts the notice the driver just read.
			msg := h.RentRefusalFor(userID, car.OwnerID, car.RentPricePeriod)
			if msg == "" {
				msg = "This car can't be rented right now."
			}
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("INTERVAL_NOT_SUPPORTED", msg))
			return
		}
		oldBuild := clientBuild > 0 && clientBuild < models.FirstConsentSheetBuild
		if (explicitFixed || oldBuild) && (pilot || h.recurringOnly) {
			h.logger.Warn("lease create: eligible driver refused — update required",
				"driver_id", userID, "client_build", clientBuild, "explicit_fixed", explicitFixed, "user_agent", r.UserAgent())
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("APP_UPDATE_REQUIRED", updateMsg))
			return
		}
		if explicitFixed || oldBuild {
			// Public + RECURRING_ONLY off: the historical lease, said out loud.
			h.logger.Warn("lease create: fixed-term created for an eligible driver (public rollout, old client)",
				"driver_id", userID, "client_build", clientBuild, "explicit_fixed", explicitFixed, "user_agent", r.UserAgent(), "listing_id", listingID)
			break
		}
		billingMode = models.BillingModeRolling
		billingInterval = interval
		weeks = 1
	case h.recurringOnly:
		h.logger.Warn("lease create: refused — RECURRING_ONLY is on and the driver is not eligible",
			"driver_id", userID, "client_build", clientBuild, "user_agent", r.UserAgent())
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RENTALS_PAUSED", rentalsPausedMsg))
		return
	default:
		if explicitRolling {
			httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("ROLLING_DISABLED",
				"Weekly rentals aren't available right now"))
			return
		}
		if h.rollingEnabled {
			h.logger.Warn("lease create: fixed-term created while rolling is enabled",
				"driver_id", userID, "client_build", clientBuild, "user_agent", r.UserAgent(), "listing_id", listingID)
		}
	}

	lr := &models.LeaseRequest{
		ListingID:       listingID,
		OwnerID:         car.OwnerID,
		DriverID:        userID,
		WeeklyPrice:     car.WeeklyRentPrice.Float64,
		Currency:        car.Currency,
		Weeks:           weeks,
		Message:         body.Message,
		BillingMode:     billingMode,
		BillingInterval: billingInterval,
	}

	created, err := h.leaseRepo.CreateLeaseRequest(r.Context(), lr)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
		} else {
			h.logger.Error("create lease request", "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	// Auto-share the driver's onboarding documents with the owner via this
	// lease request. Non-fatal: if the driver has no docs yet, or the insert
	// fails transiently, the lease request itself still succeeds — the owner
	// simply won't see a Driver Documents section until docs are re-shared on
	// a subsequent request.
	h.shareDriverDocs(r.Context(), created)

	// Build response with names
	resp := h.buildLeaseRequestResponse(r, created, nil)

	httputil.WriteJSON(w, http.StatusCreated, models.CreateLeaseRequestResponse{
		ChatID:       created.ChatID,
		LeaseRequest: resp,
	})

	// Broadcast to owner via WebSocket
	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_created",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{created.OwnerID},
	})

	// In-app notification + push for owner
	chatID := created.ChatID
	leaseID := created.ID
	driverName := resp.DriverName
	if driverName == "" {
		driverName = "A driver"
	}
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "your listing"
	}
	notifBody := fmt.Sprintf("%s requested %d week(s) for %s", driverName, created.Weeks, carTitle)
	if created.BillingMode == models.BillingModeRolling {
		notifBody = fmt.Sprintf("%s wants to rent %s — renews %s until returned", driverName, carTitle, models.IntervalLabel(created.BillingInterval))
	}
	go h.notifHandler.Notify(created.OwnerID, models.NotificationTypeLeaseRequest,
		"New lease request", notifBody, &chatID, &leaseID)
}

// ListLeaseRequests handles GET /api/v1/chats/{chatId}/lease-requests
func (h *LeaseRequestHandler) ListLeaseRequests(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid chat ID"))
		return
	}

	// Verify participant
	isParticipant, err := h.chatRepo.IsParticipant(r.Context(), chatID, userID)
	if err != nil {
		h.logger.Error("check participant", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !isParticipant {
		httputil.WriteError(w, http.StatusForbidden, models.ErrNotParticipant)
		return
	}

	leaseRequests, err := h.leaseRepo.ListForChat(r.Context(), chatID)
	if err != nil {
		h.logger.Error("list lease requests", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, models.LeaseRequestsListResponse{
		LeaseRequests: leaseRequests,
	})
}

// AcceptLeaseRequest handles POST /api/v1/lease-requests/{id}/accept
func (h *LeaseRequestHandler) AcceptLeaseRequest(w http.ResponseWriter, r *http.Request) {
	h.handleLeaseAction(w, r, "accept")
}

// DeclineLeaseRequest handles POST /api/v1/lease-requests/{id}/decline
func (h *LeaseRequestHandler) DeclineLeaseRequest(w http.ResponseWriter, r *http.Request) {
	h.handleLeaseAction(w, r, "decline")
}

// CancelLeaseRequest handles POST /api/v1/lease-requests/{id}/cancel
func (h *LeaseRequestHandler) CancelLeaseRequest(w http.ResponseWriter, r *http.Request) {
	h.handleLeaseAction(w, r, "cancel")
}

// RescindAcceptedLeaseRequest handles POST /api/v1/lease-requests/{id}/rescind.
// Owner-only path to undo a mistaken Accept while the lease is still in the
// `accepted` state (no payment in flight). Refuses with 409 once the driver
// has moved to payment_pending / paid — at that point the owner must either
// wait or use admin tooling to refund first. Releases the car reservation
// atomically with the status change, so Discovery sees the listing again
// before the response returns.
func (h *LeaseRequestHandler) RescindAcceptedLeaseRequest(w http.ResponseWriter, r *http.Request) {
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

	updated, err := h.leaseRepo.RescindAccept(r.Context(), leaseID, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			switch apiErr.Code {
			case models.ErrCodeLeaseRequestNotFound:
				status = http.StatusNotFound
			case models.ErrCodeInvalidLeaseAction:
				// "Only the owner can perform this action" → 403; status mismatch → 409.
				if apiErr.Message == "Only the owner can perform this action" {
					status = http.StatusForbidden
				} else {
					status = http.StatusConflict
				}
			}
			httputil.WriteError(w, status, apiErr)
			return
		}
		h.logger.Error("rescind accept", "error", err, "lease_request_id", leaseID, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildLeaseRequestResponse(r, updated, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{updated.DriverID, updated.OwnerID},
	})

	chatID := updated.ChatID
	lrID := updated.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "the car"
	}
	go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeLeaseRequest,
		"Owner cancelled the rental",
		fmt.Sprintf("The owner cancelled your accepted request for %s before payment. No charge was made.", carTitle),
		&chatID, &lrID)
}

func (h *LeaseRequestHandler) handleLeaseAction(w http.ResponseWriter, r *http.Request, action string) {
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

	// payment_pending exits (item 4): before declining/cancelling a lease
	// whose payment window is open, neutralize the PaymentIntent. Stripe
	// refuses to cancel a succeeded/processing intent — that refusal IS
	// the race signal: the payment won, back off and let the webhook flip
	// the lease to paid. A successfully cancelled intent can never be
	// confirmed, so proceeding is then safe; the repo's status-scoped
	// UPDATE stays the final serializer.
	if action == "decline" || action == "cancel" {
		if lr, gerr := h.leaseRepo.GetByID(r.Context(), leaseID); gerr == nil && lr != nil && lr.Status == models.LeaseStatusPaymentPending {
			payment, perr := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID)
			if perr != nil {
				// Can't see the payment → can't prove no money moved →
				// must not kill the lease (M3d closed the old fall-through).
				h.logger.Error("lease action: payment lookup", "error", perr, "lease_request_id", leaseID)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
			if payment != nil && payment.PaymentIntentID != nil {
				switch h.neutralizePaymentIntent(*payment.PaymentIntentID) {
				case piMoneyMoved:
					httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYMENT_IN_FLIGHT",
						"The payment just completed — refresh to see the paid rental"))
					return
				case piUnknown:
					httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("PAYMENT_STATE_UNKNOWN",
						"Couldn't verify the payment's state — try again in a moment"))
					return
				}
			}
		}
	}

	// Owner terms: a rolling lease request cannot be accepted by an owner
	// who has not accepted the owner package. Fixed-term requests never
	// pass through this (the helper checks billing_mode first).
	if action == "accept" && h.requireOwnerTermsForRollingAccept(w, r, leaseID, userID) {
		return
	}

	var updated *models.LeaseRequest
	switch action {
	case "accept":
		updated, err = h.leaseRepo.AcceptLeaseRequest(r.Context(), leaseID, userID)
	case "decline":
		updated, err = h.leaseRepo.DeclineLeaseRequest(r.Context(), leaseID, userID)
	case "cancel":
		updated, err = h.leaseRepo.CancelLeaseRequest(r.Context(), leaseID, userID)
	}

	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			switch apiErr.Code {
			case models.ErrCodeLeaseRequestNotFound:
				status = http.StatusNotFound
			case models.ErrCodeCarNotAvailable:
				// Accept lost to a concurrent transaction (another lease's
				// reservation, an in-flight purchase, rented/sold) — a
				// state conflict, not a bad request.
				status = http.StatusConflict
			}
			httputil.WriteError(w, status, apiErr)
		} else {
			h.logger.Error("lease action", "action", action, "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	resp := h.buildLeaseRequestResponse(r, updated, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	// Broadcast to the other party
	otherUserID := updated.OwnerID
	if userID == updated.OwnerID {
		otherUserID = updated.DriverID
	}
	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{otherUserID},
	})

	// In-app notification + push for the counterparty.
	// Previously this handler only WS-broadcasted, which meant accept/decline
	// of a lease request never reached a backgrounded recipient. The
	// notification surface is the same one used elsewhere in this file —
	// chat-id + lease-id give iOS the deep link. (leaseID is already in
	// scope from the URL parse at the top of the handler; rebind via &.)
	chatID := updated.ChatID
	notifyLeaseID := updated.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "your listing"
	}
	ownerName := resp.OwnerName
	if ownerName == "" {
		ownerName = "The owner"
	}

	switch action {
	case "accept":
		// Owner→Driver: request accepted, please pay.
		go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeLeaseRequest,
			"Request accepted",
			fmt.Sprintf("%s accepted your request for %s — complete payment to confirm.", ownerName, carTitle),
			&chatID, &notifyLeaseID)
	case "decline":
		// Owner→Driver: request declined.
		go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeLeaseRequest,
			"Request declined",
			fmt.Sprintf("%s declined your request for %s.", ownerName, carTitle),
			&chatID, &notifyLeaseID)
	case "cancel":
		// Cancel can be initiated by either side pre-accept. Notify the
		// OTHER party. otherUserID was already computed above as the
		// non-actor — reuse it so we don't ping the actor's own device.
		go h.notifHandler.Notify(otherUserID, models.NotificationTypeLeaseRequest,
			"Request cancelled",
			fmt.Sprintf("The lease request for %s was cancelled.", carTitle),
			&chatID, &notifyLeaseID)
	}
}

// --- Payment endpoints ---

// CreatePaymentIntent handles POST /api/v1/lease-requests/{id}/payments/intent
func (h *LeaseRequestHandler) CreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
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

	// Fetch lease request
	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusNotFound, apiErr)
		} else {
			h.logger.Error("get lease request for payment", "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	// Only the driver can pay
	if userID != lr.DriverID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the driver can initiate payment"))
		return
	}

	// Rolling: the consent ceremony lives in the client (build >= 35). This
	// is the step that takes money and binds the mandate, so an unproven
	// client is refused HERE, before any Stripe call, regardless of what
	// request-time decided — an unknown or stripped User-Agent is "not
	// proven capable", never "not proven old" (review 2026-09-17).
	if lr.BillingMode == models.BillingModeRolling {
		if b := httputil.ClientBuild(r); b < models.FirstConsentSheetBuild {
			h.logger.Warn("rolling intent: client cannot show the consent sheet — refused",
				"lease_request_id", leaseID, "client_build", b, "user_agent", r.UserAgent())
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("APP_UPDATE_REQUIRED",
				"Your version of DriveBai can't show the rental terms, so nothing was charged. Ask us for the TestFlight build, or watch for the next App Store update."))
			return
		}
	}

	// Block payment while the driver still has to act on a price change.
	// We refuse with 409 PRICE_REVIEW_PENDING so iOS can map this to the
	// "Owner updated the price — accept or decline before paying" surface.
	// Check this BEFORE the status check so the more-specific error wins.
	if lr.PriceChangePending {
		httputil.WriteError(w, http.StatusConflict, models.ErrPriceReviewPending)
		return
	}

	// Must be in accepted status (or payment_pending if retrying)
	if lr.Status != models.LeaseStatusAccepted && lr.Status != models.LeaseStatusPaymentPending {
		httputil.WriteError(w, http.StatusBadRequest, models.NewAPIError(models.ErrCodeInvalidLeaseAction, "Lease request must be accepted before payment"))
		return
	}

	// The lease can be payable while the CAR has left the marketplace
	// underneath it — sold through the purchase flow or archived by the
	// owner (lifecycle batch, defect 1 audit). Paying for a car that can
	// never be picked up is exactly the "flow that cannot finish" this
	// batch closes; refuse before any money moves.
	if payCar, cerr := h.carRepo.GetByID(r.Context(), lr.ListingID); cerr != nil || payCar == nil {
		h.logger.Error("payment: load car", "error", cerr, "listing_id", lr.ListingID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	} else if payCar.IsArchived() || payCar.Status == models.CarStatusSold {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarNotAvailable)
		return
	}
	// A purchase that started before this lease was accepted can be racing
	// toward handover; only the purchase side of occupancy is checked here
	// — the lease rightfully holds its own reservation at pay time.
	if blocked, berr := h.carRepo.HasBlockingPurchase(r.Context(), lr.ListingID); berr != nil {
		h.logger.Error("payment: blocking-purchase check", "error", berr, "listing_id", lr.ListingID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	} else if blocked {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarNotAvailable)
		return
	}

	// Check if payment already exists (idempotent — return stored client_secret)
	existingPayment, err := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID)
	if err != nil {
		h.logger.Error("check existing payment", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// The stored intent's own status decides whether it may be re-served
	// (H2: the old unconditional fast path re-served dead client_secrets
	// forever — succeeded ones with a lost webhook, canceled ones after a
	// partial decline).
	var replacePayment *models.Payment
	if existingPayment != nil && existingPayment.PaymentIntentID != nil && existingPayment.ClientSecret != nil {
		switch existingPayment.Status {
		case models.PaymentStatusSucceeded:
			// The charge already landed — adopt it (lease → paid, or the
			// orphan pipeline if the lease died) instead of handing the
			// driver a sheet that can only error.
			h.adoptSucceededPayment(r.Context(), existingPayment)
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_PAID",
				"This payment already completed — refresh to see the result"))
			return
		case models.PaymentStatusRefunded, models.PaymentStatusRefundUnrecoverable:
			// Terminal reconciliation outcomes (review R3): the lease died
			// and the charge was (or is being) returned. Never re-enter the
			// adopt path — that would resurrect a claimed-once state.
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("REQUEST_CLOSED",
				"This request closed and the payment was refunded — send a new request if you still want the car"))
			return
		case models.PaymentStatusProcessing:
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYMENT_IN_FLIGHT",
				"Your payment is still processing — refresh in a moment"))
			return
		case models.PaymentStatusCanceled:
			// Dead intent (price-change cancel, partial decline, sweep). Fall
			// through to mint a replacement on the same payment row.
			replacePayment = existingPayment
		default:
			// requires_payment_method / _confirmation — the normal retry.
			customerID := ""
			ephemeralKeySecret := ""
			if existingPayment.StripeCustomerID != nil {
				customerID = *existingPayment.StripeCustomerID
				ek, ekErr := h.stripe.CreateEphemeralKey(customerID)
				if ekErr == nil {
					ephemeralKeySecret = ek.Secret
				}
			}

			h.logger.Info("returning existing payment intent", "lease_request_id", leaseID, "payment_intent_id", *existingPayment.PaymentIntentID)

			// A rolling retry must still show the recorded consent text.
			existingDisclosure, existingTerms := "", ""
			if lr.BillingMode == models.BillingModeRolling && h.billingRepo != nil {
				if c, gerr := h.billingRepo.GetActiveConsent(r.Context(), leaseID); gerr == nil && c != nil {
					existingDisclosure, existingTerms = c.DisclosureText, c.TermsVersion
				}
			}
			httputil.WriteJSON(w, http.StatusOK, models.PaymentIntentResponse{
				PaymentIntentClientSecret: *existingPayment.ClientSecret,
				PaymentIntentID:           *existingPayment.PaymentIntentID,
				PublishableKey:            h.stripe.PublishableKey(),
				CustomerID:                customerID,
				EphemeralKeySecret:        ephemeralKeySecret,
				Amount:                    existingPayment.Amount,
				Currency:                  existingPayment.Currency,
				DisclosureText:            existingDisclosure,
				TermsVersion:              existingTerms,
			})
			return
		}
	}

	// Claim the payment window BEFORE any Stripe call. This status-scoped
	// transition is the serializer against the accept-expiry sweep (M3a):
	// whoever moves the row first wins, and a lease the sweep already
	// expired can never mint a live PaymentSheet. The old order — create
	// the PI first, then Warn-and-continue when this transition matched
	// zero rows — handed drivers a working sheet for a dead lease.
	windowClaimed := false
	if lr.Status == models.LeaseStatusAccepted {
		if _, err := h.leaseRepo.SetPaymentPending(r.Context(), leaseID); err != nil {
			h.logger.Warn("payment intent refused: lease no longer payable", "error", err, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError(models.ErrCodeInvalidLeaseAction,
				"This request just expired — refresh the chat"))
			return
		}
		windowClaimed = true
	}
	// revertWindow undoes a claim this request made when the Stripe leg
	// fails before a live intent exists (review R4) — otherwise the lease
	// sits at payment_pending with no payment row, a state iOS renders
	// without a Pay button, until the 24h sweep. Best-effort; the repo
	// guard (no payments row) keeps it from ever demoting a real window.
	revertWindow := func() {
		if !windowClaimed {
			return
		}
		if _, rerr := h.leaseRepo.RevertPaymentWindow(r.Context(), leaseID); rerr != nil {
			h.logger.Error("revert payment window", "error", rerr, "lease_request_id", leaseID)
		}
	}

	// Compute amount
	totalCents := lr.TotalAmountCents()
	if lr.BillingMode == models.BillingModeRolling {
		// One cycle of the lease's interval — for monthly that is the weekly
		// figure ×RentMonthWeeks, i.e. the owner's typed monthly amount.
		totalCents = lr.IntervalAmountCents()
	}
	// A nil Stripe service is a configuration state, not a request to panic
	// on. Placed AFTER the adopt branch so a retry that only needs to adopt
	// an already-succeeded payment never touches Stripe (2026-09-17).
	if h.stripe == nil {
		revertWindow()
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("PAYMENTS_DISABLED", "payments not configured"))
		return
	}
	platformFeeCents := h.stripe.PlatformFee(totalCents)

	// Get driver user for Stripe customer
	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("get user for stripe", "error", err)
		revertWindow()
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Resolve the user's bound Stripe customer (H6 — never by email).
	customer, err := customerForUser(r.Context(), h.stripe, h.userRepo, user, h.logger)
	if err != nil {
		h.logger.Error("stripe resolve customer", "error", err)
		revertWindow()
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create payment customer"))
		return
	}

	// Create ephemeral key
	ephemeralKey, err := h.stripe.CreateEphemeralKey(customer.ID)
	if err != nil {
		h.logger.Error("stripe create ephemeral key", "error", err)
		revertWindow()
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create ephemeral key"))
		return
	}

	// Create PaymentIntent. Idempotency key = lease request ID for the first
	// intent; a REPLACEMENT (dead canceled intent being swapped out) must
	// use a different key — Stripe's idempotency cache would otherwise hand
	// back the very canceled intent we're replacing. Keyed on the
	// predecessor so replays of the same replacement still dedupe.
	piIdemKey := leaseID.String()
	if replacePayment != nil && replacePayment.PaymentIntentID != nil {
		piIdemKey = fmt.Sprintf("lease-%s-after-%s", leaseID, *replacePayment.PaymentIntentID)
	}
	// Rolling cycle 1 (batch 2): record consent BEFORE the intent exists,
	// and save the card under the stored-credential framework. The consent
	// row is the amount authority for every later off-session charge.
	piOpts := stripeService.PaymentIntentOptions{}
	var recordedDisclosure, recordedTerms string
	if lr.BillingMode == models.BillingModeRolling {
		if !h.rollingEnabled || h.billingRepo == nil {
			revertWindow()
			httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("ROLLING_DISABLED",
				"Weekly rentals aren't available right now"))
			return
		}
		// The package for this consent's interval. CreateConsent records
		// 'weekly' (the only interval the schema admits), so this resolves
		// to v3: the text that discloses the persistent balance the debt
		// ledger enforces. v2 predates that ledger — a driver who consented
		// on v2 was never told a debt blocks new rentals or survives
		// account closure, and enforcement is on by default.
		disclosureText, termsVersion := models.RollingDisclosureFor(lr.BillingInterval, totalCents)
		consentRow, cerr := h.billingRepo.CreateConsent(r.Context(), &models.BillingConsent{
			LeaseRequestID:  leaseID,
			DriverID:        lr.DriverID,
			AmountCents:     totalCents,
			BillingInterval: lr.BillingInterval,
			TermsVersion:    termsVersion,
			DisclosureText:  disclosureText,
		})
		if cerr != nil || consentRow == nil {
			h.logger.Error("rolling consent: create", "error", cerr, "lease_request_id", leaseID)
			revertWindow()
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		// Serve the ROW's text, not a re-render: if an unactivated row
		// already existed, what it holds is what the driver must see.
		recordedDisclosure, recordedTerms = consentRow.DisclosureText, consentRow.TermsVersion
		piOpts.SetupFutureUsage = "off_session"
	}
	pi, err := h.stripe.CreatePaymentIntentWithOptions(totalCents, lr.Currency, customer.ID, platformFeeCents, piIdemKey, piOpts)
	if err != nil {
		h.logger.Error("stripe create payment intent", "error", err)
		revertWindow()
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create payment"))
		return
	}

	h.logger.Info("payment intent created",
		"lease_request_id", leaseID,
		"payment_intent_id", pi.ID,
		"amount_cents", totalCents,
		"currency", lr.Currency,
		"customer_id", customer.ID,
	)

	if replacePayment != nil {
		// Swap the dead intent for the fresh one on the same row. Status-
		// scoped to 'canceled': if a webhook somehow succeeded the old
		// intent in the meantime, the swap refuses and the driver's next
		// tap lands in the adopt branch above.
		swapped, serr := h.leaseRepo.ReplacePaymentIntent(r.Context(), replacePayment.ID, pi.ID, pi.ClientSecret, totalCents, platformFeeCents)
		if serr != nil {
			h.logger.Error("replace payment intent", "error", serr, "lease_request_id", leaseID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if !swapped {
			// The fresh intent never reached any payments row — cancel it
			// so it can't accumulate as Stripe-side clutter (review LOW).
			if cerr := h.stripe.CancelPaymentIntent(pi.ID); cerr != nil {
				h.logger.Warn("cancel unswapped replacement intent", "error", cerr, "intent_id", pi.ID)
			}
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYMENT_IN_FLIGHT",
				"The payment state just changed — refresh and try again"))
			return
		}
	} else {
		// Save payment record (including client_secret for retry)
		payment := &models.Payment{
			LeaseRequestID:    leaseID,
			Provider:          "stripe",
			StripeCustomerID:  &customer.ID,
			PaymentIntentID:   &pi.ID,
			ClientSecret:      &pi.ClientSecret,
			Amount:            totalCents,
			Currency:          lr.Currency,
			PlatformFeeAmount: platformFeeCents,
			Status:            models.PaymentStatusRequiresPaymentMethod,
		}

		_, err = h.leaseRepo.CreatePayment(r.Context(), payment)
		if err != nil {
			// If duplicate, that's OK — idempotent
			if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodePaymentAlreadyExists {
				h.logger.Info("payment already exists, returning existing", "lease_request_id", leaseID)
			} else {
				h.logger.Error("save payment record", "error", err)
				// The intent exists at Stripe but no row records it —
				// neutralize it before reverting the window (it was never
				// served to the client, so it cannot have succeeded).
				if cerr := h.stripe.CancelPaymentIntent(pi.ID); cerr != nil {
					h.logger.Warn("cancel unrecorded intent", "error", cerr, "intent_id", pi.ID)
				}
				revertWindow()
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, models.PaymentIntentResponse{
		PaymentIntentClientSecret: pi.ClientSecret,
		PaymentIntentID:           pi.ID,
		PublishableKey:            h.stripe.PublishableKey(),
		CustomerID:                customer.ID,
		EphemeralKeySecret:        ephemeralKey.Secret,
		Amount:                    totalCents,
		Currency:                  lr.Currency,
		DisclosureText:            recordedDisclosure,
		TermsVersion:              recordedTerms,
	})
}

// SyncPaymentStatus handles POST /api/v1/lease-requests/{id}/payments/sync
// Fallback mechanism: queries Stripe for current PaymentIntent status and reconciles locally.
func (h *LeaseRequestHandler) SyncPaymentStatus(w http.ResponseWriter, r *http.Request) {
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
		httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
		return
	}

	// Only participants can sync
	if userID != lr.DriverID && userID != lr.OwnerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Not a participant"))
		return
	}

	// If already paid, return current state
	if lr.Status == models.LeaseStatusPaid {
		resp := h.buildLeaseRequestResponse(r, lr, nil)
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}

	// Get local payment record
	payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID)
	if err != nil || payment == nil || payment.PaymentIntentID == nil {
		resp := h.buildLeaseRequestResponse(r, lr, payment)
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}

	// Query Stripe for current PI status
	pi, err := h.stripe.RetrievePaymentIntent(*payment.PaymentIntentID)
	if err != nil {
		h.logger.Error("sync: retrieve PI from Stripe", "error", err, "intent_id", *payment.PaymentIntentID)
		resp := h.buildLeaseRequestResponse(r, lr, payment)
		httputil.WriteJSON(w, http.StatusOK, resp)
		return
	}

	h.logger.Info("sync: stripe PI status", "intent_id", pi.ID, "stripe_status", pi.Status, "local_payment_status", payment.Status, "lease_status", lr.Status)

	// Map Stripe status → local PaymentStatus
	newStatus := mapStripeStatus(pi.Status, payment.Status)

	// Update payment status if changed
	if newStatus != payment.Status {
		if err := h.leaseRepo.UpdatePaymentStatus(r.Context(), payment.ID, newStatus); err != nil {
			h.logger.Error("sync: update payment status", "error", err)
		} else {
			payment.Status = newStatus
		}
	}

	// If payment succeeded, transition lease to paid
	if newStatus == models.PaymentStatusSucceeded && lr.Status != models.LeaseStatusPaid {
		updatedLR, err := h.leaseRepo.SetPaid(r.Context(), leaseID)
		if err != nil {
			h.logger.Warn("sync: set lease paid", "error", err, "lease_request_id", leaseID, "current_status", lr.Status)
		} else {
			lr = updatedLR
			h.logger.Info("sync: lease transitioned to paid", "lease_request_id", leaseID)
			// Bind the mandate BEFORE telling the owner it renews.
			h.activateRecoveredConsent(r.Context(), lr, pi.ID, pi.PaymentMethod)
			syncResp := h.buildLeaseRequestResponse(r, lr, payment)
			h.wsHub.Broadcast(&ws.Event{
				Type:          "lease_request_updated",
				Payload:       syncResp,
				TargetUserIDs: []uuid.UUID{lr.DriverID, lr.OwnerID},
			})
			chatID := lr.ChatID
			lrID := lr.ID
			driverName := syncResp.DriverName
			if driverName == "" {
				driverName = "The driver"
			}
			carTitle := syncResp.CarTitle
			if carTitle == "" {
				carTitle = "your listing"
			}
			go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
				"Payment received",
				ownerPaidBody(driverName, carTitle, lr),
				&chatID, &lrID)
			go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
				"Payment confirmed",
				fmt.Sprintf("Payment confirmed for %s — wait for pickup instructions from the owner", carTitle),
				&chatID, &lrID)

			// Create the key-handover task (idempotent with the webhook path).
			h.ensureKeyHandover(r, lr)

			// Arm the pickup deadline (idempotent — guarded by status='paid'
			// AND pickup_deadline_at IS NULL inside the repo).
			h.armPickupDeadline(r.Context(), lr)

			// Close out competing requests (idempotent with the webhook path).
			h.declineSiblingsOfPaidLease(r.Context(), lr)
		}
	}

	resp := h.buildLeaseRequestResponse(r, lr, payment)
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// mapStripeStatus converts a Stripe PaymentIntent status string to our PaymentStatus.
func mapStripeStatus(stripeStatus string, fallback models.PaymentStatus) models.PaymentStatus {
	switch stripeStatus {
	case "succeeded":
		return models.PaymentStatusSucceeded
	case "processing":
		return models.PaymentStatusProcessing
	case "requires_payment_method":
		return models.PaymentStatusRequiresPaymentMethod
	case "requires_confirmation":
		return models.PaymentStatusRequiresConfirmation
	case "canceled":
		return models.PaymentStatusCanceled
	default:
		return fallback
	}
}

// HandleWebhook handles POST /api/v1/stripe/webhook
func (h *LeaseRequestHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		h.logger.Error("read webhook body", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sigHeader := r.Header.Get("Stripe-Signature")
	if sigHeader == "" {
		h.logger.Warn("webhook: missing Stripe-Signature header")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	event, err := h.stripe.VerifyWebhookSignature(payload, sigHeader)
	if err != nil {
		h.logger.Warn("webhook: signature verification failed", "error", err, "webhook_secret_set", h.stripe.WebhookSecret() != "")
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	eventType, _ := event["type"].(string)
	dataObj, _ := event["data"].(map[string]interface{})
	obj, _ := dataObj["object"].(map[string]interface{})
	intentID, _ := obj["id"].(string)

	h.logger.Info("webhook: event received", "type", eventType, "intent_id", intentID, "verified", true)

	if intentID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Dispute and refund events carry a dispute/charge object, not a PI —
	// route them BEFORE the intent-keyed switch (batch 1, audit M2).
	switch eventType {
	case "charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed":
		if !h.handleChargeDispute(r, eventType, obj) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	case "charge.refunded":
		if !h.handleChargeRefunded(r, obj) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Rolling cycle intents carry metadata.kind="cycle": success runs the
	// atomic advance; failures are the sweep's job (it reads intent state
	// on its own clock), so they ACK.
	if md, ok := obj["metadata"].(map[string]interface{}); ok {
		if kind, _ := md["kind"].(string); kind == "cycle" {
			if eventType == "payment_intent.succeeded" {
				cycleIDStr, _ := md["billing_cycle_id"].(string)
				if cycleID, perr := uuid.Parse(cycleIDStr); perr == nil {
					if !h.handleCyclePaid(r.Context(), cycleID, intentID) {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		// kind="arrears" (batch 4): a post-return debt settled on-session.
		// Deliberately NOT handleCyclePaid — its occupancy guard auto-
		// refunds charges landing after a return, which is exactly wrong
		// for arrears. Failures ACK (the driver just retries in the app).
		if kind, _ := md["kind"].(string); kind == "arrears" {
			if eventType == "payment_intent.succeeded" {
				cycleIDStr, _ := md["billing_cycle_id"].(string)
				if cycleID, perr := uuid.Parse(cycleIDStr); perr == nil {
					if !h.handleArrearsPaid(r.Context(), cycleID, intentID) {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
				}
			}
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Route by PI metadata: purchase intents carry metadata.kind="purchase"
	// so the purchase state machine handles amount_capturable_updated /
	// succeeded / canceled without polluting the lease code paths.
	if h.purchaseHandler != nil {
		if md, ok := obj["metadata"].(map[string]interface{}); ok {
			if kind, _ := md["kind"].(string); kind == "purchase" {
				h.purchaseHandler.HandleStripeEvent(r.Context(), eventType, intentID)
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}

	switch eventType {
	case "payment_intent.succeeded":
		// A success signal we failed to persist must NOT be ACKed — a 200
		// here told Stripe "done" and the retry never came (H2). 500 makes
		// Stripe redeliver with backoff for days.
		if !h.handlePaymentSucceeded(r, intentID, obj) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	case "payment_intent.payment_failed":
		h.handlePaymentFailed(r, intentID)
	case "payment_intent.canceled":
		h.handlePaymentCanceled(r, intentID)
	}

	w.WriteHeader(http.StatusOK)
}

// handlePaymentSucceeded returns false only for failures a Stripe redelivery
// can fix (DB errors mid-processing); the webhook then answers 500. Benign
// and terminally-triaged outcomes return true.
func (h *LeaseRequestHandler) handlePaymentSucceeded(r *http.Request, intentID string, obj map[string]interface{}) bool {
	payment, err := h.leaseRepo.GetPaymentByIntentID(r.Context(), intentID)
	if err != nil {
		// Not found = PI was not created by us; ignore silently
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodePaymentNotFound {
			h.logger.Info("webhook: ignoring unknown payment_intent", "intent_id", intentID)
			return true
		}
		h.logger.Error("webhook: get payment by intent", "event", "succeeded", "intent_id", intentID, "error", err)
		return false // 500 → Stripe redelivers (H2: never ACK a signal we failed to read)
	}

	// Idempotency: reconciled payments (refunded family) are terminal —
	// skip. An already-SUCCEEDED payment is NOT skipped outright: a prior
	// delivery may have persisted the payment and then failed SetPaid (we
	// answered 500, this IS the redelivery) — skipping here would defeat
	// the very retry the 500 asked for. SetPaid below is status-scoped, so
	// the fully-processed case degrades to the benign already-paid branch.
	switch payment.Status {
	case models.PaymentStatusRefunded, models.PaymentStatusRefundUnrecoverable:
		h.logger.Info("webhook: payment already reconciled (idempotent skip)", "intent_id", intentID, "payment_id", payment.ID, "status", payment.Status)
		return true
	case models.PaymentStatusSucceeded:
		// Already recorded — proceed straight to the lease transition.
	default:
		if err := h.leaseRepo.UpdatePaymentStatus(r.Context(), payment.ID, models.PaymentStatusSucceeded); err != nil {
			h.logger.Error("webhook: update payment status", "event", "succeeded", "payment_id", payment.ID, "error", err)
			return false
		}
		payment.Status = models.PaymentStatusSucceeded
	}

	// Transition lease request to paid (accepts both accepted and payment_pending)
	lr, err := h.leaseRepo.SetPaid(r.Context(), payment.LeaseRequestID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodeInvalidLeaseAction {
			// The lease moved on without us. "Already paid" is the benign
			// idempotent case; a TERMINAL lease means we just captured
			// money for a dead request — the M3 bug was treating both as
			// "skip". Triage exactly like the adopt path.
			cur, gerr := h.leaseRepo.GetByID(r.Context(), payment.LeaseRequestID)
			if gerr != nil || cur == nil {
				h.logger.Error("webhook: reload lease after set-paid refusal", "error", gerr, "lease_request_id", payment.LeaseRequestID)
				return false
			}
			switch cur.Status {
			case models.LeaseStatusPaid:
				h.logger.Info("webhook: lease already paid (idempotent skip)", "lease_request_id", cur.ID, "intent_id", intentID)
				// A crash between SetPaid and consent activation lands the
				// redelivery HERE (verify-pass C3 residual) — finish the
				// activation before ACKing, or the rolling lease bricks.
				// (cur comes from GetByID and carries billing_mode.)
				if h.billingRepo != nil && cur.BillingMode == models.BillingModeRolling {
					if consent, cerr := h.billingRepo.GetActiveConsent(r.Context(), cur.ID); cerr != nil {
						return false
					} else if consent != nil && consent.ActivatedAt == nil {
						pmID, _ := obj["payment_method"].(string)
						if pmID == "" {
							h.logger.Error("rolling consent: redelivery carries no payment_method", "lease_request_id", cur.ID)
							return false
						}
						brand, last4, fp := h.cardForPaymentMethod(pmID, cur.ID)
						if _, aerr := h.billingRepo.ActivateConsent(r.Context(), cur.ID, pmID, brand, last4, fp); aerr != nil {
							return false
						}
						h.logger.Info("rolling consent activated on redelivery", "lease_request_id", cur.ID)
					}
				}
				// Still broadcast in case the client missed the first one —
				// but no notifications/handover re-fire (they already did).
				h.broadcastLeaseUpdate(r.Context(), cur)
				return true
			case models.LeaseStatusExpired, models.LeaseStatusCancelled, models.LeaseStatusDeclined:
				h.logger.Warn("webhook: charge captured for a terminal lease — refunding",
					"lease_request_id", cur.ID, "status", cur.Status, "intent_id", intentID)
				h.refundOrphanedPayment(r.Context(), cur, payment)
				return true // reconciliation owns it from here; don't re-deliver
			default:
				h.logger.Error("webhook: charge captured for lease in unexpected state",
					"lease_request_id", cur.ID, "status", cur.Status, "intent_id", intentID)
				return true // orphan sweep's lister decides; nothing a retry fixes
			}
		}
		h.logger.Error("webhook: set lease paid", "lease_request_id", payment.LeaseRequestID, "error", err)
		return false
	}

	h.logger.Info("payment succeeded", "lease_request_id", lr.ID, "payment_id", payment.ID, "intent_id", intentID)

	// Rolling cycle 1: activate the consent with the saved payment method.
	// SetPaid's RETURNING now carries billing_mode (batch 3 carry-in #1),
	// so FIXED-TERM webhooks never touch the billing tables — a billing-
	// table outage cannot 500 the legacy payment path. The consent-
	// existence check remains inside the branch as defence-in-depth.
	if h.billingRepo != nil && lr.BillingMode == models.BillingModeRolling {
		if consent, cerr := h.billingRepo.GetActiveConsent(r.Context(), lr.ID); cerr != nil {
			h.logger.Error("rolling consent: lookup", "error", cerr, "lease_request_id", lr.ID)
			return false
		} else if consent != nil && consent.ActivatedAt == nil {
			pmID, _ := obj["payment_method"].(string)
			if pmID == "" {
				h.logger.Error("rolling consent: succeeded event carries no payment_method — refusing until redelivery", "lease_request_id", lr.ID)
				return false
			}
			brand, last4, fp := h.cardForPaymentMethod(pmID, lr.ID)
			if activated, aerr := h.billingRepo.ActivateConsent(r.Context(), lr.ID, pmID, brand, last4, fp); aerr != nil {
				h.logger.Error("rolling consent: activate", "error", aerr, "lease_request_id", lr.ID)
				return false
			} else if activated {
				h.logger.Info("rolling consent activated", "lease_request_id", lr.ID)
			}
		}
	}

	// Broadcast update to both parties
	resp := h.buildLeaseRequestResponse(r, lr, nil)
	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{lr.DriverID, lr.OwnerID},
	})

	// In-app notifications + push
	chatID := lr.ChatID
	leaseID := lr.ID
	driverName := resp.DriverName
	if driverName == "" {
		driverName = "The driver"
	}
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "your listing"
	}
	ownerBody := ownerPaidBody(driverName, carTitle, lr)
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
		"Payment received", ownerBody, &chatID, &leaseID)

	driverBody := fmt.Sprintf("Payment confirmed for %s — wait for pickup instructions from the owner", carTitle)
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Payment confirmed", driverBody, &chatID, &leaseID)

	// Create the key-handover task so both parties can coordinate the meetup.
	h.ensureKeyHandover(r, lr)

	// Arm the pickup deadline. Webhook retries are safe — the repo guard
	// (status='paid' AND pickup_deadline_at IS NULL) makes this idempotent.
	h.armPickupDeadline(r.Context(), lr)

	// The car is committed now — competing requests can no longer succeed
	// and must not sit in other drivers' Today lists until their TTL
	// lazily expires. Idempotent (a webhook retry matches zero rows).
	h.declineSiblingsOfPaidLease(r.Context(), lr)
	return true
}

// ensureKeyHandover creates the key-handover task for a freshly paid lease
// (idempotent on lease_request_id) and broadcasts it so both parties' Today
// tabs pick it up. Pickup location is snapshotted from the car listing.
func (h *LeaseRequestHandler) ensureKeyHandover(r *http.Request, lr *models.LeaseRequest) {
	h.ensureKeyHandoverCtx(r.Context(), lr)
}

func (h *LeaseRequestHandler) ensureKeyHandoverCtx(ctx context.Context, lr *models.LeaseRequest) {
	if h.keyHandoverRepo == nil {
		return
	}

	var lat, lng *float64
	var area *string
	if car, err := h.carRepo.GetByID(ctx, lr.ListingID); err == nil {
		if car.Latitude.Valid {
			v := car.Latitude.Float64
			lat = &v
		}
		if car.Longitude.Valid {
			v := car.Longitude.Float64
			lng = &v
		}
		if car.Area.Valid && car.Area.String != "" {
			v := car.Area.String
			area = &v
		}
	}

	kh, err := h.keyHandoverRepo.CreateForLease(ctx, lr, lat, lng, area)
	if err != nil {
		h.logger.Error("create key handover", "error", err, "lease_request_id", lr.ID)
		return
	}

	h.wsHub.Broadcast(&ws.Event{
		Type:          "key_handover_created",
		Payload:       map[string]any{"id": kh.ID, "lease_request_id": kh.LeaseRequestID, "status": kh.Status},
		TargetUserIDs: []uuid.UUID{lr.OwnerID, lr.DriverID},
	})
}

func (h *LeaseRequestHandler) handlePaymentFailed(r *http.Request, intentID string) {
	payment, err := h.leaseRepo.GetPaymentByIntentID(r.Context(), intentID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodePaymentNotFound {
			h.logger.Info("webhook: ignoring unknown payment_intent", "intent_id", intentID)
		} else {
			h.logger.Error("webhook: get payment by intent", "event", "failed", "intent_id", intentID, "error", err)
		}
		return
	}

	// Idempotency: already in terminal state
	if payment.Status == models.PaymentStatusFailed || payment.Status == models.PaymentStatusSucceeded {
		h.logger.Info("webhook: payment already terminal (idempotent skip)", "event", "failed", "intent_id", intentID, "status", payment.Status)
		return
	}

	if err := h.leaseRepo.UpdatePaymentStatus(r.Context(), payment.ID, models.PaymentStatusFailed); err != nil {
		h.logger.Error("webhook: update payment status", "event", "failed", "payment_id", payment.ID, "error", err)
	}

	h.logger.Info("payment failed", "lease_request_id", payment.LeaseRequestID, "payment_id", payment.ID, "intent_id", intentID)

	// Notify the driver so they can retry. We deliberately do NOT notify
	// the owner — failed payments are a driver-side recoverable state, and
	// owners only need to know about successful or canceled payments.
	if lr, lerr := h.leaseRepo.GetByID(r.Context(), payment.LeaseRequestID); lerr == nil && lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		carTitle := "your rental"
		if car, cerr := h.carRepo.GetByID(r.Context(), lr.ListingID); cerr == nil {
			carTitle = car.Title
		}
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"Payment failed",
			fmt.Sprintf("Your payment for %s didn't go through. Tap to try again.", carTitle),
			&chatID, &leaseID)
	}
}

func (h *LeaseRequestHandler) handlePaymentCanceled(r *http.Request, intentID string) {
	payment, err := h.leaseRepo.GetPaymentByIntentID(r.Context(), intentID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodePaymentNotFound {
			h.logger.Info("webhook: ignoring unknown payment_intent", "intent_id", intentID)
		} else {
			h.logger.Error("webhook: get payment by intent", "event", "canceled", "intent_id", intentID, "error", err)
		}
		return
	}

	// Idempotency: already in terminal state
	if payment.Status == models.PaymentStatusCanceled || payment.Status == models.PaymentStatusSucceeded {
		h.logger.Info("webhook: payment already terminal (idempotent skip)", "event", "canceled", "intent_id", intentID, "status", payment.Status)
		return
	}

	if err := h.leaseRepo.UpdatePaymentStatus(r.Context(), payment.ID, models.PaymentStatusCanceled); err != nil {
		h.logger.Error("webhook: update payment status", "event", "canceled", "payment_id", payment.ID, "error", err)
	}

	h.logger.Info("payment canceled", "lease_request_id", payment.LeaseRequestID, "payment_id", payment.ID, "intent_id", intentID)

	// Push the driver so a backgrounded PaymentSheet flow doesn't strand
	// them — they get a banner explaining the intent was cancelled and can
	// reopen the chat to choose a new course (re-pay, message the owner).
	if lr, lerr := h.leaseRepo.GetByID(r.Context(), payment.LeaseRequestID); lerr == nil && lr != nil {
		chatID := lr.ChatID
		leaseID := lr.ID
		carTitle := "your rental"
		if car, cerr := h.carRepo.GetByID(r.Context(), lr.ListingID); cerr == nil {
			carTitle = car.Title
		}
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"Payment cancelled",
			fmt.Sprintf("The payment for %s was cancelled. Open the chat to start again if you still want to rent.", carTitle),
			&chatID, &leaseID)
	}
}

// --- Pickup expiry scanner ---

// StartPickupExpiryScanner runs a background loop that polls for paid lease
// requests whose pickup deadline has elapsed without confirmation. For each
// match it atomically claims the row (UPDATE...RETURNING guarded by status +
// pickup_confirmed_at IS NULL), issues a Stripe refund with a stable
// idempotency key, persists the outcome, and broadcasts WS events so both
// parties' UIs flip immediately.
//
// Multi-instance safe: ClaimForExpiry is the serialization point — losers of
// the race see pgx.ErrNoRows and skip the row.
//
// Crash safe: the claim moves the row to status=expired_refunded with
// refund_status='pending' BEFORE the Stripe call. On restart the next tick
// will retry from FinalizeRefund (Stripe dedupes on the idempotency key, so
// no double refund). The car is unreserved by the claim itself, so listings
// return to discovery even if the Stripe call hangs.
func (h *LeaseRequestHandler) StartPickupExpiryScanner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	h.logger.Info("pickup expiry scanner started", "interval", interval.String(), "deadline", h.pickupDeadline.String())

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("pickup expiry scanner stopped")
			return
		case <-ticker.C:
			h.runExpirySweep(ctx)
			h.runPaymentPendingSweep(ctx)
			h.runAcceptExpirySweep(ctx)
			h.runOrphanedPaymentSweep(ctx)
			h.runBillingSweep(ctx)
		}
	}
}

// runAcceptExpirySweep enforces the accepted-lease TTL (client decision,
// Sep 4): warn both parties 24h before expiry (claimed-once), then expire
// at 72h and release the car. Same shape as runPaymentPendingSweep; the
// status-scoped claim is the race serializer against a driver who starts
// paying at the last minute.
func (h *LeaseRequestHandler) runAcceptExpirySweep(ctx context.Context) {
	now := time.Now().UTC()

	// Phase 1: the 24h warning.
	warnCutoff := now.Add(-(models.LeaseAcceptTTL - models.LeaseAcceptWarnBefore))
	warned, err := h.leaseRepo.ClaimAcceptExpiryWarnings(ctx, warnCutoff, 50)
	if err != nil {
		h.logger.Error("accept-expiry sweep: claim warnings", "error", err)
	}
	for i := range warned {
		lr := &warned[i]
		leaseRef := lr.ID
		chatRef := lr.ChatID
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
			"Complete your payment within 24 hours",
			"Your accepted rental request expires in about 24 hours if it isn't paid. Pay now to lock it in, or cancel to release the car.",
			&chatRef, &leaseRef)
		go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
			"Awaiting payment — 24 hours left",
			"The driver hasn't paid yet. If they don't pay within about 24 hours, the request expires and your car opens up to others automatically.",
			&chatRef, &leaseRef)
	}

	// Phase 2: expiry at the full TTL.
	expireCutoff := now.Add(-models.LeaseAcceptTTL)
	candidates, err := h.leaseRepo.ListAcceptExpired(ctx, expireCutoff, 50)
	if err != nil {
		h.logger.Error("accept-expiry sweep: list", "error", err)
		return
	}
	for i := range candidates {
		lr := &candidates[i]
		// A stale PI should not exist at 'accepted' (it's created on the
		// payment_pending transition), but a crash window can leave one.
		// The claim proceeds ONLY on a proven-neutralized intent: a winning
		// payment is adopted, an unverifiable one defers to the next tick
		// (the old Warn-and-continue fall-through was M3b).
		payment, perr := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, lr.ID)
		if perr != nil {
			h.logger.Error("accept-expiry sweep: payment lookup", "error", perr, "lease_request_id", lr.ID)
			continue
		}
		if payment != nil && payment.PaymentIntentID != nil {
			switch h.neutralizePaymentIntent(*payment.PaymentIntentID) {
			case piMoneyMoved:
				h.logger.Info("accept-expiry sweep: payment won — adopting", "lease_request_id", lr.ID)
				h.adoptSucceededPayment(ctx, payment)
				continue
			case piUnknown:
				h.logger.Warn("accept-expiry sweep: intent state unknown, deferring", "lease_request_id", lr.ID)
				continue
			}
		}
		claimed, cerr := h.leaseRepo.ClaimAcceptExpiry(ctx, lr.ID)
		if cerr != nil {
			continue // state moved (payment started / explicit exit) — correct
		}
		h.logger.Info("accept-expiry sweep: lease expired, car released",
			"lease_request_id", claimed.ID, "car_id", claimed.ListingID)

		resp := h.buildLeaseRequestResponseCtx(ctx, claimed, nil)
		h.wsHub.Broadcast(&ws.Event{
			Type:          "lease_request_updated",
			Payload:       resp,
			TargetUserIDs: []uuid.UUID{claimed.DriverID, claimed.OwnerID},
		})
		leaseRef := claimed.ID
		chatRef := claimed.ChatID
		go h.notifHandler.Notify(claimed.DriverID, models.NotificationTypeLeaseRequest,
			"Request expired",
			"Your accepted request wasn't paid within 72 hours, so it expired. The car may still be available — you can send a new request anytime.",
			&chatRef, &leaseRef)
		go h.notifHandler.Notify(claimed.OwnerID, models.NotificationTypeLeaseRequest,
			"Request expired — car released",
			"An accepted request went unpaid for 72 hours, so it expired and your car is available to others again.",
			&chatRef, &leaseRef)
	}
}

// runPaymentPendingSweep expires leases whose payment window (item 4:
// LeasePaymentPendingTTL from the payment_pending_at stamp) lapsed without a
// successful charge, releasing the car's reservation. Race-safe by the
// house pattern: neutralize the PI first (a succeeded/processing intent
// refuses cancellation → skip, the webhook wins), then the status-scoped
// ClaimPaymentExpiry UPDATE is the serializer.
func (h *LeaseRequestHandler) runPaymentPendingSweep(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-models.LeasePaymentPendingTTL)
	candidates, err := h.leaseRepo.ListPaymentPendingExpired(ctx, cutoff, 50)
	if err != nil {
		h.logger.Error("payment-pending sweep: list", "error", err)
		return
	}
	for i := range candidates {
		lr := &candidates[i]
		payment, perr := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, lr.ID)
		if perr != nil {
			// Can't see the payment → can't prove no money moved → the row
			// waits for the next tick (the old skip-the-neutralize-and-
			// claim-anyway path was M3b).
			h.logger.Error("payment-pending sweep: payment lookup", "error", perr, "lease_request_id", lr.ID)
			continue
		}
		if payment != nil && payment.PaymentIntentID != nil {
			switch h.neutralizePaymentIntent(*payment.PaymentIntentID) {
			case piMoneyMoved:
				// The payment won at the wire. Don't just leave it for a
				// webhook that may never come (H2) — adopt it now.
				h.logger.Info("payment-pending sweep: payment won — adopting", "lease_request_id", lr.ID)
				h.adoptSucceededPayment(ctx, payment)
				continue
			case piUnknown:
				h.logger.Warn("payment-pending sweep: intent state unknown, deferring", "lease_request_id", lr.ID)
				continue
			}
		}
		claimed, cerr := h.leaseRepo.ClaimPaymentExpiry(ctx, lr.ID)
		if cerr != nil {
			// Zero rows = someone else moved the state (webhook or an
			// explicit exit) — correct outcome, not an error.
			continue
		}
		h.logger.Info("payment-pending sweep: lease expired, car released",
			"lease_request_id", claimed.ID, "car_id", claimed.ListingID)

		resp := h.buildLeaseRequestResponseCtx(ctx, claimed, nil)
		h.wsHub.Broadcast(&ws.Event{
			Type:          "lease_request_updated",
			Payload:       resp,
			TargetUserIDs: []uuid.UUID{claimed.DriverID, claimed.OwnerID},
		})
		leaseRef := claimed.ID
		chatRef := claimed.ChatID
		go h.notifHandler.Notify(claimed.DriverID, models.NotificationTypeLeaseRequest,
			"Request expired",
			"The payment window for your rental request closed, so the request expired. The car may still be available — you can send a new request anytime.",
			&chatRef, &leaseRef)
		go h.notifHandler.Notify(claimed.OwnerID, models.NotificationTypeLeaseRequest,
			"Request expired — car released",
			"A driver's payment window closed without payment, so their request expired and your car is available to others again.",
			&chatRef, &leaseRef)
	}
}

// stuckRefundStaleAfter is the minimum age before a row claimed for expiry
// (status=expired_refunded, refund_id=NULL) is considered "stuck" and replayed.
// 2 minutes gives a worker on its slow ticker a chance to finish without us
// stepping on it, while still surfacing a real outage within the next sweep.
const stuckRefundStaleAfter = 2 * time.Minute

func (h *LeaseRequestHandler) runExpirySweep(ctx context.Context) {
	now := time.Now().UTC()

	// Phase 1: claim freshly-expired leases (status='paid' AND deadline <= now).
	candidates, err := h.leaseRepo.ListExpiredAwaitingPickup(ctx, now, 50)
	if err != nil {
		h.logger.Error("expiry sweep: list", "error", err)
	} else if len(candidates) > 0 {
		h.logger.Info("expiry sweep: candidates", "count", len(candidates))
		for _, c := range candidates {
			h.processExpiredLease(ctx, c.ID)
		}
	}

	// Phase 2: retry stuck refunds — leases the worker already claimed but
	// whose Stripe refund never persisted (process crash / Stripe 5xx /
	// transient DB error between CreateRefund and FinalizeRefund). The
	// stable idempotency key (`refund-<leaseID>`) makes the replay safe —
	// Stripe returns the same Refund object instead of issuing a second
	// charge-back. Without this phase a single mid-flight crash would
	// silently dangle a refund forever (status='expired_refunded' but
	// refund_id IS NULL is invisible to ListExpiredAwaitingPickup).
	stuckBefore := now.Add(-stuckRefundStaleAfter)
	stuck, err := h.leaseRepo.ListStuckRefunds(ctx, stuckBefore, 50)
	if err != nil {
		h.logger.Error("expiry sweep: list stuck refunds", "error", err)
		return
	}
	if len(stuck) > 0 {
		h.logger.Info("expiry sweep: stuck refund candidates", "count", len(stuck))
		for i := range stuck {
			h.retryStuckRefund(ctx, &stuck[i])
		}
	}
}

func (h *LeaseRequestHandler) processExpiredLease(ctx context.Context, leaseID uuid.UUID) {
	// Step 1: atomically claim the row. Losers (concurrent worker / already-
	// confirmed driver / status moved on) get ErrNoRows and we skip.
	lr, err := h.leaseRepo.ClaimForExpiry(ctx, leaseID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		h.logger.Error("expiry: claim", "error", err, "lease_request_id", leaseID)
		return
	}

	h.logger.Info("expiry: claimed", "lease_request_id", lr.ID)

	// A no-show expiry ends any rolling consent with the lease (batch 3):
	// no future charge may ever ride a consent whose rental never started.
	// Claimed-once in the repo; fixed-term leases have no consent row, so
	// this matches zero rows and is a no-op. (ClaimForExpiry's RETURNING
	// doesn't carry billing_mode, hence no mode gate here.)
	if h.billingRepo != nil {
		if _, rerr := h.billingRepo.RevokeConsent(ctx, lr.ID, "pickup_no_show"); rerr != nil {
			h.logger.Warn("expiry: revoke rolling consent", "error", rerr, "lease_request_id", lr.ID)
		}
	}

	// Step 2: broadcast the cancel + notify so the UI flips immediately,
	// regardless of the Stripe call latency.
	h.broadcastLeaseUpdate(ctx, lr)
	h.notifyExpiry(ctx, lr)

	// Step 3: issue + finalize the refund (shared with the stuck-retry path).
	h.issueAndFinalizeRefund(ctx, lr, "expiry")
}

// OwnerReleaseStalePickup — POST /api/v1/lease-requests/{id}/owner-release
//
// The owner's exit from a car held hostage by a rental that never started.
//
// The pickup-expiry scanner can only see leases whose pickup_deadline_at was
// armed at payment time; a paid lease with a NULL deadline is invisible to it
// forever, and its car stays reserved — out of discovery, un-rentable,
// un-sellable, with no button anywhere that frees it. One such car sat that
// way for nearly six months.
//
// This does exactly what the scanner would have done: the same claim, the
// same terminal state, the same full refund to the driver who paid for a
// rental they never received. The only difference is who noticed.
func (h *LeaseRequestHandler) OwnerReleaseStalePickup(w http.ResponseWriter, r *http.Request) {
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

	lr, err := h.leaseRepo.ClaimForOwnerRelease(r.Context(), leaseID, userID, models.LeaseOwnerReleaseMinAge)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not releasable. Say which of the three reasons it is, rather than
		// a bare 409 the owner cannot act on.
		existing, gerr := h.leaseRepo.GetByID(r.Context(), leaseID)
		apiErr := models.NewAPIError("RELEASE_NOT_ALLOWED",
			"This rental can't be released. It may have already started, already ended, or be too recent to release yet.")
		if gerr == nil && existing != nil {
			switch {
			case existing.OwnerID != userID:
				httputil.WriteError(w, http.StatusForbidden,
					models.NewAPIError("RELEASE_NOT_ALLOWED", "Only the car's owner can release it."))
				return
			case existing.PickupConfirmedAt != nil:
				apiErr.Message = "The driver already picked this car up, so the rental is live. Use the return flow to end it."
			case existing.Status != models.LeaseStatusPaid:
				apiErr.Message = "This rental isn't in a state that holds your car — nothing to release."
			default:
				apiErr.Message = "This rental is too recent to release. If the driver doesn't collect the car, it frees up automatically."
			}
			apiErr.Details = map[string]interface{}{"status": string(existing.Status)}
		}
		httputil.WriteError(w, http.StatusConflict, apiErr)
		return
	}
	if err != nil {
		h.logger.Error("owner release: claim", "error", err, "lease_request_id", leaseID, "owner_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	h.logger.Info("owner release: claimed a stale pickup hold",
		"lease_request_id", lr.ID, "owner_id", userID, "car_id", lr.ListingID,
		"lease_age_hours", time.Since(lr.CreatedAt).Hours(),
		"deadline_was_armed", lr.PickupDeadlineAt != nil)

	// Identical tail to the scanner's: revoke any rolling consent, tell both
	// parties, refund the driver in full.
	// Detached from the request: the claim has committed and the car is
	// already free, so the driver's money must not depend on the owner's
	// phone keeping the connection open long enough.
	bg := context.WithoutCancel(r.Context())
	if h.billingRepo != nil {
		if _, rerr := h.billingRepo.RevokeConsent(bg, lr.ID, "pickup_no_show"); rerr != nil {
			h.logger.Warn("owner release: revoke rolling consent", "error", rerr, "lease_request_id", lr.ID)
		}
	}
	h.broadcastLeaseUpdate(bg, lr)
	h.notifyOwnerRelease(bg, lr, h.refundLooksPayable(bg, lr))
	h.issueAndFinalizeRefund(bg, lr, "owner-release")

	fresh, _ := h.leaseRepo.GetByID(r.Context(), lr.ID)
	if fresh == nil {
		fresh = lr
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildLeaseRequestResponse(r, fresh, nil))
}

// ListMyStalePickupHolds — GET /api/v1/me/stale-car-holds
//
// Every car of MINE whose reservation is held by a rental that never started.
// Exists so this state is visible in the product instead of being discovered
// when an Accept mysteriously fails.
func (h *LeaseRequestHandler) ListMyStalePickupHolds(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	// Scoped in SQL, not by a loop afterwards: a filter that lives in Go is
	// one refactor away from returning every owner's cars to any caller, and
	// a global LIMIT would hide this owner's only exit behind other people's
	// older rows.
	mine, err := h.leaseRepo.ListStalePickupHolds(r.Context(), &userID, models.LeaseOwnerReleaseMinAge, 100)
	if err != nil {
		h.logger.Error("stale car holds: list", "error", err, "owner_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if mine == nil {
		mine = []models.StalePickupHold{}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"holds": mine})
}

// AdminListStalePickupHolds — GET /api/v1/admin/stale-car-holds
func (h *LeaseRequestHandler) AdminListStalePickupHolds(w http.ResponseWriter, r *http.Request) {
	holds, err := h.leaseRepo.ListStalePickupHolds(r.Context(), nil, models.LeaseOwnerReleaseMinAge, 200)
	if err != nil {
		h.logger.Error("admin stale car holds: list", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"holds": holds})
}

// AdminReleaseStalePickup — POST /api/v1/admin/lease-requests/{id}/release
//
// The same release, performed by support on an owner's behalf. It exists
// because the owner-facing button needs an app release to reach a phone,
// and a car should not stay stranded waiting for one.
func (h *LeaseRequestHandler) AdminReleaseStalePickup(w http.ResponseWriter, r *http.Request) {
	adminID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	leaseID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid lease request ID"))
		return
	}
	existing, gerr := h.leaseRepo.GetByID(r.Context(), leaseID)
	if gerr != nil || existing == nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
		return
	}
	lr, err := h.leaseRepo.ClaimForOwnerRelease(r.Context(), leaseID, existing.OwnerID, models.LeaseOwnerReleaseMinAge)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("RELEASE_NOT_ALLOWED",
			"This lease is not a releasable stale pickup hold (already started, already ended, or too recent)."))
		return
	}
	if err != nil {
		h.logger.Error("admin release: claim", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	h.logger.Info("admin release: claimed a stale pickup hold",
		"lease_request_id", lr.ID, "admin_id", adminID, "owner_id", lr.OwnerID, "car_id", lr.ListingID)
	bg := context.WithoutCancel(r.Context())
	if h.billingRepo != nil {
		_, _ = h.billingRepo.RevokeConsent(bg, lr.ID, "pickup_no_show")
	}
	h.broadcastLeaseUpdate(bg, lr)
	h.notifyOwnerRelease(bg, lr, h.refundLooksPayable(bg, lr))
	h.issueAndFinalizeRefund(bg, lr, "admin-release")

	fresh, _ := h.leaseRepo.GetByID(r.Context(), lr.ID)
	if fresh == nil {
		fresh = lr
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildLeaseRequestResponse(r, fresh, nil))
}

// retryStuckRefund is the recovery path for leases ClaimForExpiry already
// moved to status=expired_refunded but whose Stripe refund never persisted
// (worker crashed mid-call, Stripe returned 5xx, etc.). We do NOT re-broadcast
// the lease state — the original processExpiredLease already did that. We
// also do not re-notify (the driver was already told the deadline elapsed).
// All that remains is to make Stripe agree; the idempotency key in
// issueAndFinalizeRefund makes the replay safe.
func (h *LeaseRequestHandler) retryStuckRefund(ctx context.Context, lr *models.LeaseRequest) {
	h.logger.Info("expiry: refund retry claimed",
		"lease_request_id", lr.ID,
		"previous_refund_status", strOrEmpty(lr.RefundStatus),
		"row_age_seconds", time.Since(lr.UpdatedAt).Seconds())
	h.issueAndFinalizeRefund(ctx, lr, "retry")
}

// issueAndFinalizeRefund is the shared body for the first-attempt and the
// retry path. It loads the linked PaymentIntent, calls Stripe with the
// stable `refund-<leaseID>` idempotency key, persists the outcome, and
// re-broadcasts so the UI flips when the refund actually lands.
//
// Safety notes:
//   - Stripe dedupes on the idempotency key, so a replay returns the same
//     Refund object instead of double-charging.
//   - If the payment row has no PaymentIntentID we cannot refund — we mark
//     refund_status=failed so an operator can intervene. The retry sweep
//     will pick the row up again on a future tick (refund_status='failed'
//     is included in ListStuckRefunds).
//   - phase is "expiry" on first attempt, "retry" on later attempts; it's
//     used only for log prefixes so the two paths are distinguishable in
//     production logs.
func (h *LeaseRequestHandler) issueAndFinalizeRefund(ctx context.Context, lr *models.LeaseRequest, phase string) {
	payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, lr.ID)
	if err != nil {
		// Transient DB error — stay 'pending'/'failed' so the sweep retries.
		h.logger.Error("expiry: payment lookup failed", "phase", phase, "error", err, "lease_request_id", lr.ID)
		if ferr := h.leaseRepo.FinalizeRefund(ctx, lr.ID, "", models.RefundStatusFailed); ferr != nil {
			h.logger.Error("expiry: finalize refund (lookup failed)", "phase", phase, "error", ferr, "lease_request_id", lr.ID)
		}
		return
	}
	if payment == nil || payment.PaymentIntentID == nil {
		// PERMANENT: there is nothing to refund against, and no retry will
		// change that. The old path marked 'failed' and the sweep replayed
		// the same dead end every ~60s forever with no ticket (H7).
		h.markLeaseRefundUnrecoverable(ctx, lr, lr.TotalAmountCents(), "no payment intent recorded for this lease")
		return
	}

	// Never promise money back on a charge too old to refund. A charge past
	// this age is typically on a Stripe account we no longer run on, and the
	// old behaviour was to call anyway, fail, and either retry forever or
	// tell the driver a refund was coming that never could.
	if age := time.Since(payment.CreatedAt); age > models.LeaseRefundMaxAge {
		h.markLeaseRefundUnrecoverable(ctx, lr, payment.Amount,
			fmt.Sprintf("charge is %.0f days old — beyond the refundable window; refund by hand if it is still owed", age.Hours()/24))
		return
	}

	idemKey := fmt.Sprintf("refund-%s", lr.ID.String())
	// amountCents=0 → full refund (pickup expiry reverses the entire payment).
	refund, err := h.stripe.CreateRefund(*payment.PaymentIntentID, idemKey, "requested_by_customer", 0)
	if err != nil {
		// Permanent Stripe refusals: no retry can change them, so they exit
		// to a human instead of riding the stuck-refund sweep forever.
		// charge_already_refunded is the one support creates by refunding
		// from the dashboard first, which is the natural thing to do.
		if strings.Contains(err.Error(), "resource_missing") ||
			strings.Contains(err.Error(), "charge_already_refunded") {
			// PERMANENT: the intent no longer exists at Stripe — the exact
			// failure the vehicle-return side already parks as
			// 'unrecoverable' (000051). Same exit here.
			h.markLeaseRefundUnrecoverable(ctx, lr, payment.Amount, "payment intent no longer exists at Stripe: "+err.Error())
			return
		}
		h.logger.Error("expiry: refund retry failed",
			"phase", phase,
			"error", err,
			"lease_request_id", lr.ID,
			"intent_id", *payment.PaymentIntentID,
		)
		if ferr := h.leaseRepo.FinalizeRefund(ctx, lr.ID, "", models.RefundStatusFailed); ferr != nil {
			h.logger.Error("expiry: finalize refund (stripe failed)", "phase", phase, "error", ferr, "lease_request_id", lr.ID)
		}
		return
	}

	status := models.RefundStatusFailed
	switch refund.Status {
	case "succeeded", "pending":
		status = models.RefundStatusSucceeded
	}
	if ferr := h.leaseRepo.FinalizeRefund(ctx, lr.ID, refund.ID, status); ferr != nil {
		h.logger.Error("expiry: finalize refund", "phase", phase, "error", ferr, "lease_request_id", lr.ID, "refund_id", refund.ID)
		return
	}

	h.logger.Info("expiry: refund completed",
		"phase", phase,
		"lease_request_id", lr.ID,
		"refund_id", refund.ID,
		"stripe_status", refund.Status,
		"persisted_status", status)

	// H7: the "your money is back" message fires only now, on the actual
	// refund — notifyExpiry at claim time promises it, never asserts it.
	if status == models.RefundStatusSucceeded {
		chatID := lr.ChatID
		leaseID := lr.ID
		go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
			"Refund issued",
			fmt.Sprintf("Your refund of $%.2f is on its way back to your card.", float64(payment.Amount)/100),
			&chatID, &leaseID)
	}

	// Re-broadcast with the now-populated refund fields.
	if updated, err := h.leaseRepo.GetByID(ctx, lr.ID); err == nil && updated != nil {
		h.broadcastLeaseUpdate(ctx, updated)
	}
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// broadcastLeaseUpdate sends a lease_request_updated WS event to both
// participants. Uses a background request-less context — no http.Request is
// available from inside the worker.
func (h *LeaseRequestHandler) broadcastLeaseUpdate(ctx context.Context, lr *models.LeaseRequest) {
	resp := h.buildLeaseRequestResponseCtx(ctx, lr, nil)
	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{lr.DriverID, lr.OwnerID},
	})
}

// notifyExpiry sends in-app + push notifications to both parties announcing
// that the pickup deadline elapsed and the rental was refunded.
func (h *LeaseRequestHandler) notifyExpiry(ctx context.Context, lr *models.LeaseRequest) {
	carTitle := "the car"
	if car, err := h.carRepo.GetByID(ctx, lr.ListingID); err == nil {
		carTitle = car.Title
	}
	driverName := "The driver"
	if d, err := h.userRepo.GetByID(ctx, lr.DriverID); err == nil {
		driverName = d.FullName()
	}

	chatID := lr.ChatID
	lrID := lr.ID

	// H7: promised, not asserted — the refund hasn't been issued yet at
	// claim time. The "Refund issued" notice fires from
	// issueAndFinalizeRefund once Stripe actually accepts it.
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Pickup deadline missed",
		fmt.Sprintf("You didn't confirm pickup of %s in time. The rental was cancelled — your payment is being refunded to your card, and we'll confirm as soon as it's done.", carTitle),
		&chatID, &lrID)
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
		"Pickup deadline missed",
		fmt.Sprintf("%s didn't pick up %s in time. The rental was cancelled, the driver's payment is being refunded, and your listing is back on the market.", driverName, carTitle),
		&chatID, &lrID)
}

// UpdateOfferedPrice handles PATCH /api/v1/lease-requests/{id}/price
// Allows the owner to set a custom weekly price before accepting the request.
func (h *LeaseRequestHandler) UpdateOfferedPrice(w http.ResponseWriter, r *http.Request) {
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

	var body models.UpdateOfferedPriceBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	if body.OfferedWeeklyPrice < 1.0 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Offered weekly price must be at least 1.00"))
		return
	}

	updated, staleIntentID, err := h.leaseRepo.UpdateOfferedPrice(r.Context(), leaseID, userID, body.OfferedWeeklyPrice)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			if apiErr.Code == models.ErrCodeLeaseRequestNotFound {
				status = http.StatusNotFound
			} else if apiErr.Code == models.ErrCodePriceLocked {
				status = http.StatusConflict
			}
			httputil.WriteError(w, status, apiErr)
		} else {
			h.logger.Error("update offered price", "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	// Best-effort: cancel the stale Stripe PaymentIntent so the driver's
	// saved PaymentSheet can't submit the OLD amount. The driver will be
	// issued a fresh intent the next time they tap Pay Now (after
	// accepting the new price). Failures here are logged but not fatal —
	// the backend payment-gate (LeaseRequest.PriceChangePending) is the
	// authoritative defence; the Stripe cancel is the belt to the gate's
	// suspenders.
	if staleIntentID != "" {
		if cancelErr := h.stripe.CancelPaymentIntent(staleIntentID); cancelErr != nil {
			h.logger.Warn("price change: cancel stale PaymentIntent failed",
				"error", cancelErr, "lease_request_id", leaseID, "intent_id", staleIntentID)
		} else {
			// Mark the local payment row as canceled so subsequent
			// retries don't try to reuse the dead client_secret.
			if p, perr := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID); perr == nil && p != nil {
				_ = h.leaseRepo.UpdatePaymentStatus(r.Context(), p.ID, models.PaymentStatusCanceled)
			}
		}
	}

	resp := h.buildLeaseRequestResponse(r, updated, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{updated.DriverID, updated.OwnerID},
	})

	// In-app + push for driver — Pay Now is now hidden until they review.
	chatID := updated.ChatID
	lrID := updated.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "the car"
	}
	go h.notifHandler.Notify(updated.DriverID, models.NotificationTypeLeaseRequest,
		"Price updated",
		fmt.Sprintf("The owner changed the price for %s — review before paying.", carTitle),
		&chatID, &lrID)
}

// AcceptPriceChange handles POST /api/v1/lease-requests/{id}/accept-price.
// Driver-only path: clears the price-review flag so Pay Now becomes
// available again. Sends a gray "Driver accepted the new price" system
// message and notifies the owner.
func (h *LeaseRequestHandler) AcceptPriceChange(w http.ResponseWriter, r *http.Request) {
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

	updated, err := h.leaseRepo.AcceptPriceChange(r.Context(), leaseID, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			switch apiErr.Code {
			case models.ErrCodeLeaseRequestNotFound:
				status = http.StatusNotFound
			case models.ErrCodeNoPriceChangePending:
				status = http.StatusConflict
			case models.ErrCodeInvalidLeaseAction:
				status = http.StatusForbidden
			}
			httputil.WriteError(w, status, apiErr)
			return
		}
		h.logger.Error("accept price change", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildLeaseRequestResponse(r, updated, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{updated.DriverID, updated.OwnerID},
	})

	chatID := updated.ChatID
	lrID := updated.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "your listing"
	}
	go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypeLeaseRequest,
		"Driver accepted the new price",
		fmt.Sprintf("The driver accepted your updated price for %s. Waiting on payment.", carTitle),
		&chatID, &lrID)
}

// DeclinePriceChange handles POST /api/v1/lease-requests/{id}/decline-price.
// Driver-only path: cancels the lease, unreserves the car (handled inside
// the repo transaction), and best-effort cancels any stale Stripe
// PaymentIntent. Sends a "Driver declined the new price" system message
// and notifies the owner.
func (h *LeaseRequestHandler) DeclinePriceChange(w http.ResponseWriter, r *http.Request) {
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

	updated, staleIntentID, err := h.leaseRepo.DeclinePriceChange(r.Context(), leaseID, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			switch apiErr.Code {
			case models.ErrCodeLeaseRequestNotFound:
				status = http.StatusNotFound
			case models.ErrCodeNoPriceChangePending:
				status = http.StatusConflict
			case models.ErrCodeInvalidLeaseAction:
				status = http.StatusForbidden
			}
			httputil.WriteError(w, status, apiErr)
			return
		}
		h.logger.Error("decline price change", "error", err, "lease_request_id", leaseID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Belt + suspenders: kill the Stripe PaymentIntent the driver could
	// otherwise still submit from a backgrounded PaymentSheet.
	if staleIntentID != "" {
		if cancelErr := h.stripe.CancelPaymentIntent(staleIntentID); cancelErr != nil {
			h.logger.Warn("decline price: cancel stale PaymentIntent failed",
				"error", cancelErr, "lease_request_id", leaseID, "intent_id", staleIntentID)
		} else {
			if p, perr := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID); perr == nil && p != nil {
				_ = h.leaseRepo.UpdatePaymentStatus(r.Context(), p.ID, models.PaymentStatusCanceled)
			}
		}
	}

	resp := h.buildLeaseRequestResponse(r, updated, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{updated.DriverID, updated.OwnerID},
	})

	chatID := updated.ChatID
	lrID := updated.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "your listing"
	}
	go h.notifHandler.Notify(updated.OwnerID, models.NotificationTypeLeaseRequest,
		"Driver declined the new price",
		fmt.Sprintf("The driver declined your updated price for %s. The rental was cancelled and your car is back on the market.", carTitle),
		&chatID, &lrID)
}

// --- Pickup deadline / confirmation ---

// armPickupDeadline persists the deadline computed from h.pickupDeadline. The
// repo call is guarded (status='paid' AND pickup_deadline_at IS NULL) so it's
// safe to invoke from both the webhook and the polling path even when both
// fire for the same lease — only the first one wins, subsequent calls are
// silent no-ops. Failure here is logged but never propagated: a missing
// deadline only delays cleanup until the next ticker run picks the row up.
func (h *LeaseRequestHandler) armPickupDeadline(ctx context.Context, lr *models.LeaseRequest) {
	if lr == nil || h.pickupDeadline <= 0 {
		return
	}
	if lr.Status != models.LeaseStatusPaid {
		return
	}
	if lr.PickupDeadlineAt != nil {
		return
	}
	deadline := time.Now().UTC().Add(h.pickupDeadline)
	if err := h.leaseRepo.SetPickupDeadline(ctx, lr.ID, deadline); err != nil {
		h.logger.Error("arm pickup deadline", "error", err, "lease_request_id", lr.ID)
		return
	}
	lr.PickupDeadlineAt = &deadline
}

// ConfirmPickup handles POST /api/v1/lease-requests/{id}/pickup-confirm.
// Driver-only. Marks the rental as picked up so the expiry scanner stops
// considering it. Idempotent: calling twice returns the same confirmation
// timestamp. Returns 409 with PICKUP_DEADLINE_PASSED if the worker already
// claimed this lease for refund.
func (h *LeaseRequestHandler) ConfirmPickup(w http.ResponseWriter, r *http.Request) {
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

	lr, err := h.leaseRepo.ConfirmPickup(r.Context(), leaseID, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			switch apiErr.Code {
			case models.ErrCodeLeaseRequestNotFound:
				status = http.StatusNotFound
			case "FORBIDDEN":
				status = http.StatusForbidden
			case models.ErrCodeInvalidLeaseAction:
				status = http.StatusConflict
			case "PICKUP_DEADLINE_PASSED":
				status = http.StatusConflict
			}
			httputil.WriteError(w, status, apiErr)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
			return
		}
		h.logger.Error("confirm pickup", "error", err, "lease_request_id", leaseID, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildLeaseRequestResponse(r, lr, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{lr.DriverID, lr.OwnerID},
	})

	// The pickup confirm also flipped cars.status → 'rented' inside the
	// repo transaction. Broadcast car_updated so open owner clients refresh
	// the My Cars grid (chip flips green → orange, "Rented to ..." block
	// appears) without a manual pull-to-refresh.
	h.wsHub.Broadcast(&ws.Event{
		Type: "car_updated",
		Payload: map[string]any{
			"id":     lr.ListingID,
			"status": models.CarStatusRented,
		},
		TargetUserIDs: []uuid.UUID{lr.OwnerID, lr.DriverID},
	})

	chatID := lr.ChatID
	lrID := lr.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "the car"
	}
	driverName := resp.DriverName
	if driverName == "" {
		driverName = "The driver"
	}
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
		"Pickup confirmed",
		fmt.Sprintf("%s confirmed pickup of %s — the rental is now active.", driverName, carTitle),
		&chatID, &lrID)
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
		"Pickup confirmed",
		fmt.Sprintf("You confirmed pickup of %s. Have a great rental!", carTitle),
		&chatID, &lrID)
}

// ExtendPickupDeadline handles POST /api/v1/lease-requests/{id}/pickup-deadline/extend.
// Owner-only. Adds 15/30/60 minutes to pickup_deadline_at via a single
// guarded UPDATE — the same predicate the expiry scanner uses to claim the
// row, so the two never both succeed for the same lease. Total minutes
// added across all extensions is capped at PickupMaxExtensionMinutes
// (enforced inline by the UPDATE plus a DB CHECK in migration 000025).
func (h *LeaseRequestHandler) ExtendPickupDeadline(w http.ResponseWriter, r *http.Request) {
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

	var body models.ExtendPickupDeadlineBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if !models.IsAllowedPickupExtensionMinutes(body.Minutes) {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrInvalidExtensionMin)
		return
	}

	lr, err := h.leaseRepo.ExtendPickupDeadline(r.Context(), leaseID, userID, body.Minutes)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			status := http.StatusBadRequest
			switch apiErr.Code {
			case models.ErrCodeLeaseRequestNotFound:
				status = http.StatusNotFound
			case models.ErrCodeInvalidLeaseAction:
				// "Only the owner can extend" → 403; other invalid actions → 409.
				if apiErr.Message == "Only the car owner can extend the pickup deadline" {
					status = http.StatusForbidden
				} else {
					status = http.StatusConflict
				}
			case "PICKUP_DEADLINE_PASSED", "PICKUP_EXTENSION_CAP_REACHED":
				status = http.StatusConflict
			case "INVALID_EXTENSION_MINUTES":
				status = http.StatusBadRequest
			}
			httputil.WriteError(w, status, apiErr)
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteError(w, http.StatusNotFound, models.ErrLeaseRequestNotFound)
			return
		}
		h.logger.Error("extend pickup deadline", "error", err, "lease_request_id", leaseID, "user_id", userID, "minutes", body.Minutes)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildLeaseRequestResponse(r, lr, nil)
	httputil.WriteJSON(w, http.StatusOK, resp)

	h.wsHub.Broadcast(&ws.Event{
		Type:          "lease_request_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{lr.DriverID, lr.OwnerID},
	})

	chatID := lr.ChatID
	lrID := lr.ID
	carTitle := resp.CarTitle
	if carTitle == "" {
		carTitle = "the car"
	}
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypeLeaseRequest,
		"Pickup deadline extended",
		fmt.Sprintf("The owner added %d minutes to your pickup deadline for %s.", body.Minutes, carTitle),
		&chatID, &lrID)
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
		"Pickup deadline extended",
		fmt.Sprintf("You added %d minutes to the pickup deadline for %s.", body.Minutes, carTitle),
		&chatID, &lrID)

	h.logger.Info("pickup deadline extended",
		"lease_request_id", lr.ID,
		"by_minutes", body.Minutes,
		"total_extended_minutes", lr.PickupExtensionTotalMinutes,
		"new_deadline", lr.PickupDeadlineAt,
	)
}

// --- Helpers ---

func (h *LeaseRequestHandler) buildLeaseRequestResponse(r *http.Request, lr *models.LeaseRequest, payment *models.Payment) models.LeaseRequestResponse {
	return h.buildLeaseRequestResponseCtx(r.Context(), lr, payment)
}

// buildLeaseRequestResponseCtx is the request-less variant used by background
// workers (expiry scanner) that have no http.Request to pass through.
func (h *LeaseRequestHandler) buildLeaseRequestResponseCtx(ctx context.Context, lr *models.LeaseRequest, payment *models.Payment) models.LeaseRequestResponse {
	resp := models.LeaseRequestResponse{
		ID:                          lr.ID,
		ChatID:                      lr.ChatID,
		ListingID:                   lr.ListingID,
		OwnerID:                     lr.OwnerID,
		DriverID:                    lr.DriverID,
		Status:                      lr.Status,
		WeeklyPrice:                 lr.WeeklyPrice,
		OfferedWeeklyPrice:          lr.OfferedWeeklyPrice,
		TotalAmount:                 leaseQuoteAmount(lr),
		IntervalAmountCents:         leaseIntervalCents(lr),
		Currency:                    lr.Currency,
		Weeks:                       lr.Weeks,
		Message:                     lr.Message,
		ExpiresAt:                   models.RFC3339Time(lr.ExpiresAt),
		CreatedAt:                   models.RFC3339Time(lr.CreatedAt),
		UpdatedAt:                   models.RFC3339Time(lr.UpdatedAt),
		RefundID:                    lr.RefundID,
		RefundStatus:                lr.RefundStatus,
		PickupExtensionTotalMinutes: lr.PickupExtensionTotalMinutes,
		PickupExtensionCount:        lr.PickupExtensionCount,
		PickupExtensionRemainingMin: lr.RemainingExtensionMinutes(),
		PriceChangePending:          lr.PriceChangePending,
		PreviousOfferedWeeklyPrice:  lr.PreviousOfferedWeeklyPrice,
		BillingMode:                 lr.BillingMode,
		BillingInterval:             lr.BillingInterval,
		RenewalHaltedReason:         lr.RenewalHaltedReason,
	}
	if lr.RentalEndsAt != nil {
		t := models.RFC3339Time(*lr.RentalEndsAt)
		resp.RentalEndsAt = &t
	}
	if lr.RenewalStoppedAt != nil {
		t := models.RFC3339Time(*lr.RenewalStoppedAt)
		resp.RenewalStoppedAt = &t
	}
	if lr.DelinquentSince != nil {
		t := models.RFC3339Time(*lr.DelinquentSince)
		resp.DelinquentSince = &t
	}
	if lr.VehicleReturnedAt != nil {
		t := models.RFC3339Time(*lr.VehicleReturnedAt)
		resp.VehicleReturnedAt = &t
	}
	// Most mutation queries RETURN a narrow column list that predates
	// rolling billing, so the lease handed to this builder often carries no
	// billing_mode at all. Serving "" would tell the app a rolling lease is
	// fixed-term — and the app would then take a payment without ever
	// showing the weekly authorization screen. Read the row's real mode
	// instead; billing_mode is immutable after INSERT (migration 000054).
	if resp.BillingMode == "" {
		readFailed := true
		if h.leaseRepo != nil {
			full, ferr := h.leaseRepo.GetByID(ctx, lr.ID)
			if ferr != nil {
				// Never silent: a read failure here is the one way this
				// function can end up asserting a mode it did not read.
				h.logger.Error("lease response: billing_mode read failed",
					"error", ferr, "lease_request_id", lr.ID)
			}
			if ferr == nil && full != nil {
				readFailed = false
				resp.BillingMode = full.BillingMode
				resp.RenewalHaltedReason = full.RenewalHaltedReason
				if full.RentalEndsAt != nil {
					t := models.RFC3339Time(*full.RentalEndsAt)
					resp.RentalEndsAt = &t
				}
				if full.RenewalStoppedAt != nil {
					t := models.RFC3339Time(*full.RenewalStoppedAt)
					resp.RenewalStoppedAt = &t
				}
				if full.DelinquentSince != nil {
					t := models.RFC3339Time(*full.DelinquentSince)
					resp.DelinquentSince = &t
				}
				if full.VehicleReturnedAt != nil {
					t := models.RFC3339Time(*full.VehicleReturnedAt)
					resp.VehicleReturnedAt = &t
				}
			}
		}
		// Only a mode we actually READ may be asserted. When the read
		// succeeded, an empty value means the row really is fixed-term.
		// When it failed, say nothing: the client treats an absent mode as
		// "don't know yet" and asks again, which is the safe answer —
		// claiming fixed_term here would send a rolling driver to the card
		// form with no authorization screen, the exact failure this
		// function exists to prevent.
		if resp.BillingMode == "" && !readFailed {
			resp.BillingMode = models.BillingModeFixedTerm
		}
	}
	if lr.PriceChangeActedAt != nil {
		t := models.RFC3339Time(*lr.PriceChangeActedAt)
		resp.PriceChangeActedAt = &t
	}
	if lr.PickupDeadlineAt != nil {
		t := models.RFC3339Time(*lr.PickupDeadlineAt)
		resp.PickupDeadlineAt = &t
	}
	if lr.PickupConfirmedAt != nil {
		t := models.RFC3339Time(*lr.PickupConfirmedAt)
		resp.PickupConfirmedAt = &t
	}
	if lr.RefundedAt != nil {
		t := models.RFC3339Time(*lr.RefundedAt)
		resp.RefundedAt = &t
	}
	if lr.PickupLastExtendedAt != nil {
		t := models.RFC3339Time(*lr.PickupLastExtendedAt)
		resp.PickupLastExtendedAt = &t
	}

	// Look up names
	if driver, err := h.userRepo.GetByID(ctx, lr.DriverID); err == nil {
		resp.DriverName = driver.FullName()
	}
	if owner, err := h.userRepo.GetByID(ctx, lr.OwnerID); err == nil {
		resp.OwnerName = owner.FullName()
	}

	// Car title
	if car, err := h.carRepo.GetByID(ctx, lr.ListingID); err == nil {
		resp.CarTitle = car.Title
	}

	// Payment summary
	if payment != nil {
		resp.Payment = &models.PaymentSummary{
			ID:                payment.ID,
			PaymentIntentID:   payment.PaymentIntentID,
			Amount:            payment.Amount,
			PlatformFeeAmount: payment.PlatformFeeAmount,
			Currency:          payment.Currency,
			Status:            payment.Status,
		}
	} else {
		// Try to load payment
		if p, err := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, lr.ID); err == nil && p != nil {
			resp.Payment = &models.PaymentSummary{
				ID:                p.ID,
				PaymentIntentID:   p.PaymentIntentID,
				Amount:            p.Amount,
				PlatformFeeAmount: p.PlatformFeeAmount,
				Currency:          p.Currency,
				Status:            p.Status,
			}
		}
	}

	return resp
}

// ─── Shared driver documents ────────────────────────────────────────────────

// SharedDocumentResponse is the owner-facing view of a driver document shared
// through a lease request. It intentionally does NOT expose the on-disk
// file_path; only the public file_url (under /uploads/...) is surfaced, and
// the listing endpoint is gated by chat participation.
type SharedDocumentResponse struct {
	ID         uuid.UUID             `json:"id"`
	DocumentID uuid.UUID             `json:"document_id"`
	UploaderID uuid.UUID             `json:"uploader_id"`
	Type       models.DocumentType   `json:"type"`
	FileName   string                `json:"file_name"`
	FileURL    string                `json:"file_url"`
	FileSize   int64                 `json:"file_size"`
	MimeType   string                `json:"mime_type"`
	Status     models.DocumentStatus `json:"status"`
	SharedAt   models.RFC3339Time    `json:"shared_at"`
}

// VehicleDocumentResponse is the driver-facing view of a car document
// (registration, insurance, …) for the listing being requested. Owner
// uploads these via /cars/{carId}/documents; this surface signs the URLs
// so the driver can view them inside the chat without re-issuing them
// publicly.
type VehicleDocumentResponse struct {
	ID           uuid.UUID              `json:"id"`
	DocumentType models.CarDocumentType `json:"document_type"`
	FileName     string                 `json:"file_name"`
	FileURL      string                 `json:"file_url"`
	FileSize     int                    `json:"file_size"`
	MimeType     string                 `json:"mime_type"`
	CreatedAt    models.RFC3339Time     `json:"created_at"`
}

// SharedDocumentsListResponse is role-aware. The same chat surface serves
// both sides:
//   - viewer_role=owner   → driver_documents populated (driver's license);
//     vehicle_documents empty.
//   - viewer_role=driver  → vehicle_documents populated (the listing's
//     registration / insurance / …); driver_documents empty.
//
// Old clients that decoded just `documents` still work — the field is
// preserved alongside driver_documents and contains the same payload.
type SharedDocumentsListResponse struct {
	ViewerRole       string                    `json:"viewer_role"`
	DriverDocuments  []SharedDocumentResponse  `json:"driver_documents"`
	VehicleDocuments []VehicleDocumentResponse `json:"vehicle_documents"`
	// Documents mirrors DriverDocuments to keep older app versions working
	// while they migrate. New clients should ignore this field.
	Documents []SharedDocumentResponse `json:"documents"`
}

// shareDriverDocs captures a snapshot of the driver's onboarding documents
// into the lease_request_shared_documents link table, so the car owner can
// view them from the chat without the driver re-uploading anything. Called
// AFTER the lease request transaction commits and treated as best-effort:
// a failure here must never prevent the lease request from being returned.
func (h *LeaseRequestHandler) shareDriverDocs(ctx context.Context, lr *models.LeaseRequest) {
	// Share EVERYTHING the driver has uploaded, with live status (client
	// fix batch, item 7: owners vet TLC/commercial licences too, not just
	// the photo ID). The earlier licence-only tightening existed because
	// the "Vehicle Registration" label read as the LISTING's papers — that
	// is a labeling concern, not an access one; each row now carries its
	// type and verification status.
	docs, err := h.docRepo.GetByUserID(ctx, lr.DriverID)
	if err != nil {
		h.logger.Warn("share driver docs: lookup failed",
			"error", err, "driver_id", lr.DriverID)
		return
	}
	var docIDs []uuid.UUID
	for _, d := range docs {
		docIDs = append(docIDs, d.ID)
	}
	if len(docIDs) == 0 {
		return
	}

	if err := h.sharedDocsRepo.CreateForLeaseRequest(ctx, lr.ID, docIDs); err != nil {
		h.logger.Warn("share driver docs: insert failed",
			"error", err, "lease_request_id", lr.ID, "count", len(docIDs))
	}
}

// ListSharedDocuments handles GET /api/v1/chats/{chatId}/shared-documents.
// Returns every driver document shared through any lease request in the chat.
// Auth: caller must be a participant (driver or owner) of the chat; unrelated
// users get 403. A driver CAN see their own shared docs (helpful context),
// but they cannot reach a chat they aren't part of.
func (h *LeaseRequestHandler) ListSharedDocuments(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid chat ID"))
		return
	}

	isParticipant, err := h.chatRepo.IsParticipant(r.Context(), chatID, userID)
	if err != nil {
		h.logger.Error("shared docs: participant check failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !isParticipant {
		httputil.WriteError(w, http.StatusForbidden, models.ErrNotParticipant)
		return
	}

	// Resolve viewer role from the chat itself — chat.OwnerID / DriverID
	// tell us which side this user is on without an extra DB call.
	chat, err := h.chatRepo.GetChatByID(r.Context(), chatID)
	if err != nil || chat == nil {
		h.logger.Error("shared docs: chat lookup failed", "error", err, "chat_id", chatID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	viewerRole := "driver"
	if userID == chat.OwnerID {
		viewerRole = "owner"
	}

	// Driver documents — populated for the OWNER viewer only, every type
	// the driver has uploaded, with live verification status (item 7).
	// Self-heal first: documents uploaded AFTER the lease request was made
	// (and chats created before this change) were never linked, so re-link
	// the driver's current documents to the chat's newest lease request —
	// idempotent (ON CONFLICT DO NOTHING), and gated on a lease request
	// existing, which keeps the authorization exactly as tight: an owner
	// only ever sees documents of a driver who requested THEIR car.
	driverDocs := make([]SharedDocumentResponse, 0)
	if viewerRole == "owner" {
		if lrs, lerr := h.leaseRepo.ListForChat(r.Context(), chatID); lerr == nil && len(lrs) > 0 {
			if docs, derr := h.docRepo.GetByUserID(r.Context(), chat.DriverID); derr == nil && len(docs) > 0 {
				ids := make([]uuid.UUID, 0, len(docs))
				for _, d := range docs {
					ids = append(ids, d.ID)
				}
				if serr := h.sharedDocsRepo.CreateForLeaseRequest(r.Context(), lrs[0].ID, ids); serr != nil {
					h.logger.Warn("shared docs: self-heal relink failed", "error", serr, "chat_id", chatID)
				}
			}
		}
		infos, err := h.sharedDocsRepo.ListByChatID(r.Context(), chatID)
		if err != nil {
			h.logger.Error("shared docs: list failed", "error", err, "chat_id", chatID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		for _, info := range infos {
			driverDocs = append(driverDocs, SharedDocumentResponse{
				ID:         info.ID,
				DocumentID: info.DocumentID,
				UploaderID: info.UploaderID,
				Type:       info.Type,
				FileName:   info.FileName,
				FileURL:    h.urlSigner.Sign(publicURLForDocument(info.UploaderID, info.FilePath)),
				FileSize:   info.FileSize,
				MimeType:   info.MimeType,
				Status:     info.Status,
				SharedAt:   models.NewRFC3339Time(info.SharedAt),
			})
		}
	}

	// Vehicle documents — populated for the DRIVER viewer only, AND only
	// after the owner has accepted at least one lease request in this chat.
	// Car documents (registration, insurance, …) can be misused for fraud,
	// so we refuse to reveal them — and refuse to issue signed URLs for
	// them — until the owner has explicitly opted in by accepting. Once a
	// request is accepted/payment_pending/paid, access flips on; on
	// terminal cancellation/expiry it flips back off. Owners always have
	// full access to their own car documents via /cars/{carId}/documents,
	// so they don't need this surface.
	vehicleDocs := make([]VehicleDocumentResponse, 0)
	if viewerRole == "driver" {
		allowed, err := h.leaseRepo.HasAcceptedLeaseForChat(r.Context(), chatID, userID)
		if err != nil {
			h.logger.Error("shared docs: acceptance gate check failed",
				"error", err, "chat_id", chatID, "driver_id", userID)
			// Fail closed: if we can't prove the gate is open, do not leak
			// signed URLs. The driver simply sees an empty section.
			allowed = false
		}
		if allowed {
			docs, err := h.carDocRepo.GetByCarID(r.Context(), chat.CarID)
			if err != nil {
				h.logger.Error("shared docs: car docs list failed",
					"error", err, "car_id", chat.CarID)
			} else {
				for _, d := range docs {
					vehicleDocs = append(vehicleDocs, VehicleDocumentResponse{
						ID:           d.ID,
						DocumentType: d.DocumentType,
						FileName:     d.FileName,
						FileURL:      h.urlSigner.Sign(d.FileURL),
						FileSize:     d.FileSize,
						MimeType:     d.MimeType,
						CreatedAt:    models.RFC3339Time(d.CreatedAt),
					})
				}
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, SharedDocumentsListResponse{
		ViewerRole:       viewerRole,
		DriverDocuments:  driverDocs,
		VehicleDocuments: vehicleDocs,
		Documents:        driverDocs, // back-compat for older clients
	})
}

// publicURLForDocument derives the /uploads/... relative URL from a stored
// document FilePath. Documents are written to {uploadDir}/{userID}/{onDiskName}
// by the upload handler, so the last two path segments form the public URL
// under the /uploads/* static file server — the same convention already used
// by car photos and profile photos.
func publicURLForDocument(userID uuid.UUID, filePath string) string {
	diskName := filepath.Base(filePath)
	return fmt.Sprintf("/uploads/%s/%s", userID.String(), diskName)
}

// cardForPaymentMethod fetches the saved card's brand, last4 and fingerprint
// for the consent row. Best-effort by design: activation is the money-
// critical step and must not fail because a lookup did — the row activates
// without card details and the card-update path fills them in later. The
// fingerprint is what recognises a returning debtor across accounts (a flag
// for a human; never an automatic block).
// activateRecoveredConsent binds the rolling mandate to the card that actually
// paid, for the two recovery paths that reach 'paid' WITHOUT a webhook:
// SyncPaymentStatus (the app calls it the moment PaymentSheet completes) and
// adoptSucceededPayment (the reconciliation sweep).
//
// Before this, only the webhook activated the consent. A lease recovered by
// either path was fully paid with a DEAD mandate: week 1 ran, the owner was
// told "renewing until they return it", and at the next charge lead the engine
// saw !consent.Active(), halted renewals and told the driver "we can't charge
// your saved card anymore" — when no card had ever been attached and the
// driver had done nothing wrong. Observed end to end in the T-C/C3 run
// (2026-09-18): relay stopped mid-charge, sync recovered the lease, consent
// stayed NULL. Webhook redelivery does heal it, so the exposure is a webhook
// that is never successfully delivered inside Stripe's retry window.
//
// Safe to call on every paid transition: it is a no-op for fixed-term leases
// and for a consent that is already active, and ActivateConsent itself is
// claimed-once (activated_at IS NULL in its WHERE), so a racing webhook and a
// racing sweep cannot double-bind or overwrite each other.
func (h *LeaseRequestHandler) activateRecoveredConsent(ctx context.Context, lr *models.LeaseRequest, intentID, pmID string) {
	if h.billingRepo == nil || lr == nil || lr.BillingMode != models.BillingModeRolling {
		return
	}
	consent, cerr := h.billingRepo.GetActiveConsent(ctx, lr.ID)
	if cerr != nil {
		h.logger.Error("rolling consent: recovery lookup", "error", cerr, "lease_request_id", lr.ID)
		return
	}
	if consent == nil || consent.ActivatedAt != nil {
		return
	}
	if pmID == "" && intentID != "" && h.stripe != nil {
		if pi, rerr := h.stripe.RetrievePaymentIntent(intentID); rerr == nil {
			pmID = pi.PaymentMethod
		} else {
			h.logger.Warn("rolling consent: recovery could not retrieve intent", "error", rerr, "lease_request_id", lr.ID)
		}
	}
	if pmID == "" {
		// Leave it for webhook redelivery rather than binding a guess: an
		// unactivated consent halts renewals loudly, a wrong card charges
		// the wrong person.
		h.logger.Error("rolling consent: recovery has no payment_method — leaving for webhook redelivery", "lease_request_id", lr.ID)
		return
	}
	brand, last4, fp := h.cardForPaymentMethod(pmID, lr.ID)
	if activated, aerr := h.billingRepo.ActivateConsent(ctx, lr.ID, pmID, brand, last4, fp); aerr != nil {
		h.logger.Error("rolling consent: recovery activate", "error", aerr, "lease_request_id", lr.ID)
	} else if activated {
		h.logger.Info("rolling consent activated by recovery path", "lease_request_id", lr.ID)
	}
}

func (h *LeaseRequestHandler) cardForPaymentMethod(pmID string, leaseID uuid.UUID) (brand, last4, fingerprint string) {
	if h.stripe == nil || pmID == "" {
		return "", "", ""
	}
	card, err := h.stripe.RetrievePaymentMethodCard(pmID)
	if err != nil {
		h.logger.Warn("rolling consent: card details unavailable — activating without them",
			"error", err, "lease_request_id", leaseID, "payment_method", pmID)
		return "", "", ""
	}
	return card.Brand, card.Last4, card.Fingerprint
}

// SetRollingAllowlist names the drivers for whom weekly rentals are offered
// while ROLLING_RENTALS_ENABLED is on. An empty list leaves the feature open
// to everyone, which is what the flag meant before a pilot existed.
// SetRollingAllowlistClosed marks the allowlist unusable (it was configured
// but nothing parsed), so weekly rentals are offered to nobody until it is
// fixed — the safe direction for a feature that opens recurring mandates.
func (h *LeaseRequestHandler) SetRollingAllowlistClosed(closed bool) { h.rollingClosed = closed }

// SetRecurringOnly wires RECURRING_ONLY. See docs/DESIGN_RECURRING_BILLING.md §11.
func (h *LeaseRequestHandler) SetRecurringOnly(on bool) { h.recurringOnly = on }

// SetMonthlyEnabled wires MONTHLY_RENTALS_ENABLED.
func (h *LeaseRequestHandler) SetMonthlyEnabled(on bool) { h.monthlyEnabled = on }

// rentalsPausedMsg is shared by the listing notice and the RENTALS_PAUSED
// refusal, so a driver never reads one sentence and then a different one.
const rentalsPausedMsg = "Rentals are paused for your account right now. Nothing was charged. We'll let you know when they reopen."

// RentRefusalFor returns the reason THIS viewer would be refused a rental of
// this listing outright, or "" when a request would succeed in some mode.
//
// It mirrors the refusals in CreateLeaseRequest exactly, so the listing screen
// can show a notice instead of a button that can only 409. Only the
// interval refusals are absolute: everything else either creates a recurring
// lease or falls back to fixed-term.
func (h *LeaseRequestHandler) RentRefusalFor(viewer, owner uuid.UUID, period string) string {
	if !h.RollingOpenFor(viewer) {
		if h.recurringOnly {
			// RECURRING_ONLY refuses this viewer on EVERY listing. Unreachable
			// while a named pilot is in force (main.go forces the flag off
			// unless rolling is open to everyone), but the pilot ending is the
			// intended future state and that invariant lives in main.go, not
			// here — so the notice models it rather than trusting it.
			return rentalsPausedMsg
		}
		return "" // fixed-term path: any period is rentable, as it always was
	}
	if h.rollingAllowlist != nil && !h.RollingOpenFor(owner) {
		return "" // falls back to fixed-term rather than refusing
	}
	interval, ok := models.BillingIntervalForRentPeriod(period)
	if !ok {
		return fmt.Sprintf("This car is priced per %s. Rentals by the %s aren't available yet — try a car priced per week.",
			models.RentPeriodLabel(period), models.RentPeriodLabel(period))
	}
	if interval == "monthly" && !h.monthlyEnabled {
		return "This car is priced per month. Monthly rentals aren't available yet — try a car priced per week."
	}
	return ""
}

// RecurringAvailableFor is the one predicate the listing screen, the request
// handler and GET /config agree on: would a request from viewer for this
// owner's listing (priced per period) be created as a recurring lease?
func (h *LeaseRequestHandler) RecurringAvailableFor(viewer, owner uuid.UUID, period string) bool {
	if !h.RollingOpenFor(viewer) {
		return false
	}
	if h.rollingAllowlist != nil && !h.RollingOpenFor(owner) {
		return false
	}
	interval, ok := models.BillingIntervalForRentPeriod(period)
	if !ok || (interval == "monthly" && !h.monthlyEnabled) {
		return false
	}
	return true
}

func (h *LeaseRequestHandler) SetRollingAllowlist(ids []uuid.UUID) {
	if len(ids) == 0 {
		h.rollingAllowlist = nil
		return
	}
	h.rollingAllowlist = make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		h.rollingAllowlist[id] = struct{}{}
	}
}

// RollingOpenFor reports whether weekly rentals are offered to this user.
// The same answer must drive BOTH the /config response the driver's app asks
// before rendering the CTA and the refusal on lease creation — an option that
// is shown but then refused is the failure mode the CTA gate exists to avoid.
func (h *LeaseRequestHandler) RollingOpenFor(userID uuid.UUID) bool {
	if !h.rollingEnabled || h.rollingClosed {
		return false
	}
	if h.rollingAllowlist == nil {
		return true
	}
	_, ok := h.rollingAllowlist[userID]
	return ok
}

// refundLooksPayable answers, before we say anything to anyone, whether a
// refund on this lease can plausibly succeed: there is a succeeded payment,
// it carries an intent, and the charge is young enough to refund. It is only
// used to choose HONEST COPY — issueAndFinalizeRefund re-checks and is the
// authority on what actually happens.
func (h *LeaseRequestHandler) refundLooksPayable(ctx context.Context, lr *models.LeaseRequest) bool {
	payment, err := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, lr.ID)
	if err != nil || payment == nil || payment.PaymentIntentID == nil || *payment.PaymentIntentID == "" {
		return false
	}
	if time.Since(payment.CreatedAt) > models.LeaseRefundMaxAge {
		return false
	}
	// Ask Stripe rather than guess. The rows this path can actually reach in
	// production are old enough to predate the Stripe account we run on now,
	// and their intents simply do not exist here — but they are still inside
	// the refundable age window, so age alone would let us promise a refund
	// that can never arrive. One retrieve on a rare, owner-initiated action
	// is worth not telling someone their money is coming back when it isn't.
	if h.stripe != nil {
		if _, cerr := h.stripe.GetLatestChargeID(*payment.PaymentIntentID); cerr != nil {
			h.logger.Warn("release: payment intent not reachable on this Stripe account — not promising a refund",
				"error", cerr, "lease_request_id", lr.ID, "intent_id", *payment.PaymentIntentID)
			return false
		}
	}
	return true
}

// notifyOwnerRelease tells both parties what actually happened when an owner
// (or support) closes out a rental that never started.
//
// It deliberately does NOT reuse notifyExpiry's copy. That text says "You
// didn't confirm pickup in time" — blame for a deadline that, in every row
// this path can reach, was never armed and therefore never communicated. And
// it asserts a refund unconditionally, which on an old charge is a promise
// the code cannot keep.
func (h *LeaseRequestHandler) notifyOwnerRelease(ctx context.Context, lr *models.LeaseRequest, refundExpected bool) {
	carTitle := "the car"
	if car, err := h.carRepo.GetByID(ctx, lr.ListingID); err == nil {
		carTitle = car.Title
	}
	chatID := lr.ChatID
	lrID := lr.ID

	driverBody := fmt.Sprintf("Your rental of %s was closed because the car was never collected. You were not charged for any use of it.", carTitle)
	if refundExpected {
		driverBody = fmt.Sprintf("Your rental of %s was closed because the car was never collected. Your payment is being refunded — we'll confirm as soon as it's done.", carTitle)
	} else {
		driverBody += " If you paid for it and haven't been refunded, contact support and we'll sort it out."
	}
	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Rental closed — the car was never collected", driverBody, &chatID, &lrID)

	ownerBody := fmt.Sprintf("%s is back on the market. The rental that was holding it never started.", carTitle)
	if refundExpected {
		ownerBody += " The driver's payment is being refunded."
	}
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypeLeaseRequest,
		"Your car has been released", ownerBody, &chatID, &lrID)
}

// leaseQuoteAmount is the figure the card quotes and the driver taps Pay
// from. For a recurring lease that is ONE cycle of its interval — the same
// number the consent sheet and the PaymentIntent carry — never weekly × weeks.
func leaseQuoteAmount(lr *models.LeaseRequest) float64 {
	if lr.BillingMode == models.BillingModeRolling {
		return lr.IntervalPrice()
	}
	return float64(lr.TotalAmountCents()) / 100.0
}

func leaseIntervalCents(lr *models.LeaseRequest) int64 {
	if lr.BillingMode == models.BillingModeRolling {
		return lr.IntervalAmountCents()
	}
	return 0
}

// ownerPaidBody is what the OWNER reads when a driver's first payment lands.
//
// The historical wording — "paid for N week(s)" — describes a bounded rental.
// On a recurring lease that is false in the way that cost us 2026-09-14: the
// owner is told a fixed term for a rental that renews until the car comes
// back. A recurring lease names its cadence and its amount per cycle; a
// fixed-term lease keeps the old sentence byte for byte.
func ownerPaidBody(driverName, carTitle string, lr *models.LeaseRequest) string {
	if lr.BillingMode == models.BillingModeRolling {
		return fmt.Sprintf("%s paid the first %s of %s — %s %.2f per %s, renewing until they return it. Coordinate pickup in chat.",
			driverName, models.IntervalUnit(lr.BillingInterval), carTitle,
			lr.Currency, lr.IntervalPrice(), models.IntervalUnit(lr.BillingInterval))
	}
	return fmt.Sprintf("%s paid for %d week(s) of %s — coordinate pickup in chat", driverName, lr.Weeks, carTitle)
}
