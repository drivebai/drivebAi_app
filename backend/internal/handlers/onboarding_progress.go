package handlers

import (
	"net/http"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// OnboardingHandler serves the product-tour ("onboarding") progress endpoints.
//
// Strict authorization: both endpoints derive the user id SOLELY from the
// authenticated JWT (httputil.GetUserID). There is no user_id path or body
// parameter, so a user can only ever read or write their own rows — user A
// cannot address, let alone mutate, user B's progress.
type OnboardingHandler struct {
	repo *repository.OnboardingProgressRepository
}

func NewOnboardingHandler(repo *repository.OnboardingProgressRepository) *OnboardingHandler {
	return &OnboardingHandler{repo: repo}
}

// GetProgress — GET /api/v1/me/onboarding-progress
func (h *OnboardingHandler) GetProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	rows, err := h.repo.ListForUser(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toTourProgressList(rows))
}

// UpdateProgress — PUT /api/v1/me/onboarding-progress
//
// Merge-upsert: each entry upserts its own (user, tour_key) row; tours not
// named in the body are left untouched. Returns the user's full row set.
func (h *OnboardingHandler) UpdateProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	var body models.UpsertTourProgressBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if apiErr := body.Validate(); apiErr != nil {
		httputil.WriteError(w, http.StatusBadRequest, apiErr)
		return
	}
	rows, err := h.repo.UpsertMany(r.Context(), userID, body.Entries)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toTourProgressList(rows))
}

// ResetProgress — DELETE /api/v1/me/onboarding-progress
//
// Clears the caller's own product-tour progress so the tours replay from
// scratch. This backs the QA reset in the debug builds of the app. It is
// self-service, scoped to the JWT's user id, and reaches nothing but that
// user's tour rows — it is not an admin control and destroys no product data.
func (h *OnboardingHandler) ResetProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	if err := h.repo.DeleteForUser(r.Context(), userID); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Inline shape: models.MessageResponse is a chat message, not an ack.
	httputil.WriteJSON(w, http.StatusOK, map[string]string{"message": "Onboarding progress reset"})
}

func toTourProgressList(rows []models.TourProgress) models.TourProgressListResponse {
	out := models.TourProgressListResponse{Progress: make([]models.TourProgressResponse, 0, len(rows))}
	for _, t := range rows {
		out.Progress = append(out.Progress, models.NewTourProgressResponse(t))
	}
	return out
}
