package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// ─── Product-tour ("onboarding") progress ────────────────────────────────────
//
// Namespace decision (DESIGN B): every Go/Swift SYMBOL lives in the
// ProductTour namespace (TourProgress, TourStatus, TourKey…). Only the backend
// TABLE (user_onboarding_progress) and ROUTE (/me/onboarding-progress) keep the
// human word "onboarding" — a table/route name cannot collide with the
// signup-flow onboarding_status ENUM or the Swift OnboardingStatus enum.

// TourStatus is the lifecycle of a single product-tour for a user.
type TourStatus string

const (
	TourStatusInProgress TourStatus = "in_progress"
	TourStatusCompleted  TourStatus = "completed"
	TourStatusSkipped    TourStatus = "skipped"
)

// IsValid reports whether s is a known tour status. Kept in lockstep with the
// user_onboarding_progress.status CHECK constraint (migration 000034).
func (s TourStatus) IsValid() bool {
	switch s {
	case TourStatusInProgress, TourStatusCompleted, TourStatusSkipped:
		return true
	}
	return false
}

// Bounds for a single upsert request. tour_key is a client-defined stable
// identifier (e.g. "driverTabs"); we cap its length and the batch size to keep
// a hostile client from writing unbounded rows.
const (
	TourKeyMaxLen        = 100
	TourProgressMaxBatch = 100
)

// TourProgress is one persisted product-tour state row for a user.
type TourProgress struct {
	UserID    uuid.UUID
	TourKey   string
	Status    TourStatus
	Step      int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ─── API shapes ──────────────────────────────────────────────────────────────

// UpsertTourProgressEntry is one tour's desired state in a PUT body.
type UpsertTourProgressEntry struct {
	TourKey string     `json:"tour_key"`
	Status  TourStatus `json:"status"`
	// Step is optional; omitted → 0. Pointer so an explicit 0 and an omitted
	// field are indistinguishable (both mean "start"), which is fine.
	Step *int `json:"step,omitempty"`
}

// UpsertTourProgressBody is the PUT /me/onboarding-progress payload. Semantics
// are MERGE-upsert: each entry upserts its (user, tour_key) row; rows not named
// in the body are left untouched. This avoids a partial PUT silently wiping a
// user's other tours.
type UpsertTourProgressBody struct {
	Entries []UpsertTourProgressEntry `json:"entries"`
}

// Validate normalizes and checks the batch. Returns a 400 APIError on any
// malformed entry. Trims tour_key in place and defaults an omitted status to
// completed (the common "I finished this tour" write).
func (b *UpsertTourProgressBody) Validate() *APIError {
	if len(b.Entries) == 0 {
		return NewValidationError("entries must contain at least one tour")
	}
	if len(b.Entries) > TourProgressMaxBatch {
		return NewValidationError("too many entries in a single request")
	}
	for i := range b.Entries {
		b.Entries[i].TourKey = strings.TrimSpace(b.Entries[i].TourKey)
		key := b.Entries[i].TourKey
		if key == "" {
			return NewValidationError("tour_key is required")
		}
		if len(key) > TourKeyMaxLen {
			return NewValidationError("tour_key is too long")
		}
		if b.Entries[i].Status == "" {
			b.Entries[i].Status = TourStatusCompleted
		}
		if !b.Entries[i].Status.IsValid() {
			return NewValidationError("status must be one of in_progress, completed, skipped")
		}
		if b.Entries[i].Step != nil && *b.Entries[i].Step < 0 {
			return NewValidationError("step must be zero or positive")
		}
	}
	return nil
}

// TourProgressResponse is one row in the GET/PUT response.
type TourProgressResponse struct {
	TourKey   string      `json:"tour_key"`
	Status    TourStatus  `json:"status"`
	Step      int         `json:"step"`
	UpdatedAt RFC3339Time `json:"updated_at"`
}

// TourProgressListResponse is the envelope returned by both endpoints.
type TourProgressListResponse struct {
	Progress []TourProgressResponse `json:"progress"`
}

// NewTourProgressResponse maps a domain row to its API shape.
func NewTourProgressResponse(t TourProgress) TourProgressResponse {
	return TourProgressResponse{
		TourKey:   t.TourKey,
		Status:    t.Status,
		Step:      t.Step,
		UpdatedAt: RFC3339Time(t.UpdatedAt),
	}
}
