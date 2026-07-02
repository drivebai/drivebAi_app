package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

// PurchaseRequestHandler serves the buy-the-car endpoints + runs the
// offer-expiry and auth-expiry scanners. Mirrors VehicleReturnHandler's
// shape for consistency with the other post-lease flows.
type PurchaseRequestHandler struct {
	repo         *repository.PurchaseRequestRepository
	carRepo      *repository.CarRepository
	userRepo     *repository.UserRepository
	chatRepo     *repository.ChatRepository
	leaseRepo    *repository.LeaseRequestRepository
	stripe       *stripeService.Service
	wsHub        *ws.Hub
	notifHandler *NotificationHandler
	urlSigner    *PrivateURLSigner
	uploadDir    string
	logger       *slog.Logger
}

func NewPurchaseRequestHandler(
	repo *repository.PurchaseRequestRepository,
	carRepo *repository.CarRepository,
	userRepo *repository.UserRepository,
	chatRepo *repository.ChatRepository,
	leaseRepo *repository.LeaseRequestRepository,
	stripe *stripeService.Service,
	wsHub *ws.Hub,
	notifHandler *NotificationHandler,
	urlSigner *PrivateURLSigner,
	uploadDir string,
	logger *slog.Logger,
) *PurchaseRequestHandler {
	return &PurchaseRequestHandler{
		repo:         repo,
		carRepo:      carRepo,
		userRepo:     userRepo,
		chatRepo:     chatRepo,
		leaseRepo:    leaseRepo,
		stripe:       stripe,
		wsHub:        wsHub,
		notifHandler: notifHandler,
		urlSigner:    urlSigner,
		uploadDir:    uploadDir,
		logger:       logger,
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func (h *PurchaseRequestHandler) findOrCreatePurchaseChat(ctx context.Context, carID, buyerID, sellerID uuid.UUID) (uuid.UUID, error) {
	c, err := h.chatRepo.FindOrCreateChat(ctx, carID, buyerID, sellerID)
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

func (h *PurchaseRequestHandler) buildBOSResponse(b *models.PurchaseBillOfSale) *models.PurchaseBillOfSaleResponse {
	if b == nil {
		return nil
	}
	resp := &models.PurchaseBillOfSaleResponse{
		ID:                b.ID,
		PurchaseRequestID: b.PurchaseRequestID,
		VehicleYear:       b.VehicleYear,
		VehicleMake:       b.VehicleMake,
		VehicleModel:      b.VehicleModel,
		VIN:               b.VIN,
		SaleAmountCents:   b.SaleAmountCents,
		Currency:          b.Currency,
		TermsConditions:   b.TermsConditions,
		SellerName:        b.SellerName,
		SellerAddress:     b.SellerAddress,
		SellerSignedAt:    models.NewRFC3339TimePtr(b.SellerSignedAt),
		BuyerName:         b.BuyerName,
		BuyerAddress:      b.BuyerAddress,
		BuyerSignedAt:     models.NewRFC3339TimePtr(b.BuyerSignedAt),
		FinalizedAt:       models.NewRFC3339TimePtr(b.FinalizedAt),
		Locked:            b.SellerSigned() || b.BuyerSigned(),
		FullySigned:       b.FullySigned(),
		CreatedAt:         models.RFC3339Time(b.CreatedAt),
		UpdatedAt:         models.RFC3339Time(b.UpdatedAt),
	}
	if b.SellerSignatureURL != nil {
		signed := h.urlSigner.Sign(*b.SellerSignatureURL)
		resp.SellerSignatureURL = &signed
	}
	if b.BuyerSignatureURL != nil {
		signed := h.urlSigner.Sign(*b.BuyerSignatureURL)
		resp.BuyerSignatureURL = &signed
	}
	if b.FinalizedPDFURL != nil {
		signed := h.urlSigner.Sign(*b.FinalizedPDFURL)
		resp.FinalizedPDFURL = &signed
	}
	return resp
}

func (h *PurchaseRequestHandler) buildRejectionResponse(ctx context.Context, rej *models.PurchaseRejection) *models.PurchaseRejectionResponse {
	if rej == nil {
		return nil
	}
	resp := &models.PurchaseRejectionResponse{
		ID:                rej.ID,
		PurchaseRequestID: rej.PurchaseRequestID,
		ReasonCategory:    rej.ReasonCategory,
		Explanation:       rej.Explanation,
		Status:            rej.Status,
		RefundStatus:      rej.RefundStatus,
		AdminNote:         rej.AdminNote,
		ResolvedBy:        rej.ResolvedBy,
		ResolvedAt:        models.NewRFC3339TimePtr(rej.ResolvedAt),
		Evidence:          []models.PurchaseRejectionEvidenceResponse{},
		CreatedAt:         models.RFC3339Time(rej.CreatedAt),
		UpdatedAt:         models.RFC3339Time(rej.UpdatedAt),
	}
	if ev, err := h.repo.ListEvidence(ctx, rej.ID); err == nil {
		for _, e := range ev {
			resp.Evidence = append(resp.Evidence, models.PurchaseRejectionEvidenceResponse{
				ID:        e.ID,
				FileURL:   h.urlSigner.Sign(e.FileURL),
				Filename:  e.Filename,
				MimeType:  e.MimeType,
				SizeBytes: e.SizeBytes,
				CreatedAt: models.RFC3339Time(e.CreatedAt),
			})
		}
	}
	return resp
}

func (h *PurchaseRequestHandler) buildResponse(ctx context.Context, p *models.PurchaseRequest, viewerID uuid.UUID) models.PurchaseRequestResponse {
	resp := models.PurchaseRequestResponse{
		ID:                   p.ID,
		CarID:                p.CarID,
		ChatID:               p.ChatID,
		SellerID:             p.SellerID,
		BuyerID:              p.BuyerID,
		OfferAmountCents:     p.OfferAmountCents,
		Currency:             p.Currency,
		BuyerMessage:         p.BuyerMessage,
		Status:               p.Status,
		ExpiresAt:            models.RFC3339Time(p.ExpiresAt),
		AuthExpiresAt:        models.NewRFC3339TimePtr(p.AuthExpiresAt),
		HandoverLocation:     p.HandoverLocation,
		HandoverLatitude:     p.HandoverLatitude,
		HandoverLongitude:    p.HandoverLongitude,
		HandoverScheduledAt:  models.NewRFC3339TimePtr(p.HandoverScheduledAt),
		KeysHandedOverAt:     models.NewRFC3339TimePtr(p.KeysHandedOverAt),
		InspectionDeadlineAt: models.NewRFC3339TimePtr(p.InspectionDeadlineAt),
		InspectionAcceptedAt: models.NewRFC3339TimePtr(p.InspectionAcceptedAt),
		CompletedAt:          models.NewRFC3339TimePtr(p.CompletedAt),
		PaymentIntentID:      p.PaymentIntentID,
		PaymentStatus:        p.PaymentStatus,
		RefundStatus:         p.RefundStatus,
		RefundID:             p.RefundID,
		RefundedAt:           models.NewRFC3339TimePtr(p.RefundedAt),
		CreatedAt:            models.RFC3339Time(p.CreatedAt),
		UpdatedAt:            models.RFC3339Time(p.UpdatedAt),
	}
	if seller, err := h.userRepo.GetByID(ctx, p.SellerID); err == nil {
		resp.SellerName = seller.FullName()
	}
	if buyer, err := h.userRepo.GetByID(ctx, p.BuyerID); err == nil {
		resp.BuyerName = buyer.FullName()
	}
	if car, err := h.carRepo.GetByID(ctx, p.CarID); err == nil && car != nil {
		resp.CarTitle = car.Title
	}
	switch viewerID {
	case p.SellerID:
		resp.ViewerRole = "seller"
		resp.CounterpartyName = resp.BuyerName
	case p.BuyerID:
		resp.ViewerRole = "buyer"
		resp.CounterpartyName = resp.SellerName
	default:
		resp.ViewerRole = "admin"
	}
	if bos, err := h.repo.GetBillOfSale(ctx, p.ID); err == nil && bos != nil {
		resp.BillOfSale = h.buildBOSResponse(bos)
	}
	if rej, err := h.repo.GetRejection(ctx, p.ID); err == nil && rej != nil {
		resp.Rejection = h.buildRejectionResponse(ctx, rej)
	}
	return resp
}

func (h *PurchaseRequestHandler) broadcast(eventType string, p *models.PurchaseRequest, extras map[string]any) {
	payload := map[string]any{
		"id":                p.ID,
		"car_id":            p.CarID,
		"seller_id":         p.SellerID,
		"buyer_id":          p.BuyerID,
		"status":            p.Status,
		"chat_id":           p.ChatID,
		"offer_amount_cents": p.OfferAmountCents,
	}
	for k, v := range extras {
		payload[k] = v
	}
	h.wsHub.Broadcast(&ws.Event{
		Type:          eventType,
		Payload:       payload,
		TargetUserIDs: []uuid.UUID{p.SellerID, p.BuyerID},
	})
}

func (h *PurchaseRequestHandler) postSystemMessage(ctx context.Context, chatID, senderID uuid.UUID, body string) {
	if h.chatRepo == nil || chatID == uuid.Nil {
		return
	}
	if err := h.chatRepo.PostSystemMessage(ctx, chatID, senderID, body); err != nil {
		h.logger.Warn("purchase: post system message failed", "error", err, "chat_id", chatID)
	}
}

// ─── Buyer: create + cancel ─────────────────────────────────────────────────

// Create — POST /api/v1/cars/{carId}/purchase-requests
func (h *PurchaseRequestHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	carID, err := uuid.Parse(chi.URLParam(r, "carId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid car id"))
		return
	}
	var body models.CreatePurchaseRequestBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if body.OfferAmountCents < models.PurchaseOfferMinCents {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrPurchaseOfferTooLow)
		return
	}

	car, err := h.carRepo.GetByID(r.Context(), carID)
	if err != nil || car == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("CAR_NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID == userID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrCannotBuyOwnCar)
		return
	}
	if !car.IsForSale || !car.SalePrice.Valid {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarNotForSale)
		return
	}
	if car.Status == models.CarStatusSold || car.IsPaused {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarSold)
		return
	}
	// Friendly duplicate check before the unique-index blows.
	if existing, err := h.repo.GetActiveByCarAndBuyer(r.Context(), carID, userID); err == nil && existing != nil {
		httputil.WriteError(w, http.StatusConflict, models.ErrDuplicatePurchase)
		return
	}

	chatID, err := h.findOrCreatePurchaseChat(r.Context(), carID, userID, car.OwnerID)
	if err != nil {
		h.logger.Error("purchase: create chat", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	created, err := h.repo.CreateForCar(r.Context(), repository.CreatePurchaseRequestParams{
		CarID:            carID,
		SellerID:         car.OwnerID,
		BuyerID:          userID,
		ChatID:           chatID,
		OfferAmountCents: body.OfferAmountCents,
		Currency:         "USD",
		BuyerMessage:     body.BuyerMessage,
		ExpiresAt:        time.Now().UTC().Add(models.PurchaseOfferTTL),
	})
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("purchase: create", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), created, userID)
	httputil.WriteJSON(w, http.StatusCreated, resp)
	h.broadcast("purchase_request_created", created, nil)
	h.postSystemMessage(r.Context(), chatID, userID,
		fmt.Sprintf("New purchase offer: %s", formatMoney(created.OfferAmountCents)))

	go h.notifHandler.Notify(created.SellerID, models.NotificationTypePurchaseRequest,
		"New purchase offer",
		fmt.Sprintf("%s offered %s for %s", nameOr(resp.BuyerName, "A buyer"), formatMoney(created.OfferAmountCents), carTitleOr(resp.CarTitle)),
		&chatID, &created.ID)
}

