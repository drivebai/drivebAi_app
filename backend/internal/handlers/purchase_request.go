package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png" // registers the PNG decoder used by validateSignatureUpload
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/billofsale"
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
	// salesDisabled refuses new purchase offers while the sale flow is
	// switched off (no seller payout path yet — audit M1).
	salesDisabled bool
	// salesAllowlist keeps the flow open to a named pilot group while the
	// switch is on. See SetSalesAllowlist.
	salesAllowlist map[uuid.UUID]struct{}
	// payoutH pays the SELLER when a sale completes. Optional: nil means the
	// sale still completes and the ledger row is simply never written, which
	// is the pre-seller-payout behaviour.
	payoutH *PayoutHandler
	// payoutRepo gates acceptance on the seller being able to receive money.
	// Optional: nil disables the gate, which is the pre-seller-payout
	// behaviour.
	payoutRepo *repository.PayoutRepository
}

// SetPayoutRepository wires the payout account lookup used by the
// acceptance-time onboarding gate.
func (h *PurchaseRequestHandler) SetPayoutRepository(p *repository.PayoutRepository) {
	h.payoutRepo = p
}

// SetPayoutHandler wires the payout engine so a completed sale pays its
// seller. Setter, per the house pattern.
func (h *PurchaseRequestHandler) SetPayoutHandler(p *PayoutHandler) {
	h.payoutH = p
}

// SetSalesDisabled wires the DISABLE_CAR_SALES kill switch (house setter
// pattern).
func (h *PurchaseRequestHandler) SetSalesDisabled(disabled bool) { h.salesDisabled = disabled }

// SetSalesAllowlist names the users for whom the sale flow stays open while
// DISABLE_CAR_SALES is on: an internal pilot behind a public kill switch.
// BOTH parties to a sale must be on it — a listed buyer offering on a
// stranger's car would strand at the seller's first refused step.
func (h *PurchaseRequestHandler) SetSalesAllowlist(ids []uuid.UUID) {
	h.salesAllowlist = make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		h.salesAllowlist[id] = struct{}{}
	}
}

