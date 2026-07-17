package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/google/uuid"
)

// reviewStore is the narrow interface ReviewHandler needs. The concrete
// *repository.ReviewRepository satisfies it; tests substitute a fake so the
// party/completion validation runs behaviorally without a DB.
type reviewStore interface {
	Create(ctx context.Context, rev *models.Review) error
	ResolveCompletedTransaction(ctx context.Context, txType models.ReviewTransactionType, txID uuid.UUID) (*repository.ReviewTransaction, error)
}

// ReviewHandler exposes rating submission for completed transactions.
type ReviewHandler struct {
	reviews reviewStore
	logger  *slog.Logger
}

func NewReviewHandler(reviews *repository.ReviewRepository, logger *slog.Logger) *ReviewHandler {
	return &ReviewHandler{reviews: reviews, logger: logger}
}

// SubmitReviewRequest rates a completed transaction in one call: the car
// (buyer/driver only — owners don't rate their own car) and/or the
// counterparty. At least one rating must be present.
type SubmitReviewRequest struct {
	TransactionType string  `json:"transaction_type"` // "purchase" | "rental"
	TransactionID   string  `json:"transaction_id"`
	CarRating       *int    `json:"car_rating,omitempty"`     // 1–5
	PartnerRating   *int    `json:"partner_rating,omitempty"` // 1–5
	Comment         *string `json:"comment,omitempty"`
}

type submitReviewResponse struct {
	OK        bool `json:"ok"`
	Submitted int  `json:"submitted"`
}

// SubmitReview — POST /api/v1/reviews
//
// Validates that the transaction exists AND is completed, that the caller is
// one of its parties, and that stars are 1–5. The car rating is only
// accepted from the consumer side (buyer / driver): the seller/owner rating
// their own vehicle would inflate the aggregate. Double submissions are
// rejected by the DB unique index as 409 REVIEW_ALREADY_EXISTS.
func (h *ReviewHandler) SubmitReview(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var req SubmitReviewRequest
	if err := DecodeJSON(r, &req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	txType := models.ReviewTransactionType(req.TransactionType)
	if !txType.IsValid() {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("transaction_type must be 'purchase' or 'rental'"))
		return
	}
	txID, err := uuid.Parse(req.TransactionID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid transaction_id"))
		return
	}
	if req.CarRating == nil && req.PartnerRating == nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("at least one of car_rating or partner_rating is required"))
		return
	}
	for _, rt := range []*int{req.CarRating, req.PartnerRating} {
		if rt != nil && (*rt < 1 || *rt > 5) {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("ratings must be between 1 and 5 stars"))
			return
		}
	}
	var comment *string
	if req.Comment != nil {
		if c := strings.TrimSpace(*req.Comment); c != "" {
			if len(c) > 1000 {
				c = c[:1000]
			}
			comment = &c
		}
	}

	tx, err := h.reviews.ResolveCompletedTransaction(r.Context(), txType, txID)
	if err != nil {
		h.logger.Error("review: resolve transaction", "error", err, "tx_id", txID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if tx == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "No completed transaction found to review"))
		return
	}

	isConsumer := userID == tx.ConsumerSideID // buyer / driver
	isOwnerSide := userID == tx.OwnerSideID   // seller / owner
	if !isConsumer && !isOwnerSide {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Only a party to the transaction can rate it"))
		return
	}
	if req.CarRating != nil && !isConsumer {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Only the buyer or driver can rate the vehicle"))
		return
	}

	counterparty := tx.OwnerSideID
	if isOwnerSide {
		counterparty = tx.ConsumerSideID
	}

	submitted := 0
	if req.CarRating != nil {
		carID := tx.CarID
		rev := &models.Review{
			AuthorID:        userID,
			SubjectType:     models.ReviewSubjectCar,
			SubjectCarID:    &carID,
			TransactionType: txType,
			TransactionID:   txID,
			Rating:          *req.CarRating,
			Comment:         comment,
		}
		if err := h.createOne(r.Context(), w, rev); err != nil {
			return
		}
		submitted++
	}
	if req.PartnerRating != nil {
		partnerID := counterparty
		rev := &models.Review{
			AuthorID:        userID,
			SubjectType:     models.ReviewSubjectUser,
			SubjectUserID:   &partnerID,
			TransactionType: txType,
			TransactionID:   txID,
			Rating:          *req.PartnerRating,
			Comment:         comment,
		}
		if err := h.createOne(r.Context(), w, rev); err != nil {
			return
		}
		submitted++
	}

	httputil.WriteJSON(w, http.StatusCreated, submitReviewResponse{OK: true, Submitted: submitted})
}

// createOne inserts a review and writes the HTTP error itself on failure
// (non-nil return means the response is already written).
func (h *ReviewHandler) createOne(ctx context.Context, w http.ResponseWriter, rev *models.Review) error {
	if err := h.reviews.Create(ctx, rev); err != nil {
		if err == models.ErrAlreadyReviewed {
			httputil.WriteError(w, http.StatusConflict, models.ErrAlreadyReviewed)
			return err
		}
		h.logger.Error("review: create", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return err
	}
	return nil
}