// Cancel — POST /api/v1/purchase-requests/{id}/cancel
func (h *PurchaseRequestHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}

	// If a PaymentIntent was already created (BoS signed → intent
	// created → buyer cancels before webhook confirms) we MUST cancel
	// it at Stripe first — otherwise the auth can still succeed via
	// webhook after our DB row terminates, leaving the buyer with a
	// ~7-day hold on their card for a purchase they cancelled and no
	// reconciliation path.
	//
	// We fetch the current row before mutating so we can read the
	// payment_intent_id if any. Cancel-at-Stripe is best-effort: if it
	// fails (already-succeeded, network hiccup) we log + still let the
	// DB cancel proceed so the user isn't trapped in a stuck row. Any
	// stuck auth ends up on the admin retry surface via the stuck-
	// refund scanner path.
	existing, getErr := h.repo.GetByIDForUser(r.Context(), id, userID)
	if getErr != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if existing.PaymentIntentID != nil && *existing.PaymentIntentID != "" {
		key := fmt.Sprintf("purchase-cancel-%s", id.String())
		if stripeErr := h.stripe.CancelPaymentIntentWithKey(*existing.PaymentIntentID, key); stripeErr != nil {
			h.logger.Warn("purchase: stripe cancel failed — continuing with DB cancel",
				"purchase_id", id, "payment_intent_id", *existing.PaymentIntentID, "error", stripeErr)
		}
	}

	p, err := h.repo.CancelOffer(r.Context(), id, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: cancel", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildResponse(r.Context(), p, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_request_updated", p, nil)
	h.postSystemMessage(r.Context(), p.ChatID, userID, "Buyer cancelled the purchase offer")

	go h.notifHandler.Notify(p.SellerID, models.NotificationTypePurchaseRequest,
		"Purchase offer cancelled",
		fmt.Sprintf("%s cancelled the purchase offer.", nameOr(resp.BuyerName, "The buyer")),
		&p.ChatID, &p.ID)
}