// salesOpenFor reports whether the sale flow is open to this user: either
// the switch is off for everyone, or they are on the pilot allowlist.
func (h *PurchaseRequestHandler) salesOpenFor(userID uuid.UUID) bool {
	if !h.salesDisabled {
		return true
	}
	_, ok := h.salesAllowlist[userID]
	return ok
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
		ID:                  b.ID,
		PurchaseRequestID:   b.PurchaseRequestID,
		VehicleYear:         b.VehicleYear,
		VehicleMake:         b.VehicleMake,
		VehicleModel:        b.VehicleModel,
		VIN:                 b.VIN,
		SaleAmountCents:     b.SaleAmountCents,
		Currency:            b.Currency,
		TermsConditions:     b.TermsConditions,
		SellerName:          b.SellerName,
		SellerAddress:       b.SellerAddress,
		SellerAddressLat:    b.SellerAddressLat,
		SellerAddressLng:    b.SellerAddressLng,
		SellerSignedAt:      models.NewRFC3339TimePtr(b.SellerSignedAt),
		BuyerName:           b.BuyerName,
		BuyerAddress:        b.BuyerAddress,
		BuyerAddressLat:     b.BuyerAddressLat,
		BuyerAddressLng:     b.BuyerAddressLng,
		BuyerSignedAt:       models.NewRFC3339TimePtr(b.BuyerSignedAt),
		TitleCondition:      b.TitleCondition,
		TitleConditionOther: b.TitleConditionOther,
		FinalizedAt:         models.NewRFC3339TimePtr(b.FinalizedAt),
		Locked:              b.SellerSigned() || b.BuyerSigned(),
		FullySigned:         b.FullySigned(),
		CreatedAt:           models.RFC3339Time(b.CreatedAt),
		UpdatedAt:           models.RFC3339Time(b.UpdatedAt),
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

// enrichBOSDocuments joins the title car_document + each party's driver-license
// document onto a BoS response. Title URL + ID URLs are SIGNED private URLs;
// ID docs are show-if-on-file (nil when absent — never a hard requirement).
// title_uploaded is the presence flag the buyer-Accept gate keys on.
func (h *PurchaseRequestHandler) enrichBOSDocuments(ctx context.Context, resp *models.PurchaseBillOfSaleResponse, carID, sellerID, buyerID uuid.UUID) {
	if resp == nil {
		return
	}
	if titleURL, err := h.repo.GetCarTitleDocumentURL(ctx, carID); err == nil && titleURL != nil {
		signed := h.urlSigner.Sign(*titleURL)
		resp.TitleDocumentURL = &signed
		resp.TitleUploaded = true
	}
	if sellerLic, err := h.repo.GetUserLicenseDocumentURL(ctx, sellerID); err == nil && sellerLic != nil {
		signed := h.urlSigner.Sign(*sellerLic)
		resp.SellerIDDocumentURL = &signed
	}
	if buyerLic, err := h.repo.GetUserLicenseDocumentURL(ctx, buyerID); err == nil && buyerLic != nil {
		signed := h.urlSigner.Sign(*buyerLic)
		resp.BuyerIDDocumentURL = &signed
	}
}

// buildChecklistResponse maps a persisted inspection checklist to its response
// shape. Returns nil for a nil checklist (tolerated for pre-migration rows).
func buildChecklistResponse(c *models.PurchaseInspectionChecklist) *models.PurchaseInspectionChecklistResponse {
	if c == nil {
		return nil
	}
	return &models.PurchaseInspectionChecklistResponse{
		VINMatches:            c.VINMatches,
		OdometerReviewed:      c.OdometerReviewed,
		ExteriorOK:            c.ExteriorOK,
		InteriorOK:            c.InteriorOK,
		MechanicalTestDriveOK: c.MechanicalTestDriveOK,
		TitleReviewed:         c.TitleReviewed,
		KeysHandedOver:        c.KeysHandedOver,
		BuyerUnderstandsAcceptanceCompletesPayment: c.BuyerUnderstandsAcceptanceCompletesPayment,
		CreatedAt: models.RFC3339Time(c.CreatedAt),
	}
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
	car, _ := h.carRepo.GetByID(ctx, p.CarID)
	if car != nil {
		resp.CarTitle = car.Title
		// Fall back to the live car row for the structured vehicle fields; the
		// BoS row (seeded at Accept) overrides below when present.
		resp.VehicleYear = car.Year
		resp.VehicleMake = car.Make
		resp.VehicleModel = car.Model
		if car.VIN.Valid {
			resp.VehicleVIN = car.VIN.String
		}
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
		h.enrichBOSDocuments(ctx, resp.BillOfSale, p.CarID, p.SellerID, p.BuyerID)
		// The BoS snapshot is the authoritative legal record — prefer it over
		// the live car row for the top-level vehicle fields.
		resp.VehicleYear = bos.VehicleYear
		resp.VehicleMake = bos.VehicleMake
		resp.VehicleModel = bos.VehicleModel
		resp.VehicleVIN = bos.VIN
	}
	if rej, err := h.repo.GetRejection(ctx, p.ID); err == nil && rej != nil {
		resp.Rejection = h.buildRejectionResponse(ctx, rej)
	}
	if cl, err := h.repo.GetInspectionChecklist(ctx, p.ID); err == nil && cl != nil {
		resp.InspectionChecklist = buildChecklistResponse(cl)
	}
	return resp
}

// buildAdminResponse wraps buildResponse and attaches the admin-only detail
// block (DESIGN SPEC item 24): car specs + all photos + SIGNED car documents,
// buyer/seller contact + address, buyer/seller SIGNED ID documents (show-if-
// present), the inspection checklist, and refund/cancellation reasons. Reuses
// the existing urlSigner. Callers MUST gate this to the admin role.
func (h *PurchaseRequestHandler) buildAdminResponse(ctx context.Context, p *models.PurchaseRequest) models.PurchaseRequestResponse {
	resp := h.buildResponse(ctx, p, p.SellerID)
	detail := &models.PurchaseAdminDetailResponse{
		CarPhotos:           []string{},
		CarDocuments:        []models.PurchaseAdminCarDocumentResponse{},
		RefundFailureReason: p.RefundFailureReason,
		CancellationReason:  p.CancellationReason,
		InspectionChecklist: resp.InspectionChecklist,
	}
	if car, err := h.carRepo.GetByID(ctx, p.CarID); err == nil && car != nil {
		detail.CarMake = car.Make
		detail.CarModel = car.Model
		detail.CarYear = car.Year
		if car.VIN.Valid {
			detail.CarVIN = car.VIN.String
		}
	}
	// Car photos are PUBLIC (Sign passes them through); car documents are
	// PRIVATE (Sign appends ?sig=&exp=).
	if cover, err := h.repo.GetCarCoverPhotoURL(ctx, p.CarID); err == nil && cover != nil {
		signed := h.urlSigner.Sign(*cover)
		detail.CoverPhotoURL = &signed
	}
	if photos, err := h.repo.GetCarPhotoURLs(ctx, p.CarID); err == nil {
		for _, u := range photos {
			detail.CarPhotos = append(detail.CarPhotos, h.urlSigner.Sign(u))
		}
	}
	if docs, err := h.repo.GetCarDocumentBriefs(ctx, p.CarID); err == nil {
		for _, d := range docs {
			detail.CarDocuments = append(detail.CarDocuments, models.PurchaseAdminCarDocumentResponse{
				DocumentType: d.DocumentType,
				FileName:     d.FileName,
				FileURL:      h.urlSigner.Sign(d.FileURL),
			})
		}
	}
	if buyer, err := h.userRepo.GetByID(ctx, p.BuyerID); err == nil && buyer != nil {
		detail.BuyerEmail = buyer.Email
		detail.BuyerPhone = buyer.Phone
	}
	if seller, err := h.userRepo.GetByID(ctx, p.SellerID); err == nil && seller != nil {
		detail.SellerEmail = seller.Email
		detail.SellerPhone = seller.Phone
	}
	if resp.BillOfSale != nil {
		// Addresses live on the BoS; ID doc URLs were already signed by
		// enrichBOSDocuments — reuse them rather than re-joining/re-signing.
		detail.BuyerAddress = resp.BillOfSale.BuyerAddress
		detail.SellerAddress = resp.BillOfSale.SellerAddress
		detail.BuyerIDDocumentURL = resp.BillOfSale.BuyerIDDocumentURL
		detail.SellerIDDocumentURL = resp.BillOfSale.SellerIDDocumentURL
	} else {
		// No BoS yet (offer pre-accept): join licenses directly.
		if sellerLic, err := h.repo.GetUserLicenseDocumentURL(ctx, p.SellerID); err == nil && sellerLic != nil {
			signed := h.urlSigner.Sign(*sellerLic)
			detail.SellerIDDocumentURL = &signed
		}
		if buyerLic, err := h.repo.GetUserLicenseDocumentURL(ctx, p.BuyerID); err == nil && buyerLic != nil {
			signed := h.urlSigner.Sign(*buyerLic)
			detail.BuyerIDDocumentURL = &signed
		}
	}
	resp.AdminDetail = detail
	return resp
}

func (h *PurchaseRequestHandler) broadcast(eventType string, p *models.PurchaseRequest, extras map[string]any) {
	payload := map[string]any{
		"id":                 p.ID,
		"car_id":             p.CarID,
		"seller_id":          p.SellerID,
		"buyer_id":           p.BuyerID,
		"status":             p.Status,
		"chat_id":            p.ChatID,
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

// notifyPurchaseCounterparty writes a notification row + push to whichever of
// buyer/seller is NOT the actor. The related_chat_id + related_lease_request_id
// slots on the notification row carry the chat + purchase-request ids so the
// iOS bell + APNs deep link can route back into the same purchase card.
//
// The lease_request_id column is reused as the generic "related resource id"
// slot for purchase pushes to avoid a schema fork; iOS DeepLinkRouter switches
// on the `type` string first so a purchase_* type + a UUID in that field
// resolves to the purchase branch.
//
// Runs in its own goroutine so the caller (an HTTP handler or a scanner tick)
// never blocks on repo/push work. The push fan-out is already spawned inside
// NotificationHandler.Notify (per the notifications.go contract) so this
// wrapper deliberately does NOT double-spawn a push — it only pushes the
// Notify call itself onto a background goroutine.
func (h *PurchaseRequestHandler) notifyPurchaseCounterparty(
	p *models.PurchaseRequest,
	actorID uuid.UUID,
	notifType models.NotificationType,
	title, body string,
) {
	if h.notifHandler == nil || p == nil {
		return
	}
	// The recipient is whichever party is NOT the actor. If the actor id
	// matches neither (e.g. system-triggered from the expiry scanner), we
	// fall back to notifying the buyer — callers that need to reach both
	// parties should invoke this twice, once per party.
	var recipient uuid.UUID
	switch actorID {
	case p.SellerID:
		recipient = p.BuyerID
	case p.BuyerID:
		recipient = p.SellerID
	default:
		recipient = p.BuyerID
	}
	if recipient == uuid.Nil {
		return
	}
	chatID := p.ChatID
	purchaseID := p.ID
	go h.notifHandler.Notify(recipient, notifType, title, body, &chatID, &purchaseID)
}

// notifyPurchaseParty writes a notification row + push to a specific user id
// (not derived from actor). Used when both parties must be notified (admin
// resolutions, offer expiry, refund completion) or when the sender/recipient
// mapping isn't a simple actor→counterparty flip.
func (h *PurchaseRequestHandler) notifyPurchaseParty(
	p *models.PurchaseRequest,
	recipient uuid.UUID,
	notifType models.NotificationType,
	title, body string,
) {
	if h.notifHandler == nil || p == nil || recipient == uuid.Nil {
		return
	}
	chatID := p.ChatID
	purchaseID := p.ID
	go h.notifHandler.Notify(recipient, notifType, title, body, &chatID, &purchaseID)
}

// rejectionResolutionVerb yields grammatically correct past-tense forms for
// the admin rejection resolution copy. Previously we used fmt.Sprintf("%sed",
// resolution) which produced "upholded" for the "uphold" resolution.
func rejectionResolutionVerb(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "accept":
		return "accepted"
	case "uphold":
		return "upheld"
	default:
		return resolution
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
	// Sales kill switch (audit M1), with the pilot allowlist: refuse at the
	// front door unless the BUYER is allowed. The seller is checked once
	// the car is loaded, below. Rentals are unaffected.
	if h.refuseIfSalesDisabled(w, userID) {
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
	if err != nil || car == nil || car.IsArchived() {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("CAR_NOT_FOUND", "Car not found"))
		return
	}
	if car.OwnerID == userID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrCannotBuyOwnCar)
		return
	}
	// The SELLER must be on the pilot too, or this sale strands at their
	// first refused step (scheduling the handover).
	if h.refuseIfSalesDisabled(w, car.OwnerID) {
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
	// D4: owners may configure is_for_sale anytime (even mid-rental), but a
	// purchase can't START while a driver physically holds the car. This is
	// the single choke point — blocking the toggle client-side would race
	// with rental start anyway. Reliable after the migration-000032 status
	// backfill.
	if car.Status == models.CarStatusRented {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError(
			models.ErrCodeCarCurrentlyRented, "This car is currently rented and can't be purchased right now"))
		return
	}
	// Friendly duplicate check before the unique-index blows.
	if existing, err := h.repo.GetActiveByCarAndBuyer(r.Context(), carID, userID); err == nil && existing != nil {
		httputil.WriteError(w, http.StatusConflict, models.ErrDuplicatePurchase)
		return
	}
	// Reservation guard: once ANY buyer's purchase is past acceptance and not
	// terminal, no other buyer may open a new offer — otherwise buyer B can
	// authorize a payment hold on a car mid-sale to buyer A. Self-releasing:
	// the predicate is computed from purchase statuses, so a declined/
	// cancelled/expired/refunded purchase frees the car with no cleanup step.
	// (Checked AFTER the duplicate check so the original buyer still gets
	// their own DUPLICATE_ACTIVE_REQUEST, not a misleading sale-in-progress.)
	if blocked, err := h.repo.HasBlockingPurchase(r.Context(), carID, uuid.Nil); err != nil {
		h.logger.Error("purchase: reservation check", "car_id", carID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	} else if blocked {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarSaleInProgress)
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

	h.notifyPurchaseCounterparty(created, userID, models.NotificationTypePurchaseRequest,
		"New purchase offer",
		fmt.Sprintf("%s offered %s for %s", nameOr(resp.BuyerName, "A buyer"), formatMoney(created.OfferAmountCents), carTitleOr(resp.CarTitle)))
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

	h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseRequest,
		"Purchase offer cancelled",
		fmt.Sprintf("%s cancelled the purchase offer.", nameOr(resp.BuyerName, "The buyer")))
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

	// Payout readiness, gated HERE and nowhere else.
	//
	// Not at listing: that would kill supply for sellers who are still
	// deciding. Not at completion: by then the buyer has paid and taken the
	// car, and escrowing tens of thousands of dollars with no timeline is a
	// far worse failure than a week's rent sitting in escrow. Accepting an
	// offer is the moment the seller commits to sell, and it is before the
	// buyer has paid anything — so "you must be able to receive money before
	// you agree to sell" costs nobody anything but the seller's own setup.
	if h.payoutRepo != nil {
		accountID, status, aerr := h.payoutRepo.GetPayoutAccount(r.Context(), userID)
		if aerr != nil {
			h.logger.Error("purchase.accept: payout account lookup", "error", aerr, "seller_id", userID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if accountID == nil || status != models.PayoutAccountReady {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYOUT_SETUP_REQUIRED",
				"Finish payout setup before accepting an offer — otherwise we'd take the buyer's money with no way to pay you. Open Earnings & payouts in your profile."))
			return
		}
	}

	// Seller-side reservation guard (mirror of Create's): accepting offer B
	// while offer A is already past acceptance would run two concurrent
	// sales of one car. The row being accepted is excluded from the check.
	if blocked, err := h.repo.HasBlockingPurchase(r.Context(), existing.CarID, existing.ID); err != nil {
		h.logger.Error("purchase.accept: reservation check", "purchase_id", id, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	} else if blocked {
		httputil.WriteError(w, http.StatusConflict, models.ErrCarSaleInProgress)
		return
	}

	// Fail loud on car-lookup failure. Previously we swallowed the error
	// with `car, _ :=` and fell through with a nil car, which silently
	// left year/make/model/vin as Go zero-values (0, "", "", "") — the
	// wizard's Review step then showed `—` for every seeded field. If the
	// car row genuinely disappeared, return a 409 so the seller sees a
	// concrete "Car no longer exists" rather than an opaque bad seed.
	car, err := h.carRepo.GetByID(r.Context(), existing.CarID)
	if err != nil {
		h.logger.Error("purchase.accept: car lookup failed", "purchase_id", id, "car_id", existing.CarID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if car == nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CAR_MISSING", "Car no longer exists"))
		return
	}
	seller, _ := h.userRepo.GetByID(r.Context(), existing.SellerID)
	buyer, _ := h.userRepo.GetByID(r.Context(), existing.BuyerID)

	seed := repository.BillOfSaleSeed{
		Currency:        "USD",
		SaleAmountCents: existing.OfferAmountCents,
		VehicleYear:     car.Year,
		VehicleMake:     car.Make,
		VehicleModel:    car.Model,
		TermsConditions: models.DefaultBOSTerms,
	}
	if car.VIN.Valid {
		seed.VIN = car.VIN.String
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

	h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseRequest,
		"Offer accepted",
		fmt.Sprintf("%s accepted your purchase offer for %s. Review and sign the Bill of Sale.", nameOr(resp.SellerName, "The seller"), carTitleOr(resp.CarTitle)))
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

	h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseRequest,
		"Offer declined",
		fmt.Sprintf("%s declined your purchase offer.", nameOr(resp.SellerName, "The seller")))
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
	h.enrichBOSDocuments(r.Context(), resp, existing.CarID, existing.SellerID, existing.BuyerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	// Broadcast so the buyer's live BoS view refreshes.
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{existing.SellerID, existing.BuyerID},
	})
	// Chat trail + counterparty notification so the buyer knows the
	// seller touched the document.
	h.postSystemMessage(r.Context(), existing.ChatID, userID, "Seller updated the Bill of Sale")
	h.notifyPurchaseCounterparty(existing, userID, models.NotificationTypePurchaseRequest,
		"Bill of Sale updated",
		"The seller updated the Bill of Sale. Review the changes.")
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
	// Buyer-only. The prior guard admitted both sides ("!= buyer AND !=
	// seller"), letting the seller rewrite buyer_name / buyer_address
	// before the buyer signed — and the counterparty notification then
	// fired at the seller (the actor), never reaching the buyer. Field
	// ownership per DESIGN SPEC §B.
	if userID != existing.BuyerID {
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
	h.enrichBOSDocuments(r.Context(), resp, existing.CarID, existing.SellerID, existing.BuyerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{existing.SellerID, existing.BuyerID},
	})
	h.postSystemMessage(r.Context(), existing.ChatID, userID, "Buyer updated the Bill of Sale")
	h.notifyPurchaseCounterparty(existing, userID, models.NotificationTypePurchaseRequest,
		"Bill of Sale updated",
		"The buyer updated the Bill of Sale. Review the changes.")
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

	// Required-address gate (DESIGN SPEC items 15/16): the signer's OWN address
	// string must be non-empty before they can sign. Never blocks on the
	// counterparty's address. Checked before the file touches disk.
	curBOS, err := h.repo.GetBillOfSale(r.Context(), id)
	if err != nil || curBOS == nil {
		httputil.WriteError(w, http.StatusConflict, models.ErrInvalidPurchaseAction)
		return
	}
	if role == "seller" && strings.TrimSpace(curBOS.SellerAddress) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrSellerAddressRequired)
		return
	}
	if role == "buyer" && strings.TrimSpace(curBOS.BuyerAddress) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrBuyerAddressRequired)
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
	// Bound the read: ParseMultipartForm's 5 MB argument is only the in-memory
	// buffer, not a hard cap on the part's size.
	data, err := io.ReadAll(io.LimitReader(file, maxSignatureUploadBytes+1))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Reject anything that isn't a small, well-formed PNG *before* it reaches
	// the filesystem. The PDF renderer parses these images; a file with valid
	// PNG magic but a malformed header can make the PDF library panic instead
	// of erroring, and generation runs on a detached goroutine where a panic
	// would kill the process. Validate at the door, not just at render time.
	if err := validateSignatureUpload(data); err != nil {
		httputil.WriteError(w, http.StatusBadRequest,
			models.NewValidationError("signature must be a PNG image"))
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
	// Return the raw BillOfSaleResponse so iOS can decode it with the same
	// model it uses for GET /bos and PATCH /bos. The prior envelope
	// (`{"bill_of_sale": ..., "already_signed": ...}`) broke iOS decode.
	// `already_signed` was informational only — iOS can detect a no-op
	// sign by comparing seller_signed_at / buyer_signed_at to what it
	// already had.
	resp := h.buildBOSResponse(bos)
	h.enrichBOSDocuments(r.Context(), resp, p.CarID, p.SellerID, p.BuyerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	// Broadcast purchase + BoS updates so both sides refresh.
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{p.SellerID, p.BuyerID},
	})
	h.broadcast("purchase_request_updated", p, nil)

	// Chat system message + notification.
	// Only post the per-role signed message the first time each role
	// signs — a repeat multipart hit (alreadySigned) shouldn't spam the
	// chat with duplicate rows or re-notify the counterparty.
	if !alreadySigned {
		if role == "seller" {
			h.postSystemMessage(r.Context(), p.ChatID, userID, "Seller signed the Bill of Sale")
			h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseRequest,
				"Seller signed",
				"The seller signed the Bill of Sale. Sign to proceed to payment.")
		} else {
			h.postSystemMessage(r.Context(), p.ChatID, userID, "Buyer signed the Bill of Sale")
			h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseRequest,
				"Buyer signed",
				"The buyer signed the Bill of Sale.")
		}
	}
	// State-transition notification when the second signature completes the
	// document — always fires at the buyer (who must next authorize payment).
	// This is not an "echo" of the buyer's own sign, it's a distinct
	// transition into the "payment required" state, so we let it fire even
	// when the buyer is the actor.
	if p.Status == models.PurchaseStatusBOSSigned && !alreadySigned {
		h.postSystemMessage(r.Context(), p.ChatID, userID, "Bill of Sale completed. Buyer can proceed to payment.")
		h.notifyPurchaseParty(p, p.BuyerID, models.NotificationTypePurchasePayment,
			"Bill of Sale signed",
			"Both parties have signed. Authorize payment to continue.")
		// Generate the finalized Bill-of-Sale PDF in a detached goroutine —
		// AFTER the response is written and the WS broadcast fired above. The
		// signer must not wait on PDF layout + disk I/O. Signatures are
		// already durably committed by MarkSignature, so a PDF failure never
		// loses them; clients refresh on the purchase_bill_of_sale_updated WS
		// event the goroutine broadcasts on success, or self-heal via the
		// retry endpoint / lazy GetBOS finalize. Use context.Background()
		// because the request context is cancelled once we return.
		go h.finalizeBillOfSale(context.Background(), p.ID, p.SellerID, p.BuyerID)
	}
}

