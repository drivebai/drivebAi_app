package models

import (
	"time"

	"github.com/google/uuid"
)

// Profile is a role-scoped sub-account under a single user identity.
// Each user may have at most one profile per non-admin role (driver, car_owner)
// and one of them is designated active via users.active_profile_id.
type Profile struct {
	ID               uuid.UUID        `json:"id"`
	UserID           uuid.UUID        `json:"user_id"`
	Role             Role             `json:"role"`
	OnboardingStatus OnboardingStatus `json:"onboarding_status"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}