// ─── Seller: accept / decline ───────────────────────────────────────────────

// Accept — POST /api/v1/purchase-requests/{id}/accept
func (h *PurchaseRequestHandler) Accept(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if existing.SellerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrInvalidPurchaseAction)
		return
	}
	// Idempotent: already accepted → return current.
	if existing.Status != models.PurchaseStatusRequested {
		if existing.Status == models.PurchaseStatusAccepted ||
			existing.Status == models.PurchaseStatusBOSPendingSeller ||
			existing.Status == models.PurchaseStatusBOSPendingBuyer ||
			existing.Status == models.PurchaseStatusBOSSigned {
			httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), existing, userID))
			return
		}
		httputil.WriteError(w, http.StatusConflict, models.ErrInvalidPurchaseAction)
		return
	}

	car, _ := h.carRepo.GetByID(r.Context(), existing.CarID)
	seller, _ := h.userRepo.GetByID(r.Context(), existing.SellerID)
	buyer, _ := h.userRepo.GetByID(r.Context(), existing.BuyerID)

	seed := repository.BillOfSaleSeed{
		Currency:        "USD",
		SaleAmountCents: existing.OfferAmountCents,
	}
	if car != nil {
		seed.VehicleYear = car.Year
		seed.VehicleMake = car.Make
		seed.VehicleModel = car.Model
		if car.VIN.Valid {
			seed.VIN = car.VIN.String
		}
	}
	if seller != nil {
		seed.SellerName = seller.FullName()
	}
	if buyer != nil {
		seed.BuyerName = buyer.FullName()
	}

	p, err := h.repo.AcceptOffer(r.Context(), id, userID, seed)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
		h.logger.Error("purchase: accept", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	resp := h.buildResponse(r.Context(), p, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_request_updated", p, nil)
	h.postSystemMessage(r.Context(), p.ChatID, userID, "Seller accepted the offer — Bill of Sale opened for signing")

	go h.notifHandler.Notify(p.BuyerID, models.NotificationTypePurchaseRequest,
		"Offer accepted",
		fmt.Sprintf("%s accepted your purchase offer. Review and sign the Bill of Sale.", nameOr(resp.SellerName, "The seller")),
		&p.ChatID, &p.ID)
}

// Decline — POST /api/v1/purchase-requests/{id}/decline
func (h *PurchaseRequestHandler) Decline(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	var body models.DeclinePurchaseBody
	_ = httputil.DecodeJSON(r, &body)

	p, err := h.repo.DeclineOffer(r.Context(), id, userID, body.Reason)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: decline", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildResponse(r.Context(), p, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_request_updated", p, nil)
	h.postSystemMessage(r.Context(), p.ChatID, userID, "Seller declined the purchase offer")

	go h.notifHandler.Notify(p.BuyerID, models.NotificationTypePurchaseRequest,
		"Offer declined",
		fmt.Sprintf("%s declined your purchase offer.", nameOr(resp.SellerName, "The seller")),
		&p.ChatID, &p.ID)
}

// ─── Bill of Sale editing + signing ─────────────────────────────────────────

// UpdateBOS — PATCH /api/v1/purchase-requests/{id}/bos (seller)
func (h *PurchaseRequestHandler) UpdateBOS(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if userID != existing.SellerID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrInvalidPurchaseAction)
		return
	}
	var body models.UpdateBOSBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	bos, err := h.repo.UpdateBillOfSaleFields(r.Context(), id, body)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: update bos", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildBOSResponse(bos)
	httputil.WriteJSON(w, http.StatusOK, resp)
	// Broadcast so the buyer's live BoS view refreshes.
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{existing.SellerID, existing.BuyerID},
	})
}

