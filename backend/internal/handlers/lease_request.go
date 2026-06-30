package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
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
	// pickupDeadline is the grace window after payment_intent.succeeded in
	// which the driver must confirm pickup. Past this point a background
	// scanner will refund the payment and unreserve the car.
	pickupDeadline time.Duration
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

	listingID, err := uuid.Parse(chi.URLParam(r, "listingId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid listing ID"))
		return
	}

	var body models.CreateLeaseRequestBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		// Body is optional, allow empty
		body = models.CreateLeaseRequestBody{}
	}

	// Fetch the car listing
	car, err := h.carRepo.GetByID(r.Context(), listingID)
	if err != nil {
		h.logger.Error("get car for lease request", "error", err)
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("CAR_NOT_FOUND", "Car listing not found"))
		return
	}

	// Validate: car must be for rent
	if !car.IsForRent || !car.WeeklyRentPrice.Valid {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrCarNotForRent)
		return
	}

	// Validate: driver cannot request own car
	if userID == car.OwnerID {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrCannotLeaseOwnCar)
		return
	}

	weeks := 1
	if body.Weeks != nil && *body.Weeks > 0 {
		weeks = *body.Weeks
	}

	lr := &models.LeaseRequest{
		ListingID:   listingID,
		OwnerID:     car.OwnerID,
		DriverID:    userID,
		WeeklyPrice: car.WeeklyRentPrice.Float64,
		Currency:    car.Currency,
		Weeks:       weeks,
		Message:     body.Message,
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
			if apiErr.Code == models.ErrCodeLeaseRequestNotFound {
				status = http.StatusNotFound
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

	// Check if payment already exists (idempotent — return stored client_secret)
	existingPayment, err := h.leaseRepo.GetPaymentByLeaseRequestID(r.Context(), leaseID)
	if err != nil {
		h.logger.Error("check existing payment", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	if existingPayment != nil && existingPayment.PaymentIntentID != nil && existingPayment.ClientSecret != nil {
		// Already have a PaymentIntent with stored client_secret — return it
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

		httputil.WriteJSON(w, http.StatusOK, models.PaymentIntentResponse{
			PaymentIntentClientSecret: *existingPayment.ClientSecret,
			PaymentIntentID:           *existingPayment.PaymentIntentID,
			PublishableKey:            h.stripe.PublishableKey(),
			CustomerID:                customerID,
			EphemeralKeySecret:        ephemeralKeySecret,
			Amount:                    existingPayment.Amount,
			Currency:                  existingPayment.Currency,
		})
		return
	}

	// Compute amount
	totalCents := lr.TotalAmountCents()
	platformFeeCents := h.stripe.PlatformFee(totalCents)

	// Get driver user for Stripe customer
	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("get user for stripe", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Find or create Stripe customer
	customer, err := h.stripe.FindOrCreateCustomer(user.Email, user.FullName())
	if err != nil {
		h.logger.Error("stripe find/create customer", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create payment customer"))
		return
	}

	// Create ephemeral key
	ephemeralKey, err := h.stripe.CreateEphemeralKey(customer.ID)
	if err != nil {
		h.logger.Error("stripe create ephemeral key", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create ephemeral key"))
		return
	}

	// Create PaymentIntent (idempotency key = lease request ID)
	pi, err := h.stripe.CreatePaymentIntent(totalCents, lr.Currency, customer.ID, platformFeeCents, leaseID.String())
	if err != nil {
		h.logger.Error("stripe create payment intent", "error", err)
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
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	}

	// Transition lease request to payment_pending
	if lr.Status == models.LeaseStatusAccepted {
		_, err = h.leaseRepo.SetPaymentPending(r.Context(), leaseID)
		if err != nil {
			h.logger.Warn("failed to set payment_pending", "error", err)
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
				fmt.Sprintf("%s paid for %d week(s) of %s — coordinate pickup in chat", driverName, lr.Weeks, carTitle),
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

	switch eventType {
	case "payment_intent.succeeded":
		h.handlePaymentSucceeded(r, intentID)
	case "payment_intent.payment_failed":
		h.handlePaymentFailed(r, intentID)
	case "payment_intent.canceled":
		h.handlePaymentCanceled(r, intentID)
	}

	w.WriteHeader(http.StatusOK)
}

func (h *LeaseRequestHandler) handlePaymentSucceeded(r *http.Request, intentID string) {
	payment, err := h.leaseRepo.GetPaymentByIntentID(r.Context(), intentID)
	if err != nil {
		// Not found = PI was not created by us; ignore silently
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodePaymentNotFound {
			h.logger.Info("webhook: ignoring unknown payment_intent", "intent_id", intentID)
		} else {
			h.logger.Error("webhook: get payment by intent", "event", "succeeded", "intent_id", intentID, "error", err)
		}
		return
	}

	// Idempotency: if already succeeded, skip
	if payment.Status == models.PaymentStatusSucceeded {
		h.logger.Info("webhook: payment already succeeded (idempotent skip)", "intent_id", intentID, "payment_id", payment.ID)
		return
	}

	// Update payment status
	if err := h.leaseRepo.UpdatePaymentStatus(r.Context(), payment.ID, models.PaymentStatusSucceeded); err != nil {
		h.logger.Error("webhook: update payment status", "event", "succeeded", "payment_id", payment.ID, "error", err)
		return
	}

	// Transition lease request to paid (accepts both accepted and payment_pending)
	lr, err := h.leaseRepo.SetPaid(r.Context(), payment.LeaseRequestID)
	if err != nil {
		// If already in a terminal state (paid/declined/cancelled), log as idempotent
		if apiErr := models.GetAPIError(err); apiErr != nil && apiErr.Code == models.ErrCodeInvalidLeaseAction {
			h.logger.Info("webhook: lease already in terminal state (idempotent skip)", "lease_request_id", payment.LeaseRequestID, "intent_id", intentID)
			// Still broadcast in case the client missed the first one
			lr, _ = h.leaseRepo.GetByID(r.Context(), payment.LeaseRequestID)
		} else {
			h.logger.Error("webhook: set lease paid", "lease_request_id", payment.LeaseRequestID, "error", err)
			return
		}
	}

	if lr == nil {
		return
	}

	h.logger.Info("payment succeeded", "lease_request_id", lr.ID, "payment_id", payment.ID, "intent_id", intentID)

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
	ownerBody := fmt.Sprintf("%s paid for %d week(s) of %s — coordinate pickup in chat", driverName, lr.Weeks, carTitle)
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
}

// ensureKeyHandover creates the key-handover task for a freshly paid lease
// (idempotent on lease_request_id) and broadcasts it so both parties' Today
// tabs pick it up. Pickup location is snapshotted from the car listing.
func (h *LeaseRequestHandler) ensureKeyHandover(r *http.Request, lr *models.LeaseRequest) {
	if h.keyHandoverRepo == nil {
		return
	}

	var lat, lng *float64
	var area *string
	if car, err := h.carRepo.GetByID(r.Context(), lr.ListingID); err == nil {
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

	kh, err := h.keyHandoverRepo.CreateForLease(r.Context(), lr, lat, lng, area)
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
		}
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

	// Step 2: broadcast the cancel + notify so the UI flips immediately,
	// regardless of the Stripe call latency.
	h.broadcastLeaseUpdate(ctx, lr)
	h.notifyExpiry(ctx, lr)

	// Step 3: issue + finalize the refund (shared with the stuck-retry path).
	h.issueAndFinalizeRefund(ctx, lr, "expiry")
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
	if err != nil || payment == nil || payment.PaymentIntentID == nil {
		h.logger.Error("expiry: payment lookup failed", "phase", phase, "error", err, "lease_request_id", lr.ID)
		if ferr := h.leaseRepo.FinalizeRefund(ctx, lr.ID, "", models.RefundStatusFailed); ferr != nil {
			h.logger.Error("expiry: finalize refund (no PI)", "phase", phase, "error", ferr, "lease_request_id", lr.ID)
		}
		return
	}

	idemKey := fmt.Sprintf("refund-%s", lr.ID.String())
	// amountCents=0 → full refund (pickup expiry reverses the entire payment).
	refund, err := h.stripe.CreateRefund(*payment.PaymentIntentID, idemKey, "requested_by_customer", 0)
	if err != nil {
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

	go h.notifHandler.Notify(lr.DriverID, models.NotificationTypePayment,
		"Pickup deadline missed",
		fmt.Sprintf("You didn't confirm pickup of %s in time. Your payment has been refunded to your card.", carTitle),
		&chatID, &lrID)
	go h.notifHandler.Notify(lr.OwnerID, models.NotificationTypePayment,
		"Pickup deadline missed",
		fmt.Sprintf("%s didn't pick up %s in time. The rental was cancelled, the payment refunded, and your listing is back on the market.", driverName, carTitle),
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
		TotalAmount:                 float64(lr.TotalAmountCents()) / 100.0,
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
	// Share only the driver's photo ID. The other onboarding doc
	// (DocumentRegistration) is the driver's OWN vehicle registration, kept
	// for identity verification — it's not relevant to the car owner
	// deciding whether to rent THEIR car out, and the UI label "Vehicle
	// Registration" caused owners to confuse it with the listing's car
	// papers. Vehicle/car documents go through the dedicated car_documents
	// surface on the driver side; see ListSharedDocuments below.
	required := []models.DocumentType{
		models.DocumentDriversLicense,
	}

	var docIDs []uuid.UUID
	for _, t := range required {
		doc, err := h.docRepo.GetByUserIDAndType(ctx, lr.DriverID, t)
		if err != nil {
			h.logger.Warn("share driver docs: lookup failed",
				"error", err, "driver_id", lr.DriverID, "type", t)
			continue
		}
		if doc != nil {
			docIDs = append(docIDs, doc.ID)
		}
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

	// Driver documents (license) — populated for the OWNER viewer only.
	// We filter to drivers_license to keep the surface correctly scoped
	// even on lease requests created before shareDriverDocs was tightened
	// (those have a stale `registration` row that we want to suppress).
	driverDocs := make([]SharedDocumentResponse, 0)
	if viewerRole == "owner" {
		infos, err := h.sharedDocsRepo.ListByChatID(r.Context(), chatID)
		if err != nil {
			h.logger.Error("shared docs: list failed", "error", err, "chat_id", chatID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		for _, info := range infos {
			if info.Type != models.DocumentDriversLicense {
				continue
			}
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