// ─── Bill-of-Sale finalization (PDF) ─────────────────────────────────────────

const (
	// maxSignatureUploadBytes caps a signature PNG upload.
	maxSignatureUploadBytes = 5 << 20
	// maxSignatureUploadDimension bounds the decoded signature dimensions.
	maxSignatureUploadDimension = 8000
)

// validateSignatureUpload accepts only a bounded, well-formed PNG.
// image.DecodeConfig parses just the header, so this is cheap and — unlike the
// PDF library's own reader — it fails safely on a bogus IHDR.
func validateSignatureUpload(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("signature is empty")
	}
	if len(data) > maxSignatureUploadBytes {
		return fmt.Errorf("signature exceeds %d bytes", maxSignatureUploadBytes)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("signature is not a decodable image: %w", err)
	}
	if format != "png" {
		return fmt.Errorf("signature must be png, got %s", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		cfg.Width > maxSignatureUploadDimension || cfg.Height > maxSignatureUploadDimension {
		return fmt.Errorf("signature has implausible dimensions %dx%d", cfg.Width, cfg.Height)
	}
	return nil
}

// finalizeInFlight serialises Bill-of-Sale PDF generation per purchase.
//
// Generation is kicked from three places (the second signature, the lazy
// self-heal in GetBOS, and the explicit retry endpoint) and GetBOS is polled
// by several screens. Without this guard, a handful of goroutines race to
// render the same document and rewrite the same path. The DB column is
// already NULL-guarded, so only one can win the row — but the losers were
// still free to truncate the file another request was busy serving.
var finalizeInFlight sync.Map // purchaseID -> struct{}

// beginFinalize claims the finalize slot for a purchase. The bool reports
// whether the caller won the claim; the func releases it.
func beginFinalize(purchaseID uuid.UUID) (func(), bool) {
	if _, loaded := finalizeInFlight.LoadOrStore(purchaseID, struct{}{}); loaded {
		return func() {}, false
	}
	return func() { finalizeInFlight.Delete(purchaseID) }, true
}

// billOfSalePDFRelPath is the deterministic BARE relative URL stored in
// finalized_pdf_url. Deterministic (one file per purchase) so retries
// overwrite the same path rather than accumulating files. buildBOSResponse
// signs it on the way out.
func billOfSalePDFRelPath(purchaseID uuid.UUID) string {
	return fmt.Sprintf("/uploads/purchases/%s/bill_of_sale_%s.pdf", purchaseID.String(), purchaseID.String())
}

// billOfSalePDFDiskPath is where the generated PDF is written on disk.
func (h *PurchaseRequestHandler) billOfSalePDFDiskPath(purchaseID uuid.UUID) string {
	return filepath.Join(h.uploadDir, "purchases", purchaseID.String(), "bill_of_sale_"+purchaseID.String()+".pdf")
}

// uploadRelToDiskPath resolves a stored "/uploads/..." relative URL to a
// concrete disk path under uploadDir — with NO HTTP fetch, matching how
// SignBOS constructs signature paths. Returns "" for anything that isn't an
// /uploads/ path.
func (h *PurchaseRequestHandler) uploadRelToDiskPath(rel string) string {
	if !strings.HasPrefix(rel, "/uploads/") {
		return ""
	}
	trimmed := strings.TrimPrefix(rel, "/uploads/")
	return filepath.Join(h.uploadDir, filepath.FromSlash(trimmed))
}

// buildBOSData maps a canonical BoS row to the renderer's presentation data,
// resolving each signature URL to its on-disk PNG path.
func (h *PurchaseRequestHandler) buildBOSData(b *models.PurchaseBillOfSale) billofsale.Data {
	// Map the seller-declared title enum to a human label. Kept inline (a local
	// closure) so the whole model→renderer mapping lives in this one function.
	titleLabel := func() string {
		if b.TitleCondition == nil {
			return ""
		}
		switch models.TitleCondition(*b.TitleCondition) {
		case models.TitleConditionClean:
			return "Clean"
		case models.TitleConditionLienRecorded:
			return "Lien recorded"
		case models.TitleConditionSalvage:
			return "Salvage"
		case models.TitleConditionRebuilt:
			return "Rebuilt"
		case models.TitleConditionLemonBuyback:
			return "Lemon buyback"
		case models.TitleConditionFlood:
			return "Flood"
		case models.TitleConditionManufacturerBuyback:
			return "Manufacturer buyback"
		case models.TitleConditionOther:
			other := ""
			if b.TitleConditionOther != nil {
				other = strings.TrimSpace(*b.TitleConditionOther)
			}
			if other == "" {
				return "Other"
			}
			return "Other: " + other
		default:
			// Unknown/legacy value — surface it verbatim rather than dropping it.
			return strings.TrimSpace(*b.TitleCondition)
		}
	}()

	d := billofsale.Data{
		ReferenceID:         b.PurchaseRequestID.String(),
		GeneratedDate:       time.Now().UTC().Format("2006-01-02"),
		VehicleYear:         b.VehicleYear,
		VehicleMake:         b.VehicleMake,
		VehicleModel:        b.VehicleModel,
		VIN:                 b.VIN,
		TitleConditionLabel: titleLabel,
		SalePriceCents:      b.SaleAmountCents,
		Currency:            b.Currency,
		Terms:               b.TermsConditions,
		SellerName:          b.SellerName,
		SellerAddress:       b.SellerAddress,
		BuyerName:           b.BuyerName,
		BuyerAddress:        b.BuyerAddress,
		// ID-on-file acknowledgement. The canonical BoS row does not itself carry
		// an ID-document presence flag (party ID docs live on the user profile
		// and are exposed only via signed in-app URLs), so this stays false here.
		// The renderer supports the "Government ID: on file" line the moment a
		// presence signal is threaded into this mapping.
		SellerIDOnFile: false,
		BuyerIDOnFile:  false,
	}
	if b.SellerSignatureURL != nil {
		d.SellerSignaturePath = h.uploadRelToDiskPath(*b.SellerSignatureURL)
	}
	if b.BuyerSignatureURL != nil {
		d.BuyerSignaturePath = h.uploadRelToDiskPath(*b.BuyerSignatureURL)
	}
	if b.SellerSignedAt != nil {
		d.SellerSignedAt = b.SellerSignedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	if b.BuyerSignedAt != nil {
		d.BuyerSignedAt = b.BuyerSignedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	return d
}

// writeBillOfSalePDF renders the PDF and writes it to the deterministic path,
// returning the BARE relative URL to persist. It performs NO database access,
// so a render/write failure leaves the DB untouched — the signature-file
// failure-isolation guarantee. Requires both parties to have signed.
func (h *PurchaseRequestHandler) writeBillOfSalePDF(b *models.PurchaseBillOfSale) (string, error) {
	if b.SellerSignatureURL == nil || b.BuyerSignatureURL == nil {
		return "", fmt.Errorf("finalize bos: cannot generate before both parties have signed")
	}
	pdfBytes, err := billofsale.Render(h.buildBOSData(b))
	if err != nil {
		return "", fmt.Errorf("finalize bos: render: %w", err)
	}
	diskPath := h.billOfSalePDFDiskPath(b.PurchaseRequestID)
	dir := filepath.Dir(diskPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("finalize bos: mkdir: %w", err)
	}
	// Publish atomically. os.WriteFile opens O_TRUNC, so a client fetching the
	// URL a previous writer already committed could read a half-written file.
	// Write to a unique temp file in the same directory, then rename — on a
	// single filesystem the reader sees either the whole old file or the whole
	// new one, never a truncated one.
	tmp, err := os.CreateTemp(dir, ".bill_of_sale_*.pdf.tmp")
	if err != nil {
		return "", fmt.Errorf("finalize bos: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if _, err := tmp.Write(pdfBytes); err != nil {
		tmp.Close()
		return "", fmt.Errorf("finalize bos: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("finalize bos: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", fmt.Errorf("finalize bos: chmod: %w", err)
	}
	if err := os.Rename(tmpName, diskPath); err != nil {
		return "", fmt.Errorf("finalize bos: publish: %w", err)
	}
	return billOfSalePDFRelPath(b.PurchaseRequestID), nil
}

// generateAndStoreBillOfSale renders + writes the PDF then runs the
// NULL-guarded UPDATE. It is the shared core of both the async finalize
// goroutine and the manual retry endpoint.
//
// Idempotent + failure-isolated:
//   - already finalized (finalized_pdf_url set) → no-op, nil error.
//   - both signatures required; a missing signature file surfaces as a
//     controlled error from the renderer (no PDF written, column stays NULL).
//   - the deterministic path means a re-run overwrites the same bytes, and
//     the WHERE finalized_pdf_url IS NULL guard makes the DB write a no-op for
//     any loser of a concurrent race.
func (h *PurchaseRequestHandler) generateAndStoreBillOfSale(ctx context.Context, b *models.PurchaseBillOfSale) error {
	if b.FinalizedPDFURL != nil {
		return nil // already finalized — nothing to do
	}
	relURL, err := h.writeBillOfSalePDF(b)
	if err != nil {
		return err
	}
	if _, err := h.repo.SetFinalizedPDF(ctx, b.PurchaseRequestID, relURL); err != nil {
		return fmt.Errorf("finalize bos: persist: %w", err)
	}
	return nil
}

// finalizeBillOfSale is the detached-goroutine entry point invoked after the
// second signature commits. Errors are logged loudly and swallowed: the
// signatures + bos_signed status are already durable, so the worst case is a
// NULL finalized_pdf_url that a retry (or lazy GetBOS finalize) heals later.
func (h *PurchaseRequestHandler) finalizeBillOfSale(ctx context.Context, purchaseID, sellerID, buyerID uuid.UUID) {
	// This runs detached from any request, so chi's Recoverer cannot see it: an
	// escaping panic would terminate the whole API process. Never let one out.
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("purchase: finalize bos: panic recovered",
				"panic", r, "purchase_id", purchaseID)
		}
	}()
	// Only one finalize per purchase at a time. GetBOS kicks this off lazily on
	// every fetch while the column is NULL, and several screens poll GetBOS.
	release, won := beginFinalize(purchaseID)
	if !won {
		return // another goroutine is already generating this document
	}
	defer release()

	b, err := h.repo.GetBillOfSale(ctx, purchaseID)
	if err != nil {
		h.logger.Error("purchase: finalize bos: load", "error", err, "purchase_id", purchaseID)
		return
	}
	if b == nil {
		h.logger.Error("purchase: finalize bos: bos row missing", "purchase_id", purchaseID)
		return
	}
	if b.FinalizedPDFURL != nil {
		return // already finalized by a concurrent path
	}
	if err := h.generateAndStoreBillOfSale(ctx, b); err != nil {
		h.logger.Error("purchase: finalize bos", "error", err, "purchase_id", purchaseID)
		return
	}
	// Re-fetch and broadcast so both clients pick up the signed PDF URL.
	fresh, err := h.repo.GetBillOfSale(ctx, purchaseID)
	if err != nil || fresh == nil {
		h.logger.Error("purchase: finalize bos: refetch", "error", err, "purchase_id", purchaseID)
		return
	}
	freshResp := h.buildBOSResponse(fresh)
	if pr, perr := h.repo.GetByID(ctx, purchaseID); perr == nil && pr != nil {
		h.enrichBOSDocuments(ctx, freshResp, pr.CarID, sellerID, buyerID)
	}
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       freshResp,
		TargetUserIDs: []uuid.UUID{sellerID, buyerID},
	})
}

// FinalizeBOS — POST /api/v1/purchase-requests/{id}/bos/finalize
//
// Manual/lazy retry for the finalized PDF. Auth: the two participants
// (GetByIDForUser succeeds → seller or buyer) AND admins. A stranger who is
// neither a participant nor an admin gets 404. Idempotent: if already
// finalized, no-op and return the existing signed response; otherwise
// regenerate (same NULL-guarded UPDATE) and return the fresh response.
func (h *PurchaseRequestHandler) FinalizeBOS(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	// Participant check first; fall back to admin.
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		if role, roleOK := httputil.GetRole(r.Context()); roleOK && role == models.RoleAdmin {
			p, err = h.repo.GetByID(r.Context(), id)
		}
		if err != nil || p == nil {
			httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
			return
		}
	}
	b, err := h.repo.GetBillOfSale(r.Context(), id)
	if err != nil {
		h.logger.Error("purchase: finalize bos retry: load", "error", err, "purchase_id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if b == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("BILL_OF_SALE_NOT_FOUND", "Bill of Sale hasn't been created yet"))
		return
	}
	// Only finalize once both parties have signed (status bos_signed+).
	if !b.FullySigned() {
		httputil.WriteError(w, http.StatusConflict, models.ErrBOSNotSigned)
		return
	}
	if b.FinalizedPDFURL == nil {
		// Take the same per-purchase claim the async path uses. If a generation
		// is already running we simply return the current (still-pending) state
		// rather than rendering a second copy on top of it; the client is
		// already listening for purchase_bill_of_sale_updated.
		release, won := beginFinalize(id)
		if won {
			defer release()
			if err := h.generateAndStoreBillOfSale(r.Context(), b); err != nil {
				h.logger.Error("purchase: finalize bos retry", "error", err, "purchase_id", id)
				httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
				return
			}
		}
		if fresh, ferr := h.repo.GetBillOfSale(r.Context(), id); ferr == nil && fresh != nil {
			b = fresh
		}
	}
	resp := h.buildBOSResponse(b)
	h.enrichBOSDocuments(r.Context(), resp, p.CarID, p.SellerID, p.BuyerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
	h.wsHub.Broadcast(&ws.Event{
		Type:          "purchase_bill_of_sale_updated",
		Payload:       resp,
		TargetUserIDs: []uuid.UUID{p.SellerID, p.BuyerID},
	})
}

// GetBOS — GET /api/v1/purchase-requests/{id}/bos
func (h *PurchaseRequestHandler) GetBOS(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	pr, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	bos, err := h.repo.GetBillOfSale(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Row not yet seeded (offer still in `requested`): return a real 404
	// so the iOS BoS cache treats it as "no row exists yet" rather than
	// decoding a null body as a required struct. Prior behavior served
	// HTTP 200 with a `null` body, which the iOS Codable then rejected
	// as a missing-key decoding error.
	if bos == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("BILL_OF_SALE_NOT_FOUND", "Bill of Sale hasn't been created yet"))
		return
	}
	// Lazy heal (C2): rows seeded blank by the pre-fix Accept bug stayed
	// blank forever (ON CONFLICT DO NOTHING + nothing backfilled). Re-copy
	// from the car row — but only genuinely-blank fields, and never on a
	// seller-signed row (see HealBlankBOSVehicleFields for the line drawn).
	if healed, healErr := h.repo.HealBlankBOSVehicleFields(r.Context(), id); healErr == nil && healed {
		if fresh, refetchErr := h.repo.GetBillOfSale(r.Context(), id); refetchErr == nil && fresh != nil {
			bos = fresh
			h.logger.Info("purchase: healed blank BoS vehicle fields", "purchase_id", id)
		}
	}
	// Lazy self-heal: if both parties have signed but the finalized PDF was
	// never produced (e.g. the client missed the WS event, or the async
	// finalize errored transiently), kick a background regenerate. The
	// NULL-guarded UPDATE keeps this idempotent; the response shape is
	// unchanged (the URL simply appears on a subsequent fetch).
	if bos.FullySigned() && bos.FinalizedPDFURL == nil {
		go h.finalizeBillOfSale(context.Background(), id, pr.SellerID, pr.BuyerID)
	}
	resp := h.buildBOSResponse(bos)
	h.enrichBOSDocuments(r.Context(), resp, pr.CarID, pr.SellerID, pr.BuyerID)
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// ─── Payment ────────────────────────────────────────────────────────────────

// CreatePaymentIntent — POST /api/v1/purchase-requests/{id}/payment-intent
// refuseIfSalesDisabled guards the points that COMMIT someone further into a
// sale: making an offer, authorizing money, scheduling the handover, handing
// over the keys.
//
// It deliberately does NOT guard the ways OUT — capture of an
// already-authorized sale, the buyer's accept or reject, refunds, admin
// resolution, and the inspection-window sweep. A kill switch that strands a
// buyer who has already authorized money and taken delivery is worse than the
// problem it is meant to contain: the hold would lapse after 7 days with the
// car already gone and nobody paid. So the switch stops sales STARTING and
// lets in-flight ones land or unwind.
func (h *PurchaseRequestHandler) refuseIfSalesDisabled(w http.ResponseWriter, userID uuid.UUID) bool {
	if h.salesOpenFor(userID) {
		return false
	}
	httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("SALES_PAUSED",
		"Buying isn't available right now — rentals are unaffected. Check back soon."))
	return true
}

func (h *PurchaseRequestHandler) CreatePaymentIntent(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	if h.refuseIfSalesDisabled(w, userID) {
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
	customer, err := customerForUser(r.Context(), h.stripe, h.userRepo, buyer, h.logger)
	if err != nil {
		h.logger.Error("purchase: resolve customer", "error", err)
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
			// Notify seller only when we actually flipped the row from
			// a prior state into authorized — a repeat sync on an
			// already-authorized row must not re-buzz.
			priorStatus := p.Status
			p = updated
			h.broadcast("purchase_payment_updated", p, map[string]any{"payment_status": "requires_capture"})
			h.postSystemMessage(r.Context(), p.ChatID, p.BuyerID, "Buyer authorized payment — funds are held pending inspection")
			if priorStatus != models.PurchaseStatusPaymentAuthorized {
				h.notifyPurchaseParty(p, p.SellerID, models.NotificationTypePurchasePayment,
					"Payment authorized",
					"The buyer's payment is authorized. Schedule the vehicle handover.")
			}
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
	if h.refuseIfSalesDisabled(w, userID) {
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
	// The auth hold is finite. Keys change hands at the handover, the buyer
	// then has PurchaseInspectionWindow, and the capture that ends the
	// window must still land inside the hold with margin to spare —
	// otherwise the seller has given up the car for money that can no
	// longer be taken. Refuse a date that would outrun it, and say when the
	// latest possible handover is.
	existing, gerr := h.repo.GetByIDForUser(r.Context(), id, userID)
	if gerr != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if existing.AuthExpiresAt != nil {
		latest := models.LatestHandoverFor(*existing.AuthExpiresAt)
		if body.HandoverScheduledAt.After(latest) {
			apiErr := models.NewAPIError("HANDOVER_TOO_LATE",
				fmt.Sprintf("The buyer's payment hold ends %s. Schedule the handover by %s so the %.0f-hour inspection window still fits inside it.",
					existing.AuthExpiresAt.Format("Mon, Jan 2 15:04 MST"), latest.Format("Mon, Jan 2 15:04 MST"),
					models.PurchaseInspectionWindow.Hours()))
			apiErr.Details = map[string]interface{}{
				"latest_handover_at": latest.UTC().Format(time.RFC3339),
				"auth_expires_at":    existing.AuthExpiresAt.UTC().Format(time.RFC3339),
			}
			httputil.WriteError(w, http.StatusConflict, apiErr)
			return
		}
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

	h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseHandover,
		"Handover scheduled",
		fmt.Sprintf("%s scheduled the handover for %s at %s.", nameOr(resp.SellerName, "The seller"), body.HandoverScheduledAt.Format(time.RFC1123), body.HandoverLocation))
}

// KeysHandedOver — POST /api/v1/purchase-requests/{id}/keys-handed-over
func (h *PurchaseRequestHandler) KeysHandedOver(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	if h.refuseIfSalesDisabled(w, userID) {
		return
	}
	// The same hold arithmetic at the moment that matters most: once the
	// keys are gone the window is running, so it must end — with margin —
	// before the hold does. (The repository re-checks this in SQL.)
	existing, gerr := h.repo.GetByIDForUser(r.Context(), id, userID)
	if gerr != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if existing.AuthExpiresAt != nil && time.Now().After(models.LatestHandoverFor(*existing.AuthExpiresAt)) {
		apiErr := models.NewAPIError("AUTH_EXPIRING",
			fmt.Sprintf("The buyer's payment hold ends %s — too soon for the %.0f-hour inspection window to finish inside it. Don't hand over the keys under this payment; once the hold lapses the sale is cancelled and the buyer can make a new offer.",
				existing.AuthExpiresAt.Format("Mon, Jan 2 15:04 MST"), models.PurchaseInspectionWindow.Hours()))
		apiErr.Details = map[string]interface{}{
			"auth_expires_at": existing.AuthExpiresAt.UTC().Format(time.RFC3339),
		}
		httputil.WriteError(w, http.StatusConflict, apiErr)
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

	h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseHandover,
		"Keys handed over",
		fmt.Sprintf("You have %.0fh to inspect the vehicle and accept or reject.", models.PurchaseInspectionWindow.Hours()))
}

// InspectAccept — POST /api/v1/purchase-requests/{id}/inspect/accept
//
// SAFETY CRITICAL ordering (DESIGN SPEC items 11 + 22): the vehicle title must
// be on file, the buyer's inspection checklist must be fully confirmed, and the
// seller-declared title condition must be set — ALL validated and the checklist
// PERSISTED BEFORE the status flip and BEFORE any Stripe capture. Strict order:
// validate → persist checklist → InspectionAccept flip → capture.
func (h *PurchaseRequestHandler) InspectAccept(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	existing, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	if userID != existing.BuyerID {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only the buyer can accept the vehicle"))
		return
	}
	var body models.InspectVehicleAcceptBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	// (11) Title-on-file gate — reject BEFORE any status flip or capture.
	titleURL, err := h.repo.GetCarTitleDocumentURL(r.Context(), existing.CarID)
	if err != nil {
		h.logger.Error("purchase: title lookup", "error", err, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if titleURL == nil {
		httputil.WriteError(w, http.StatusConflict, models.ErrTitleRequired)
		return
	}

	// (22) Inspection checklist: every item + the payment-completion ack must
	// be true, and the seller-declared title condition must be set.
	if !body.AllConfirmed() {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrInspectionChecklistIncomplete)
		return
	}
	bos, err := h.repo.GetBillOfSale(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if bos == nil || bos.TitleCondition == nil || strings.TrimSpace(*bos.TitleCondition) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.ErrTitleConditionRequired)
		return
	}

	// Persist the checklist BEFORE the status flip + capture.
	if _, err := h.repo.SaveInspectionChecklist(r.Context(), id, models.PurchaseInspectionChecklist{
		VINMatches:            body.VINMatches,
		OdometerReviewed:      body.OdometerReviewed,
		ExteriorOK:            body.ExteriorOK,
		InteriorOK:            body.InteriorOK,
		MechanicalTestDriveOK: body.MechanicalTestDriveOK,
		TitleReviewed:         body.TitleReviewed,
		KeysHandedOver:        body.KeysHandedOver,
		BuyerUnderstandsAcceptanceCompletesPayment: body.BuyerUnderstandsAcceptanceCompletesPayment,
	}); err != nil {
		h.logger.Error("purchase: save inspection checklist", "error", err, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
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
	h.postSystemMessage(r.Context(), final.ChatID, userID, "Buyer accepted the vehicle — payment completed, sale complete")

	h.notifyPurchaseCounterparty(final, userID, models.NotificationTypePurchasePayment,
		"Sale complete",
		fmt.Sprintf("%s accepted the vehicle. Payment is complete — funds are on the way to you.", nameOr(resp.BuyerName, "The buyer")))
}

// capturePayment runs Stripe capture + MarkCaptured. Returns the updated
// purchase row on success, nil on failure (row stays at
// inspection_accepted for a follow-up retry).
func (h *PurchaseRequestHandler) capturePayment(ctx context.Context, p *models.PurchaseRequest) *models.PurchaseRequest {
	if p.PaymentIntentID == nil || *p.PaymentIntentID == "" {
		h.logger.Error("purchase: capture with no payment intent", "id", p.ID)
		return nil
	}
	// Capture hits Stripe BEFORE MarkCaptured, so calling this on a state
	// MarkCaptured will refuse takes the buyer's money and leaves the row
	// behind. Admit exactly the states MarkCaptured admits, no more:
	//   inspection_accepted — the buyer accepted, or the window closed.
	//   rejected_upheld     — support ruled for the seller; this IS the
	//                         capture path for an upheld rejection.
	// An earlier version of this guard admitted only the first, which
	// silently disabled every upheld rejection: no capture, the hold lapsed
	// after 7 days, and the seller who had handed over the car was paid
	// nothing while both parties were told the sale completed.
	if p.Status != models.PurchaseStatusInspectionAccepted &&
		p.Status != models.PurchaseStatusRejectedUpheld {
		h.logger.Error("purchase: refusing to capture from an unexpected state",
			"id", p.ID, "status", p.Status)
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

	// Pay the seller. This is the settlement point: the money is captured,
	// the rejection window has closed and the handover is recorded, so the
	// sale will not in the ordinary course move backwards. Idempotent on the
	// purchase id, so a capture retry cannot pay twice. Never fatal — a
	// failure here leaves the ledger row for the payout sweeps, and the sale
	// itself is already complete.
	if h.payoutH != nil {
		h.payoutH.SettleSalePayout(ctx, updated.ID, updated.SellerID, updated.OfferAmountCents, nil)
	}
	return updated
}

// ConfirmHandover — POST /api/v1/purchase-requests/{id}/confirm-handover
//
// The buyer says "I have the car and I'm happy". This ACCELERATES the sale: it
// completes now instead of waiting out the 48-hour inspection window.
//
// Deliberately NOT a gate. A buyer-side confirmation that sales must wait for
// would reintroduce the hostage problem the window decision exists to remove:
// a buyer who says nothing would stall the seller indefinitely, having already
// driven away in their car. Silence still completes the sale on its own. This
// only lets an honest buyer end the wait early, which is also the strongest
// dispute evidence we can hold — the buyer's own confirmation of receipt.
func (h *PurchaseRequestHandler) ConfirmHandover(w http.ResponseWriter, r *http.Request) {
	userID, id, ok := h.parseAuthed(w, r)
	if !ok {
		return
	}
	p, err := h.repo.GetByIDForUser(r.Context(), id, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrPurchaseRequestNotFound)
		return
	}
	// The BUYER only: the seller confirming their own sale complete would be
	// marking their own homework.
	if p.BuyerID != userID {
		httputil.WriteError(w, http.StatusForbidden, models.ErrInvalidPurchaseAction)
		return
	}
	if p.Status == models.PurchaseStatusCompleted {
		httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), p, userID))
		return
	}
	if p.Status != models.PurchaseStatusAwaitingInspection {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NOT_AWAITING_INSPECTION",
			"This sale isn't waiting on your inspection."))
		return
	}

	claimed, cerr := h.repo.ClaimBuyerHandoverConfirm(r.Context(), id)
	if cerr != nil {
		h.logger.Error("purchase: confirm handover", "error", cerr, "id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if claimed == nil {
		// Lost the claim. If the window closed in the same moment, the sale
		// completes either way — but a rejection or an auth expiry can win
		// this race too, and the buyer must not be told "confirmed" then.
		fresh, _ := h.repo.GetByID(r.Context(), id)
		if fresh != nil && (fresh.Status == models.PurchaseStatusInspectionAccepted || fresh.Status == models.PurchaseStatusCompleted) {
			httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), fresh, userID))
			return
		}
		apiErr := models.NewAPIError("NOT_AWAITING_INSPECTION", "This sale isn't waiting on your inspection.")
		if fresh != nil {
			apiErr.Details = map[string]interface{}{"status": string(fresh.Status)}
		}
		httputil.WriteError(w, http.StatusConflict, apiErr)
		return
	}

	updated := h.capturePayment(r.Context(), claimed)
	if updated == nil {
		// Capture failed; the row stays at inspection_accepted and
		// runCaptureRetry owns it. The buyer's confirmation stands.
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("CAPTURE_PENDING",
			"We're completing your payment — this can take a moment. You don't need to do anything."))
		return
	}
	h.broadcast("purchase_request_updated", updated, nil)
	h.postSystemMessage(r.Context(), updated.ChatID, updated.BuyerID, "Buyer confirmed handover — sale completed")
	h.notifyPurchaseParty(updated, updated.SellerID, models.NotificationTypePurchaseRequest,
		"Sale completed", "The buyer confirmed they have the car. The sale is complete and your payout is on its way.")
	httputil.WriteJSON(w, http.StatusOK, h.buildResponse(r.Context(), updated, userID))
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

	h.notifyPurchaseCounterparty(updated, userID, models.NotificationTypePurchaseRejection,
		"Vehicle rejected",
		fmt.Sprintf("%s rejected the vehicle. DrivaBai support is reviewing.", nameOr(resp.BuyerName, "The buyer")))
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

	h.notifyPurchaseCounterparty(p, userID, models.NotificationTypePurchaseRejection,
		"Rejection withdrawn",
		fmt.Sprintf("%s withdrew the rejection. The sale is proceeding.", nameOr(resp.BuyerName, "The buyer")))
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
	httputil.WriteJSON(w, http.StatusOK, h.buildAdminResponse(r.Context(), p))
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
			"DrivaBai support accepted the rejection — payment hold released (buyer not charged), sale cancelled")
	case "uphold":
		if captured := h.capturePayment(r.Context(), p); captured != nil {
			final = captured
		}
		h.postSystemMessage(r.Context(), final.ChatID, adminID,
			"DrivaBai support upheld the sale — payment completed, sale complete")
	}

	// Build a rejection response with fresh evidence signatures.
	rejResp := h.buildRejectionResponse(r.Context(), rej)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"rejection":        rejResp,
		"purchase_request": h.buildResponse(r.Context(), final, adminID),
	})
	h.broadcast("purchase_request_updated", final, nil)
	verb := rejectionResolutionVerb(res)
	h.notifyPurchaseParty(final, final.BuyerID, models.NotificationTypePurchaseRejection,
		"Rejection resolved",
		fmt.Sprintf("DrivaBai support %s your rejection.", verb))
	h.notifyPurchaseParty(final, final.SellerID, models.NotificationTypePurchaseRejection,
		"Rejection resolved",
		fmt.Sprintf("DrivaBai support %s the buyer's rejection.", verb))
}