// UpdateBOSBuyerFields — PATCH /api/v1/purchase-requests/{id}/bos/buyer-fields
func (h *PurchaseRequestHandler) UpdateBOSBuyerFields(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if userID != existing.BuyerID && userID != existing.SellerID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrInvalidPurchaseAction)
		return
	}
	var body models.UpdateBOSBuyerFieldsBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	bos, err := h.repo.UpdateBillOfSaleBuyerFields(r.Context(), id, body)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: update bos buyer fields", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildBOSResponse(bos)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{existing.SellerID, existing.BuyerID},
	})
}

// SignBOS — POST /api/v1/purchase-requests/{id}/bos/sign
// Multipart: file (PNG) + role. Reuses the accident-signature upload
// mechanic verbatim — 5 MB cap, image/png only.
func (h *PurchaseRequestHandler) SignBOS(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}

	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data"))
		return
	}
	role := strings.TrimSpace(r.FormValue("role"))
	if role != "seller" && role != "buyer" {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrInvalidRoleField)
		return
	}
	// Role must match caller identity.
	if role == "seller" && userID != existing.SellerID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrInvalidRoleField)
		return
	}
	if role == "buyer" && userID != existing.BuyerID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrInvalidRoleField)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	dir := filepath.Join(h.uploadDir, "purchases", id.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	filename := fmt.Sprintf("%s_signature_%s.png", role, uuid.New().String())
	filePath := filepath.Join(dir, filename)
	data, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	fileURL := fmt.Sprintf("/uploads/purchases/%s/%s", id.String(), filename)

	bos, p, alreadySigned, err := h.repo.MarkSignature(r.Context(), id, role, fileURL)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: sign bos", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if alreadySigned {
		// Discard the freshly-uploaded file since we didn't update the DB.
		_ = os.Remove(filePath)
	}
	resp := h.buildBOSResponse(bos)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"bill_of_sale":   resp,
		"already_signed": alreadySigned,
	})
	// Broadcast purchase + BoS updates so both sides refresh.
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{p.SellerID, p.BuyerID},
	})
	h.broadcast("purchase_request_updated", p, nil)

	// Chat system message + notification.
	if role == "seller" {
		h.postSystemMessage(r.Context(), p.ChatID, userID, "Seller signed the Bill of Sale")
	} else {
		h.postSystemMessage(r.Context(), p.ChatID, userID, "Buyer signed the Bill of Sale")
	}
	if p.Status == models.PurchaseStatusBOSSigned {
		h.postSystemMessage(r.Context(), p.ChatID, userID, "Bill of Sale fully signed — buyer can now authorize payment")
		go h.notifHandler.Notify(p.BuyerID, models.NotificationTypePurchaseRequest,
			"Bill of Sale signed",
			"Both parties signed the Bill of Sale. Authorize payment to proceed.",
			&p.ChatID, &p.ID)
	}
}

// GetBOS — GET /api/v1/purchase-requests/{id}/bos
func (h *PurchaseRequestHandler) GetBOS(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	if _, err := h.repo.GetByIDForUser(r.Context(), id, userID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	bos, err := h.repo.GetBillOfSale(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildBOSResponse(bos))
}

// ─── Payment ────────────────────────────────────────────────────────────────

// CreatePaymentIntent — POST /api/v1/purchase-requests/{id}/payment-intent
func (h *PurchaseRequestHandler) CreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if userID != p.BuyerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the buyer can authorize payment"))
		return
	}
	if p.Status != models.PurchaseStatusBOSSigned && p.Status != models.PurchaseStatusPaymentAuthorized {
		httputil.WriteError(w, http.StatusConflict, models.ErrBOSNotSigned)
		return
	}

	buyer, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	customer, err := h.stripe.FindOrCreateCustomer(buyer.Email, buyer.FullName())
	if err != nil {
		h.logger.Error("purchase: find/create customer", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create payment customer"))
		return
	}
	ek, err := h.stripe.CreateEphemeralKey(customer.ID)
	if err != nil {
		h.logger.Error("purchase: ephemeral key", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create ephemeral key"))
		return
	}

	// Manual capture — funds are held (authorized), not captured.
	idemKey := fmt.Sprintf("purchase-payment-%s", p.ID.String())
	platformFee := h.stripe.PlatformFee(p.OfferAmountCents)
	pi, err := h.stripe.CreatePaymentIntentWithOptions(
		p.OfferAmountCents, p.Currency, customer.ID, platformFee, idemKey,
		stripeService.PaymentIntentOptions{
			CaptureMethod: "manual",
			Metadata: map[string]string{
				"purchase_request_id": p.ID.String(),
				"kind":                "purchase",
			},
		},
	)
	if err != nil {
		h.logger.Error("purchase: create payment intent", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.NewAPIError("STRIPE_ERROR", "Failed to create payment"))
		return
	}
	if _, err := h.repo.RecordPaymentIntent(r.Context(), p.ID, pi.ID); err != nil {
		h.logger.Error("purchase: record payment intent", "error", err)
	}
	httputil.WriteJSON(w, http.StatusOK, models.PaymentIntentResponse{
		PaymentIntentClientSecret: pi.ClientSecret,
		PaymentIntentID:           pi.ID,
		PublishableKey:            h.stripe.PublishableKey(),
		CustomerID:                customer.ID,
		EphemeralKeySecret:        ek.Secret,
		Amount:                    p.OfferAmountCents,
		Currency:                  p.Currency,
	})
}

// SyncPayment — POST /api/v1/purchase-requests/{id}/sync-payment
// Falls back to a direct Stripe read when webhooks are delayed.
func (h *PurchaseRequestHandler) SyncPayment(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if p.PaymentIntentID == nil {
		httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), p, userID))
		return
	}
	pi, err := h.stripe.RetrievePaymentIntent(*p.PaymentIntentID)
	if err != nil {
		h.logger.Warn("purchase: sync retrieve failed", "error", err, "id", id)
		httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), p, userID))
		return
	}
	// requires_capture ⇔ manual-capture succeeded (authorized).
	if pi.Status == "requires_capture" || pi.Status == "processing" {
		updated, err := h.repo.MarkAuthorized(r.Context(), *p.PaymentIntentID)
		if err == nil && updated != nil {
			p = updated
			h.broadcast("purchase_payment_updated", p, map[string]any{"payment_status": "requires_capture"})
			h.postSystemMessage(r.Context(), p.ChatID, p.BuyerID, "Buyer authorized payment — funds are held pending inspection")
		}
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), p, userID))
}

