package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
)

// Owner terms for rolling rentals.
//
// The driver's consent has been recorded verbatim since the first rolling
// lease could exist; the owner's agreement — how they are paid, what happens
// when a driver's card fails, what the capped guarantee does and does not
// cover — never had a record. Now it does, and an owner cannot ACCEPT a
// rolling lease request without one. The refusal carries the text, so the
// app can show it and record the acceptance in the same breath.

// SetOwnerTermsRepository wires the acceptance store; nil leaves the gate
// off (tests that predate it).
func (h *LeaseRequestHandler) SetOwnerTermsRepository(r *repository.OwnerTermsRepository) {
	h.ownerTermsRepo = r
}

// ownerTermsResponse is what GET /me/owner-terms returns and what the
// OWNER_TERMS_REQUIRED refusal carries in details.
type ownerTermsResponse struct {
	TermsVersion string     `json:"terms_version"`
	TermsText    string     `json:"terms_text"`
	AcceptedAt   *time.Time `json:"accepted_at,omitempty"`
}

func currentOwnerTerms() (version, text string) {
	return models.TermsVersionOwnerRollingV2, models.RollingOwnerTermsV2
}

// requireOwnerTermsForRollingAccept is the gate: an owner accepting a
// ROLLING lease request must have accepted the current owner package.
// Fixed-term requests are never gated. Returns true if the request was
// refused (response already written).
func (h *LeaseRequestHandler) requireOwnerTermsForRollingAccept(w http.ResponseWriter, r *http.Request, leaseID, userID uuid.UUID) bool {
	if h.ownerTermsRepo == nil {
		return false
	}
	lr, err := h.leaseRepo.GetByID(r.Context(), leaseID)
	if err != nil || lr == nil || lr.BillingMode != models.BillingModeRolling || lr.OwnerID != userID {
		return false // not rolling, or not the owner: the repo's own checks apply
	}
	version, text := currentOwnerTerms()
	ok, _, terr := h.ownerTermsRepo.Accepted(r.Context(), userID, version)
	if terr != nil {
		h.logger.Error("owner terms: lookup", "error", terr, "owner_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return true
	}
	if ok {
		return false
	}
	apiErr := models.NewAPIError("OWNER_TERMS_REQUIRED",
		"Before you accept a weekly rental, read and accept the owner terms for weekly rentals.")
	apiErr.Details = map[string]interface{}{
		"terms_version": version,
		"terms_text":    text,
	}
	httputil.WriteError(w, http.StatusConflict, apiErr)
	return true
}

// GetOwnerTerms — GET /me/owner-terms: the current package and whether the
// caller has accepted it.
func (h *LeaseRequestHandler) GetOwnerTerms(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	version, text := currentOwnerTerms()
	resp := ownerTermsResponse{TermsVersion: version, TermsText: text}
	if h.ownerTermsRepo != nil {
		if accepted, at, err := h.ownerTermsRepo.Accepted(r.Context(), userID, version); err == nil && accepted {
			resp.AcceptedAt = at
		}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

type acceptOwnerTermsBody struct {
	TermsVersion string `json:"terms_version"`
}

// AcceptOwnerTerms — POST /me/owner-terms/accept: records the caller's
// acceptance of the CURRENT version. The body must name that version, so a
// client showing stale text cannot record agreement to text it never showed.
func (h *LeaseRequestHandler) AcceptOwnerTerms(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	if h.ownerTermsRepo == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("OWNER_TERMS_UNAVAILABLE", "Owner terms aren't available right now."))
		return
	}
	var body acceptOwnerTermsBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	version, text := currentOwnerTerms()
	if strings.TrimSpace(body.TermsVersion) != version {
		apiErr := models.NewAPIError("OWNER_TERMS_VERSION_MISMATCH", "The terms you were shown are out of date — reload and read the current version.")
		apiErr.Details = map[string]interface{}{"terms_version": version, "terms_text": text}
		httputil.WriteError(w, http.StatusConflict, apiErr)
		return
	}
	if _, err := h.ownerTermsRepo.Record(r.Context(), userID, version, text, "app", nil, nil); err != nil {
		h.logger.Error("owner terms: record", "error", err, "owner_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	_, at, _ := h.ownerTermsRepo.Accepted(r.Context(), userID, version)
	httputil.WriteJSON(w, http.StatusOK, ownerTermsResponse{TermsVersion: version, TermsText: text, AcceptedAt: at})
}

type adminRecordOwnerTermsBody struct {
	Note string `json:"note"`
}

// AdminRecordOwnerTerms — POST /admin/users/{id}/owner-terms: records
// acceptance on an owner's behalf. The note is REQUIRED and must say how the
// agreement was obtained (read to them on a call, signed copy, …): this row
// is evidence, and evidence recorded by someone else needs provenance.
func (h *LeaseRequestHandler) AdminRecordOwnerTerms(w http.ResponseWriter, r *http.Request) {
	adminID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ownerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid user id"))
		return
	}
	if h.ownerTermsRepo == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, models.NewAPIError("OWNER_TERMS_UNAVAILABLE", "Owner terms aren't available right now."))
		return
	}
	var body adminRecordOwnerTermsBody
	if err := httputil.DecodeJSON(r, &body); err != nil || len(strings.TrimSpace(body.Note)) < 10 {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("note is required: say how the owner's agreement was obtained"))
		return
	}
	owner, err := h.userRepo.GetByID(r.Context(), ownerID)
	if err != nil || owner == nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("USER_NOT_FOUND", "User not found"))
		return
	}
	version, text := currentOwnerTerms()
	note := strings.TrimSpace(body.Note)
	created, err := h.ownerTermsRepo.Record(r.Context(), ownerID, version, text, "admin", &adminID, &note)
	if err != nil {
		h.logger.Error("owner terms: admin record", "error", err, "owner_id", ownerID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	h.logger.Info("owner terms: recorded by admin", "owner_id", ownerID, "admin_id", adminID, "created", created)
	_, at, _ := h.ownerTermsRepo.Accepted(r.Context(), ownerID, version)
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"terms_version": version, "accepted_at": at, "created": created,
	})
}