// AdminRetryRefund — POST /api/v1/admin/purchase-requests/{id}/retry-refund
// Admin-triggered kick when refund_status is stuck at 'failed'.
//
// It says "retry" and it means it: until this guard existed the endpoint would
// happily issue a FIRST full refund on any completed sale, at any age, with no
// check that a refund had ever failed — despite the doc comment claiming
// otherwise. On a completed car sale that is a five-figure movement one
// mis-click away, on a row that stays marked sold.
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
	// This is a RETRY. A refund must already have been attempted and failed;
	// otherwise the caller wants the rejection flow, not this button.
	if p.RefundStatus == nil || *p.RefundStatus != models.VehicleReturnRefundFailed {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("NO_FAILED_REFUND",
			"This sale has no failed refund to retry. Resolve the buyer's rejection instead, which issues the refund."))
		return
	}
	// Stripe will not refund a charge older than its own window, and a
	// months-old sale is a support conversation, not a button. Refuse rather
	// than fire a request that fails confusingly at the API.
	if p.CompletedAt != nil && time.Since(*p.CompletedAt) > models.PurchaseRefundMaxAge {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("REFUND_WINDOW_CLOSED",
			fmt.Sprintf("This sale completed more than %d days ago. Refunds this old have to be handled directly in Stripe.",
				int(models.PurchaseRefundMaxAge.Hours()/24))))
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
	// Claw back the seller's share. Without this the buyer is refunded in
	// full while the seller keeps their money and the platform absorbs the
	// entire sale.
	if h.payoutH != nil {
		h.payoutH.ReverseSalePayoutForRefund(r.Context(), updated.ID, updated.OfferAmountCents, "admin_retry")
	}

	// The money went back, so the car is not sold. Leaving it 'sold' and
	// archived stranded the seller's listing with no way back except a
	// database edit.
	if status == models.VehicleReturnRefundSucceeded {
		if rerr := h.repo.UnsellAfterRefund(r.Context(), updated.CarID); rerr != nil {
			h.logger.Error("purchase: un-sell car after refund", "error", rerr, "car_id", updated.CarID)
		}
	}
	// Only push refund-completion messages when the refund actually settled
	// synchronously (Stripe returned status=succeeded). Pending-refund kicks
	// stay silent — the succeeded webhook will notify when it lands.
	if refund.Status == "succeeded" {
		amount := formatMoney(updated.OfferAmountCents)
		h.notifyPurchaseParty(updated, updated.BuyerID, models.NotificationTypePurchasePayment,
			"Refund issued",
			fmt.Sprintf("%s refunded to your card.", amount))
		h.notifyPurchaseParty(updated, updated.SellerID, models.NotificationTypePurchasePayment,
			"Refund completed",
			fmt.Sprintf("Refund of %s to the buyer has completed.", amount))
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
		// Snapshot status before the transition so we only notify on a
		// real transition. Stripe re-emits this event on resync + on
		// initial confirm, and SyncPayment may have already run this
		// path — without the guard the seller gets "Payment authorized"
		// twice for the same authorization.
		before, _ := h.repo.GetByPaymentIntentID(ctx, intentID)
		p, err := h.repo.MarkAuthorized(ctx, intentID)
		if err == nil && p != nil {
			h.broadcast("purchase_payment_updated", p, map[string]any{"payment_status": "requires_capture"})
			if before == nil || before.Status != models.PurchaseStatusPaymentAuthorized {
				h.postSystemMessage(ctx, p.ChatID, p.BuyerID, "Buyer authorized payment — funds are held pending inspection")
				h.notifyPurchaseParty(p, p.SellerID, models.NotificationTypePurchasePayment,
					"Payment authorized",
					"The buyer's payment is authorized. Schedule the vehicle handover.")
			}
		}
	case "payment_intent.succeeded":
		p, err := h.repo.GetByPaymentIntentID(ctx, intentID)
		if err == nil && p != nil {
			// Snapshot pre-capture status — if the sync path in
			// InspectAccept.capturePayment already flipped the row to
			// `completed`, the webhook's follow-up MarkCaptured still
			// succeeds (its WHERE admits `completed`), and without this
			// guard we'd fire a duplicate "Payment captured" pair.
			priorStatus := p.Status
			if updated, err := h.repo.MarkCaptured(ctx, p.ID); err == nil {
				// Pay the seller HERE too. This branch is the only thing
				// that completes a sale whose synchronous MarkCaptured died
				// after Stripe had already captured; without this the sale
				// reads completed, both parties are told the seller has been
				// paid, and 100% of the money stays in the platform balance
				// with no sweep looking for it. Idempotent on the purchase.
				if h.payoutH != nil {
					h.payoutH.SettleSalePayout(ctx, updated.ID, updated.SellerID, updated.OfferAmountCents, nil)
				}
				h.broadcast("purchase_payment_updated", updated, map[string]any{"payment_status": "succeeded"})
				// (23) Sale completion must always emit purchase_request_updated
				// with the completed status so both clients settle their UI even
				// when capture lands via the webhook rather than InspectAccept.
				h.broadcast("purchase_request_updated", updated, nil)
				if priorStatus != models.PurchaseStatusCompleted {
					amount := formatMoney(updated.OfferAmountCents)
					h.notifyPurchaseParty(updated, updated.BuyerID, models.NotificationTypePurchasePayment,
						"Payment completed",
						fmt.Sprintf("Your payment of %s is complete. The seller has been paid.", amount))
					h.notifyPurchaseParty(updated, updated.SellerID, models.NotificationTypePurchasePayment,
						"Payment completed",
						fmt.Sprintf("The buyer's payment of %s is complete. Funds are on the way.", amount))
				}
			}
		}
	case "payment_intent.payment_failed":
		// Previously dropped on the floor: a buyer whose card declined
		// OUTSIDE the open PaymentSheet (backgrounded app, async issuer
		// decline) was never told and the row kept payment_status as-is.
		// Record it and tell the buyer to retry; the purchase status is
		// untouched, so the existing "Authorize payment" CTA doubles as
		// the retry button.
		if p, err := h.repo.MarkPaymentFailed(ctx, intentID); err == nil && p != nil {
			h.broadcast("purchase_payment_updated", p, map[string]any{"payment_status": "failed"})
			h.notifyPurchaseParty(p, p.BuyerID, models.NotificationTypePurchasePayment,
				"Payment failed",
				"Your payment didn't go through — you were not charged. Open the purchase and try again.")
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
				h.notifyPurchaseParty(updated, updated.BuyerID, models.NotificationTypePurchasePayment,
					"Payment hold released",
					"The hold on your card has been released — you were not charged.")
				h.notifyPurchaseParty(updated, updated.SellerID, models.NotificationTypePurchaseRequest,
					"Payment hold released",
					"The buyer's payment hold was released.")
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
			h.runAcceptExpiry(ctx)
			h.runAuthExpiry(ctx)
			h.runInspectionExpiry(ctx)
			h.runCaptureRetry(ctx)
			h.runSellerPayoutReconcile(ctx)
		}
	}
}