// ─── Handover / Inspection ──────────────────────────────────────────────────

// ScheduleHandover — POST /api/v1/purchase-requests/{id}/schedule-handover
func (h *PurchaseRequestHandler) ScheduleHandover(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	var body models.ScheduleHandoverBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if strings.TrimSpace(body.HandoverLocation) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("handover_location is required"))
		return
	}
	if body.HandoverScheduledAt.IsZero() {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("handover_scheduled_at is required"))
		return
	}
	p, err := h.repo.ScheduleHandover(r.Context(), id, userID, body)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: schedule handover", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildResponse(r.Context(), p, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_handover_updated", p, map[string]any{
		"handover_location":     body.HandoverLocation,
		"handover_scheduled_at": body.HandoverScheduledAt,
	})
	h.postSystemMessage(r.Context(), p.ChatID, userID,
		fmt.Sprintf("Seller scheduled handover on %s at %s", body.HandoverScheduledAt.Format(time.RFC1123), body.HandoverLocation))

	go h.notifHandler.Notify(p.BuyerID, models.NotificationTypePurchaseHandover,
		"Handover scheduled",
		fmt.Sprintf("%s scheduled the handover. Check the chat for details.", nameOr(resp.SellerName, "The seller")),
		&p.ChatID, &p.ID)
}

// KeysHandedOver — POST /api/v1/purchase-requests/{id}/keys-handed-over
func (h *PurchaseRequestHandler) KeysHandedOver(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.KeysHandedOver(r.Context(), id, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: keys handed over", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildResponse(r.Context(), p, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_handover_updated", p, map[string]any{
		"keys_handed_over_at":    p.KeysHandedOverAt,
		"inspection_deadline_at": p.InspectionDeadlineAt,
	})
	// Piggy-back car_updated so Discover refreshes.
	h.wsHub.Broadcast(&ws.Event{
		Type: "car_updated",
		Payload: map[string]any{
			"id":                              p.CarID,
			"reserved_by_purchase_request_id": p.ID,
		},
		TargetUserIDs: []uuid.UUID{p.SellerID, p.BuyerID},
	})
	h.postSystemMessage(r.Context(), p.ChatID, userID,
		fmt.Sprintf("Seller confirmed keys handed over — buyer has %.0fh to inspect", models.PurchaseInspectionWindow.Hours()))

	go h.notifHandler.Notify(p.BuyerID, models.NotificationTypePurchaseHandover,
		"Keys handed over",
		fmt.Sprintf("You have %.0f hours to inspect the vehicle and accept or reject the sale.", models.PurchaseInspectionWindow.Hours()),
		&p.ChatID, &p.ID)
}

// InspectAccept — POST /api/v1/purchase-requests/{id}/inspect/accept
func (h *PurchaseRequestHandler) InspectAccept(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.InspectionAccept(r.Context(), id, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: inspect accept", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Capture immediately.
	captured := h.capturePayment(r.Context(), p)
	final := p
	if captured != nil {
		final = captured
	}
	resp := h.buildResponse(r.Context(), final, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_request_updated", final, nil)
	h.postSystemMessage(r.Context(), final.ChatID, userID, "Buyer accepted the vehicle — payment captured, sale complete")

	go h.notifHandler.Notify(final.SellerID, models.NotificationTypePurchasePayment,
		"Sale complete",
		fmt.Sprintf("%s accepted the vehicle. Payment has been captured.", nameOr(resp.BuyerName, "The buyer")),
		&final.ChatID, &final.ID)
}

// capturePayment runs Stripe capture + MarkCaptured. Returns the updated
// purchase row on success, nil on failure (row stays at
// inspection_accepted for a follow-up retry).
func (h *PurchaseRequestHandler) capturePayment(ctx context.Context, p *models.PurchaseRequest) *models.PurchaseRequest {
	if p.PaymentIntentID == nil || *p.PaymentIntentID == "" {
		h.logger.Error("purchase: capture with no payment intent", "id", p.ID)
		return nil
	}
	idemKey := fmt.Sprintf("purchase-capture-%s", p.ID.String())
	if _, err := h.stripe.CapturePaymentIntent(*p.PaymentIntentID, idemKey); err != nil {
		h.logger.Error("purchase: stripe capture failed", "error", err, "id", p.ID)
		return nil
	}
	updated, err := h.repo.MarkCaptured(ctx, p.ID)
	if err != nil {
		h.logger.Error("purchase: mark captured", "error", err, "id", p.ID)
		return nil
	}
	// Piggy-back car_updated → sold.
	h.wsHub.Broadcast(&ws.Event{
		Type: "car_updated",
		Payload: map[string]any{
			"id":     updated.CarID,
			"status": "sold",
		},
		TargetUserIDs: []uuid.UUID{updated.SellerID, updated.BuyerID},
	})
	return updated
}

// releaseAuth cancels the Stripe auth (pre-capture) so the hold is
// released. Used on admin-accept rejection + auth-expiry scanner.
func (h *PurchaseRequestHandler) releaseAuth(ctx context.Context, p *models.PurchaseRequest, terminal models.PurchaseRequestStatus) *models.PurchaseRequest {
	if p.PaymentIntentID != nil && *p.PaymentIntentID != "" {
		idemKey := fmt.Sprintf("purchase-cancel-%s", p.ID.String())
		if err := h.stripe.CancelPaymentIntentWithKey(*p.PaymentIntentID, idemKey); err != nil {
			// Log but continue — the row still moves to the terminal state.
			// Stripe often 400s on already-canceled intents; that's benign.
			h.logger.Warn("purchase: stripe cancel failed", "error", err, "id", p.ID)
		}
	}
	updated, err := h.repo.MarkAuthCancelled(ctx, p.ID, terminal)
	if err != nil {
		h.logger.Error("purchase: mark auth cancelled", "error", err, "id", p.ID)
		return nil
	}
	return updated
}

// InspectReject — POST /api/v1/purchase-requests/{id}/inspect/reject
func (h *PurchaseRequestHandler) InspectReject(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	var body models.SubmitRejectionBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if !body.ReasonCategory.IsValid() {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid reason_category"))
		return
	}
	explanation := strings.TrimSpace(body.Explanation)
	if len(explanation) < models.PurchaseExplanationMinLen || len(explanation) > models.PurchaseExplanationMaxLen {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError(fmt.Sprintf("explanation must be between %d and %d chars", models.PurchaseExplanationMinLen, models.PurchaseExplanationMaxLen)))
		return
	}
	// Require at least one piece of evidence — either provided ids or
	// already-uploaded rows on the placeholder rejection.
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if userID != p.BuyerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the buyer can reject the vehicle"))
		return
	}
	// Count evidence attached so far.
	rej, _ := h.repo.GetOrCreatePendingRejection(r.Context(), id)
	count, err := h.repo.CountEvidence(r.Context(), rej.ID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if count < models.PurchaseRejectionMinEvidence {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrPurchaseEvidenceRequired)
		return
	}
	body.Explanation = explanation

	rejectionOut, updated, err := h.repo.SubmitRejection(r.Context(), id, userID, body)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: submit rejection", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildResponse(r.Context(), updated, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_rejection_created", updated, map[string]any{
		"reason_category": rejectionOut.ReasonCategory,
	})
	h.postSystemMessage(r.Context(), updated.ChatID, userID,
		fmt.Sprintf("Buyer rejected the vehicle — reason: %s. DrivaBai support is reviewing.", rejectionOut.ReasonCategory))

	go h.notifHandler.Notify(updated.SellerID, models.NotificationTypePurchaseRejection,
		"Vehicle rejected",
		fmt.Sprintf("%s rejected the vehicle. DrivaBai support is reviewing.", nameOr(resp.BuyerName, "The buyer")),
		&updated.ChatID, &updated.ID)
}

// UploadEvidence — POST /api/v1/purchase-requests/{id}/rejection-evidence (multipart)
func (h *PurchaseRequestHandler) UploadEvidence(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if userID != p.BuyerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the buyer can upload rejection evidence"))
		return
	}
	if p.Status != models.PurchaseStatusAwaitingInspection && p.Status != models.PurchaseStatusInspectionRejected {
		httputil.WriteError(w, http.StatusConflict, models.ErrNotAwaitingInspection)
		return
	}

	if err := r.ParseMultipartForm(int64(models.PurchaseRejectionEvidenceMaxBytes)); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		buf := make([]byte, 512)
		file.Read(buf)
		contentType = http.DetectContentType(buf)
		file.Seek(0, 0)
	}
	validTypes := map[string]string{
		"image/jpeg":      ".jpg",
		"image/jpg":       ".jpg",
		"image/png":       ".png",
		"image/heic":      ".heic",
		"image/heif":      ".heif",
		"video/mp4":       ".mp4",
		"video/quicktime": ".mov",
		"application/pdf": ".pdf",
	}
	ext, valid := validTypes[contentType]
	if !valid {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("unsupported file type: "+contentType))
		return
	}

	rej, err := h.repo.GetOrCreatePendingRejection(r.Context(), id)
	if err != nil {
		h.logger.Error("purchase: pending rejection", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	count, _ := h.repo.CountEvidence(r.Context(), rej.ID)
	if count >= models.PurchaseRejectionEvidenceMaxFiles {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("evidence file cap reached"))
		return
	}

	dir := filepath.Join(h.uploadDir, "purchases", id.String(), "rejection")
	if err := os.MkdirAll(dir, 0755); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	fileID := uuid.New().String()
	filename := fileID + ext
	filePath := filepath.Join(dir, filename)
	data, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if int64(len(data)) > int64(models.PurchaseRejectionEvidenceMaxBytes) {
		httputil.WriteError(w, http.StatusRequestEntityTooLarge, models.NewValidationError("file too large"))
		return
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	fileURL := fmt.Sprintf("/uploads/purchases/%s/rejection/%s", id.String(), filename)

	created, err := h.repo.CreateEvidence(r.Context(), rej.ID, models.PurchaseRejectionEvidence{
		FileURL:   fileURL,
		FilePath:  filePath,
		Filename:  header.Filename,
		MimeType:  contentType,
		SizeBytes: int64(len(data)),
	})
	if err != nil {
		_ = os.Remove(filePath)
		h.logger.Error("purchase: create evidence", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, models.PurchaseRejectionEvidenceResponse{
		ID:        created.ID,
		FileURL:   h.urlSigner.Sign(created.FileURL),
		Filename:  created.Filename,
		MimeType:  created.MimeType,
		SizeBytes: created.SizeBytes,
		CreatedAt: models.RFC3339Time(created.CreatedAt),
	})
}

// WithdrawRejection — POST /api/v1/purchase-requests/{id}/rejection/withdraw
func (h *PurchaseRequestHandler) WithdrawRejection(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.WithdrawRejection(r.Context(), id, userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: withdraw rejection", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	resp := h.buildResponse(r.Context(), p, userID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.broadcast("purchase_request_updated", p, nil)
	h.postSystemMessage(r.Context(), p.ChatID, userID, "Buyer withdrew the rejection — sale proceeding")
}

// ─── Shared reads ───────────────────────────────────────────────────────────

// Get — GET /api/v1/purchase-requests/{id}
func (h *PurchaseRequestHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), p, userID))
}