// runInspectionExpiry makes the 48-hour inspection window real.
//
// Until this existed, inspection_deadline_at was written, returned to clients
// and read by nothing. The only clock that ended `awaiting_inspection` was
// the 7-day Stripe authorization lapsing — and the seller has already handed
// over the keys by then, so the exit was "buyer keeps the car, the hold dies,
// nobody is paid".
//
// Silence now COMPLETES the sale. The reasoning: the buyer has taken delivery,
// inspection happens at handover in practice, and two full days of silence
// after driving away is acceptance. Refunding instead would make the seller
// hostage to a buyer who already holds the vehicle, and there is no mechanism
// to get a car back.
//
// That is only defensible if the silence was INFORMED, so the window warns at
// T−24h and T−2h and records that it did. The deadline plus two recorded
// warnings is the evidence if a buyer later says they never had the chance.
func (h *PurchaseRequestHandler) runInspectionExpiry(ctx context.Context) {
	now := time.Now().UTC()

	// Phase 1 — the two warnings. Each is claimed once. A row already past
	// the deadline is skipped by the `> now` bound so we never send
	// "24 hours left" and "sale completed" in the same tick.
	for _, w := range []struct {
		column string
		within time.Duration
		title  string
		body   string
	}{
		{"inspection_warned_24h_at", 24 * time.Hour, "24 hours left to inspect",
			"You have 24 hours left to accept or report a problem with %s. If we don't hear from you, the sale completes automatically and the seller is paid."},
		{"inspection_warned_2h_at", 2 * time.Hour, "2 hours left to inspect",
			"Last reminder: 2 hours left to accept or report a problem with %s. If we don't hear from you, the sale completes automatically and the seller is paid."},
	} {
		claimed, err := h.repo.ClaimInspectionWarning(ctx, w.column, w.within, now, 50)
		if err != nil {
			h.logger.Error("purchase inspection: claim warning", "error", err, "column", w.column)
			continue
		}
		for i := range claimed {
			p := &claimed[i]
			carTitle := "the car"
			if car, cerr := h.carRepo.GetByID(ctx, p.CarID); cerr == nil && car != nil {
				carTitle = carTitleOr(car.Title)
			}
			h.notifyPurchaseParty(p, p.BuyerID, models.NotificationTypePurchaseRequest,
				w.title, fmt.Sprintf(w.body, carTitle))
			h.logger.Info("purchase inspection: warning sent",
				"id", p.ID, "which", w.column, "deadline", p.InspectionDeadlineAt)
		}
	}

	// Phase 2 — the deadline itself. Claim each row individually so a buyer
	// acting in the same second wins, then run the existing capture with its
	// existing stable idempotency key.
	expired, err := h.repo.ListInspectionExpired(ctx, now, 50)
	if err != nil {
		h.logger.Error("purchase inspection: list expired", "error", err)
		return
	}
	for i := range expired {
		claimed, cerr := h.repo.ClaimInspectionAutoAccept(ctx, expired[i].ID)
		if cerr != nil {
			h.logger.Error("purchase inspection: claim auto-accept", "error", cerr, "id", expired[i].ID)
			continue
		}
		if claimed == nil {
			continue // the buyer acted first, or another instance claimed it
		}
		h.logger.Info("purchase inspection: window expired, completing sale",
			"id", claimed.ID, "deadline", claimed.InspectionDeadlineAt,
			"warned_24h", claimed.InspectionWarned24hAt != nil,
			"warned_2h", claimed.InspectionWarned2hAt != nil)

		updated := h.capturePayment(ctx, claimed)
		if updated == nil {
			// capturePayment logs and leaves the row at inspection_accepted;
			// runCaptureRetry owns it from here, so it still has an exit.
			continue
		}
		h.broadcast("purchase_request_updated", updated, nil)

		carTitle := "the car"
		if car, cerr := h.carRepo.GetByID(ctx, updated.CarID); cerr == nil && car != nil {
			carTitle = carTitleOr(car.Title)
		}
		h.postSystemMessage(ctx, updated.ChatID, updated.BuyerID,
			"Inspection window closed — sale completed automatically")
		h.notifyPurchaseParty(updated, updated.BuyerID, models.NotificationTypePurchaseRequest,
			"Sale completed",
			fmt.Sprintf("The 48-hour inspection window for %s closed without a response, so the sale completed and the seller has been paid.", carTitle))
		h.notifyPurchaseParty(updated, updated.SellerID, models.NotificationTypePurchaseRequest,
			"Sale completed",
			fmt.Sprintf("The buyer's inspection window for %s closed without a response. The sale is complete.", carTitle))
	}
}