// ListForChat — GET /api/v1/chats/{chatId}/purchase-requests
func (h *PurchaseRequestHandler) ListForChat(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	chatID, err := uuid.Parse(chi.URLParam(r, "chatId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid chat id"))
		return
	}
	rows, err := h.repo.ListForChat(r.Context(), chatID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Filter to participants only. IsParticipant would be cleaner but the
	// simple check works: seller or buyer.
	out := make([]models.PurchaseRequestResponse, 0, len(rows))
	for i := range rows {
		p := rows[i]
		if userID != p.SellerID && userID != p.BuyerID {
			continue
		}
		out = append(out, h.buildResponse(r.Context(), &p, userID))
	}
	httputil.WriteJSON(w, http.StatusOK, models.PurchaseRequestsListResponse{PurchaseRequests: out})
}

// Today — GET /api/v1/today/purchase-requests
//
// Returns non-terminal purchase rows (plus a 15-minute grace on terminal)
// where the caller is either the buyer or seller. Fuels the Today tab
// aggregation on both iOS view models. Payload matches ListForChat so
// the client can reuse its decoder.
func (h *PurchaseRequestHandler) Today(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	rows, err := h.repo.ListActiveForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("purchase: today list", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	out := make([]models.PurchaseRequestResponse, 0, len(rows))
	for i := range rows {
		p := rows[i]
		out = append(out, h.buildResponse(r.Context(), &p, userID))
	}
	httputil.WriteJSON(w, http.StatusOK, models.PurchaseRequestsListResponse{PurchaseRequests: out})
}

// ─── Admin ─────────────────────────────────────────────────────────────────

// AdminList — GET /api/v1/admin/purchase-requests
func (h *PurchaseRequestHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	page, _ := parsePageAdmin(r.URL.Query().Get("page"))
	limit, _ := parsePageAdmin(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	rows, total, err := h.repo.AdminList(r.Context(), r.URL.Query().Get("status"), page, limit)
	if err != nil {
		h.logger.Error("purchase: admin list", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	items := make([]models.PurchaseRequestResponse, 0, len(rows))
	for i := range rows {
		p := rows[i]
		items = append(items, h.buildResponse(r.Context(), &p, p.SellerID))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// AdminGet — GET /api/v1/admin/purchase-requests/{id}
func (h *PurchaseRequestHandler) AdminGet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), p, p.SellerID))
}

// AdminListRejections — GET /api/v1/admin/purchase-rejections
func (h *PurchaseRequestHandler) AdminListRejections(w http.ResponseWriter, r *http.Request) {
	page, _ := parsePageAdmin(r.URL.Query().Get("page"))
	limit, _ := parsePageAdmin(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	rows, total, err := h.repo.AdminListRejections(r.Context(), r.URL.Query().Get("status"), page, limit)
	if err != nil {
		h.logger.Error("purchase: admin list rejections", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	items := make([]models.PurchaseRejectionResponse, 0, len(rows))
	for i := range rows {
		rej := rows[i]
		if resp := h.buildRejectionResponse(r.Context(), &rej); resp != nil {
			items = append(items, *resp)
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// AdminResolveRejection — POST /api/v1/admin/purchase-rejections/{id}/resolve
func (h *PurchaseRequestHandler) AdminResolveRejection(w http.ResponseWriter, r *http.Request) {
	adminID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body models.ResolvePurchaseRejectionBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	res := strings.ToLower(strings.TrimSpace(body.Resolution))
	if res != "accept" && res != "uphold" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("resolution must be 'accept' or 'uphold'"))
		return
	}
	rej, p, err := h.repo.ResolveRejection(r.Context(), id, adminID, res, body.Note)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			httputil.WriteError(w, statusForPurchaseErr(apiErr), apiErr)
			return
		}
		h.logger.Error("purchase: resolve rejection", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Follow-up Stripe action.
	final := p
	switch res {
	case "accept":
		if released := h.releaseAuth(r.Context(), p, models.PurchaseStatusRejectedRefunded); released != nil {
			final = released
		}
		h.postSystemMessage(r.Context(), final.ChatID, adminID,
			"DrivaBai support accepted the rejection — hold released, sale cancelled")
	case "uphold":
		if captured := h.capturePayment(r.Context(), p); captured != nil {
			final = captured
		}
		h.postSystemMessage(r.Context(), final.ChatID, adminID,
			"DrivaBai support upheld the sale — payment captured, sale complete")
	}

	// Build a rejection response with fresh evidence signatures.
	rejResp := h.buildRejectionResponse(r.Context(), rej)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"rejection":        rejResp,
		"purchase_request": h.buildResponse(r.Context(), final, adminID),
	})
	h.broadcast("purchase_request_updated", final, nil)
	go h.notifHandler.Notify(final.BuyerID, models.NotificationTypePurchaseRejection,
		"Rejection resolved", fmt.Sprintf("DrivaBai support %sed your rejection.", res), &final.ChatID, &final.ID)
	go h.notifHandler.Notify(final.SellerID, models.NotificationTypePurchaseRejection,
		"Rejection resolved", fmt.Sprintf("DrivaBai support %sed the buyer's rejection.", res), &final.ChatID, &final.ID)
}

// AdminRetryRefund — POST /api/v1/admin/purchase-requests/{id}/retry-refund
// Admin-triggered kick when refund_status is stuck at 'failed'.
func (h *PurchaseRequestHandler) AdminRetryRefund(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	p, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	// Refunds only apply on the post-capture path (never on manual-cancel
	// authorizations, which release funds automatically). Refuse here so
	// the admin doesn't accidentally issue a $0 refund on an authorized-
	// but-not-captured intent.
	if p.PaymentIntentID == nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("no payment intent recorded"))
		return
	}
	if p.Status != models.PurchaseStatusCompleted && p.Status != models.PurchaseStatusRejectedUpheld {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("refunds are only supported on captured payments"))
		return
	}
	idemKey := fmt.Sprintf("purchase-refund-%s", p.ID.String())
	refund, err := h.stripe.CreateRefund(*p.PaymentIntentID, idemKey, "requested_by_customer", p.OfferAmountCents)
	if err != nil {
		h.logger.Error("purchase: retry refund", "error", err, "id", id)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "Refund attempt failed"))
		return
	}
	status := models.VehicleReturnRefundPending
	if refund.Status == "succeeded" {
		status = models.VehicleReturnRefundSucceeded
	}
	updated, err := h.repo.RecordRefund(r.Context(), p.ID, refund.ID, status)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), updated, updated.SellerID))
}

// ─── Webhook fragment ───────────────────────────────────────────────────────

// HandleStripeEvent is the purchase-side branch of the shared Stripe webhook.
// Called by LeaseRequestHandler.HandleWebhook when the intent's metadata
// carries `kind=purchase` or `purchase_request_id`. Idempotent: repeat
// events for the same PI advance state monotonically or no-op.
func (h *PurchaseRequestHandler) HandleStripeEvent(ctx context.Context, eventType, intentID string) {
	switch eventType {
	case "payment_intent.amount_capturable_updated":
		p, err := h.repo.MarkAuthorized(ctx, intentID)
		if err == nil && p != nil {
			h.broadcast("purchase_payment_updated", p, map[string]any{"payment_status": "requires_capture"})
			h.postSystemMessage(ctx, p.ChatID, p.BuyerID, "Buyer authorized payment — funds are held pending inspection")
			go h.notifHandler.Notify(p.SellerID, models.NotificationTypePurchasePayment,
				"Payment authorized",
				"The buyer's payment is authorized. Schedule the vehicle handover.",
				&p.ChatID, &p.ID)
		}
	case "payment_intent.succeeded":
		p, err := h.repo.GetByPaymentIntentID(ctx, intentID)
		if err == nil && p != nil {
			if updated, err := h.repo.MarkCaptured(ctx, p.ID); err == nil {
				h.broadcast("purchase_payment_updated", updated, map[string]any{"payment_status": "succeeded"})
			}
		}
	case "payment_intent.canceled":
		p, err := h.repo.GetByPaymentIntentID(ctx, intentID)
		if err == nil && p != nil {
			terminal := models.PurchaseStatusExpiredAuth
			if p.Status == models.PurchaseStatusInspectionRejected {
				terminal = models.PurchaseStatusRejectedRefunded
			}
			if updated, err := h.repo.MarkAuthCancelled(ctx, p.ID, terminal); err == nil {
				h.broadcast("purchase_payment_updated", updated, map[string]any{"payment_status": "canceled"})
			}
		}
	}
}

// ─── Background scanners ────────────────────────────────────────────────────

// StartExpiryScanner runs the offer-expire + auth-expire loops on the same
// ticker. Cancelled via ctx on shutdown.
func (h *PurchaseRequestHandler) StartExpiryScanner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	h.logger.Info("purchase expiry scanner started", "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("purchase expiry scanner stopped")
			return
		case <-ticker.C:
			h.runOfferExpiry(ctx)
			h.runAuthExpiry(ctx)
		}
	}
}

func (h *PurchaseRequestHandler) runOfferExpiry(ctx context.Context) {
	rows, err := h.repo.ListOfferExpired(ctx, 50)
	if err != nil {
		h.logger.Error("purchase: list offer expired", "error", err)
		return
	}
	for i := range rows {
		p, err := h.repo.ExpireIfStale(ctx, rows[i].ID)
		if err != nil {
			continue
		}
		h.broadcast("purchase_request_updated", p, nil)
		h.postSystemMessage(ctx, p.ChatID, p.BuyerID, "Purchase offer expired")
	}
}

func (h *PurchaseRequestHandler) runAuthExpiry(ctx context.Context) {
	rows, err := h.repo.ListAuthExpired(ctx, 50)
	if err != nil {
		h.logger.Error("purchase: list auth expired", "error", err)
		return
	}
	for i := range rows {
		released := h.releaseAuth(ctx, &rows[i], models.PurchaseStatusExpiredAuth)
		if released == nil {
			continue
		}
		h.broadcast("purchase_request_updated", released, nil)
		h.postSystemMessage(ctx, released.ChatID, released.BuyerID, "Payment authorization expired — sale cancelled")
	}
}

// ─── Small utilities ────────────────────────────────────────────────────────

func (h *PurchaseRequestHandler) parseAuthed(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid purchase request id"))
		return uuid.Nil, uuid.Nil, false
	}
	return userID, id, true
}

func statusForPurchaseErr(apiErr *models.APIError) int {
	switch apiErr.Code {
	case models.ErrCodePurchaseRequestNotFound, models.ErrCodePurchaseRejectionNotFound:
		return http.StatusNotFound
	case models.ErrCodeCannotBuyOwnCar, models.ErrCodeInvalidRoleField:
		return http.StatusForbidden
	case models.ErrCodeCarNotForSale, models.ErrCodeCarSold, models.ErrCodeDuplicatePurchase,
		models.ErrCodeInvalidPurchaseAction, models.ErrCodeBOSLocked,
		models.ErrCodeBOSNotSigned, models.ErrCodeAlreadySigned,
		models.ErrCodeNotAwaitingInspection, models.ErrCodeNotHandoverScheduled,
		models.ErrCodePurchaseNotCancellable:
		return http.StatusConflict
	case models.ErrCodePurchaseOfferTooLow, models.ErrCodePurchaseEvidenceRequired,
		models.ErrCodeInvalidInput:
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func parsePageAdmin(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(ch-'0')
		if n > 100000 {
			return 100000, nil
		}
	}
	return n, nil
}