// runCaptureRetry re-attempts Stripe capture for purchases parked at
// inspection_accepted (C5): the buyer accepted the vehicle but the
// synchronous capture failed, and previously nothing retried — both parties
// sat on "Completing payment…" indefinitely. capturePayment's idempotency key
// is stable per purchase, so a retry of an already-captured intent is a
// Stripe no-op; success notifications arrive via the payment_intent.succeeded
// webhook (whose priorStatus guard prevents duplicates). If the auth expired
// meanwhile, Stripe cancels the intent and the payment_intent.canceled
// webhook resolves the row — so every stuck row has a way out.
func (h *PurchaseRequestHandler) runCaptureRetry(ctx context.Context) {
	// 2m grace so we never race the request-path capture that just started.
	rows, err := h.repo.ListStuckCaptures(ctx, 2*time.Minute, 50)
	if err != nil {
		h.logger.Error("purchase: list stuck captures", "error", err)
		return
	}
	for i := range rows {
		p := &rows[i]
		h.logger.Info("purchase: retrying stuck capture", "id", p.ID)
		if updated := h.capturePayment(ctx, p); updated != nil {
			h.broadcast("purchase_request_updated", updated, nil)
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
		// Look up the car title once for a friendly notification body — a
		// missing title falls back to "the car".
		carTitle := "the car"
		if car, err := h.carRepo.GetByID(ctx, p.CarID); err == nil && car != nil {
			carTitle = carTitleOr(car.Title)
		}
		body := fmt.Sprintf("The purchase offer for %s has expired.", carTitle)
		h.notifyPurchaseParty(p, p.BuyerID, models.NotificationTypePurchaseRequest,
			"Offer expired", body)
		h.notifyPurchaseParty(p, p.SellerID, models.NotificationTypePurchaseRequest,
			"Offer expired", body)
	}
}

// runAcceptExpiry (audit H3) enforces the post-accept TTL: warn both
// parties 24h before expiry (claimed-once), then expire at 72h. Until this
// sweep, `accepted`/`bos_*` blocked every sale AND lease on the car with
// no seller exit, no admin exit, and no clock — a ghosting buyer froze the
// car forever. Same shape as the lease accept-expiry sweep.
func (h *PurchaseRequestHandler) runAcceptExpiry(ctx context.Context) {
	now := time.Now().UTC()

	// Phase 1: the 24h warning. Rows already past the full TTL go straight
	// to phase 2 (no contradictory warn+expire in one tick).
	warnCutoff := now.Add(-(models.PurchaseAcceptTTL - models.PurchaseAcceptWarnBefore))
	warned, err := h.repo.ClaimPurchaseAcceptWarnings(ctx, warnCutoff, now.Add(-models.PurchaseAcceptTTL), 50)
	if err != nil {
		h.logger.Error("purchase accept-expiry: claim warnings", "error", err)
	}
	for i := range warned {
		p := &warned[i]
		h.notifyPurchaseParty(p, p.BuyerID, models.NotificationTypePurchaseRequest,
			"Finish the sale within 24 hours",
			"Your accepted purchase expires in about 24 hours unless you complete the paperwork and authorize payment. Cancel anytime to release the car.")
		h.notifyPurchaseParty(p, p.SellerID, models.NotificationTypePurchaseRequest,
			"Sale awaiting the buyer — 24 hours left",
			"The buyer hasn't completed the sale. If they don't within about 24 hours, the offer expires and your car opens up automatically.")
	}

	// Phase 2: expiry at the full TTL.
	expireCutoff := now.Add(-models.PurchaseAcceptTTL)
	candidates, err := h.repo.ListPurchaseAcceptExpired(ctx, expireCutoff, 50)
	if err != nil {
		h.logger.Error("purchase accept-expiry: list", "error", err)
		return
	}
	for i := range candidates {
		p := &candidates[i]
		// bos_signed can carry a PaymentIntent — possibly an AUTHORIZED one
		// whose amount_capturable_updated webhook was lost. RETRIEVE FIRST:
		// Stripe's cancel SUCCEEDS on a requires_capture intent (it
		// releases the hold), so cancel-then-classify would silently void
		// a sale the buyer already authorized (review R5).
		if p.PaymentIntentID != nil && h.stripe != nil {
			pi, rerr := h.stripe.RetrievePaymentIntent(*p.PaymentIntentID)
			if rerr != nil {
				h.logger.Warn("purchase accept-expiry: retrieve failed, deferring", "error", rerr, "purchase_id", p.ID)
				continue
			}
			switch pi.Status {
			case "requires_capture":
				// The buyer authorized; only the webhook got lost. Adopt
				// through the exact path the webhook takes (idempotent,
				// prior-status-guarded notifications) — the row then moves
				// to payment_authorized and the 7-day auth TTL owns it.
				h.logger.Info("purchase accept-expiry: adopting lost authorization", "purchase_id", p.ID)
				h.HandleStripeEvent(ctx, "payment_intent.amount_capturable_updated", *p.PaymentIntentID)
				continue
			case "succeeded", "processing":
				h.logger.Info("purchase accept-expiry: payment in flight, skipping", "purchase_id", p.ID, "stripe_status", pi.Status)
				continue
			case "canceled":
				// Dead intent — safe to expire the row.
			default:
				// Live pre-authorization intent: only a CONFIRMED cancel
				// may precede the claim.
				if cerr := h.stripe.CancelPaymentIntent(*p.PaymentIntentID); cerr != nil {
					h.logger.Warn("purchase accept-expiry: cancel failed, deferring", "error", cerr, "purchase_id", p.ID)
					continue
				}
			}
		}
		expired, cerr := h.repo.ClaimPurchaseAcceptExpiry(ctx, p.ID)
		if cerr != nil {
			if !errors.Is(cerr, models.ErrInvalidPurchaseAction) {
				// A lost race reads as ErrInvalidPurchaseAction; anything
				// else is a real DB error and must be visible (review LOW).
				h.logger.Error("purchase accept-expiry: claim", "error", cerr, "purchase_id", p.ID)
			}
			continue
		}
		h.logger.Info("purchase accept-expiry: sale expired, car unblocked",
			"purchase_id", expired.ID, "car_id", expired.CarID)

		h.broadcast("purchase_request_updated", expired, nil)
		h.postSystemMessage(ctx, expired.ChatID, expired.BuyerID, "Purchase expired — sale not completed in time")
		carTitle := "the car"
		if car, err := h.carRepo.GetByID(ctx, expired.CarID); err == nil && car != nil {
			carTitle = carTitleOr(car.Title)
		}
		h.notifyPurchaseParty(expired, expired.BuyerID, models.NotificationTypePurchaseRequest,
			"Purchase expired",
			fmt.Sprintf("The sale of %s wasn't completed within 72 hours of acceptance, so it expired. You can send a new offer anytime.", carTitle))
		h.notifyPurchaseParty(expired, expired.SellerID, models.NotificationTypePurchaseRequest,
			"Sale expired — car released",
			fmt.Sprintf("The buyer didn't complete the sale of %s within 72 hours, so it expired. Your car is open to offers and rentals again.", carTitle))
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
		h.postSystemMessage(ctx, released.ChatID, released.BuyerID, "Payment hold expired — sale cancelled")
		h.notifyPurchaseParty(released, released.BuyerID, models.NotificationTypePurchasePayment,
			"Payment hold expired",
			"The hold on your card expired. Authorize payment again to continue the sale.")
		h.notifyPurchaseParty(released, released.SellerID, models.NotificationTypePurchaseRequest,
			"Sale on hold",
			"The buyer's payment hold expired. Waiting for them to authorize again.")
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
		models.ErrCodePurchaseNotCancellable, models.ErrCodeTitleRequired:
		return http.StatusConflict
	case models.ErrCodePurchaseOfferTooLow, models.ErrCodePurchaseEvidenceRequired,
		models.ErrCodeInvalidInput,
		models.ErrCodeSellerAddressRequired, models.ErrCodeBuyerAddressRequired,
		models.ErrCodeInvalidTitleCondition, models.ErrCodeTitleConditionOtherRequired,
		models.ErrCodeInspectionChecklistIncomplete, models.ErrCodeTitleConditionRequired:
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

// runSellerPayoutReconcile settles sales that completed with no ledger row.
//
// Completion arrives by three routes — the buyer's accept, the inspection
// window closing, and the payment_intent.succeeded webhook — and settling is
// best-effort in each. None of the capture sweeps looks at a row that is
// already 'completed', so without this a crash between the capture and the
// ledger write left the buyer's money taken and the seller never paid, with
// nothing anywhere looking for it.
func (h *PurchaseRequestHandler) runSellerPayoutReconcile(ctx context.Context) {
	if h.payoutH == nil || h.payoutRepo == nil {
		return
	}
	unpaid, err := h.payoutRepo.ListCompletedSalesWithoutPayout(ctx, 50)
	if err != nil {
		h.logger.Error("sale payout reconcile: list", "error", err)
		return
	}
	for _, u := range unpaid {
		h.logger.Warn("sale payout reconcile: completed sale had no payout row, settling it",
			"purchase_request_id", u.PurchaseID, "amount_cents", u.AmountCents)
		h.payoutH.SettleSalePayout(ctx, u.PurchaseID, u.SellerID, u.AmountCents, nil)
	}
}
