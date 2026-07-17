package models

import (
	"time"

	"github.com/google/uuid"
)

// ReviewSubjectType — what is being rated.
type ReviewSubjectType string

const (
	ReviewSubjectCar  ReviewSubjectType = "car"
	ReviewSubjectUser ReviewSubjectType = "user"
)

// ReviewTransactionType — the completed transaction a review is anchored to.
type ReviewTransactionType string

const (
	ReviewTransactionPurchase ReviewTransactionType = "purchase"
	ReviewTransactionRental   ReviewTransactionType = "rental"
)

func (t ReviewTransactionType) IsValid() bool {
	return t == ReviewTransactionPurchase || t == ReviewTransactionRental
}

// Review is one 1–5-star rating of a car or a user, tied to a completed
// purchase or rental. The unique index on
// (transaction_type, transaction_id, author_id, subject_type) makes
// double-rating impossible at the DB layer.
type Review struct {
	ID              uuid.UUID             `json:"id"`
	AuthorID        uuid.UUID             `json:"author_id"`
	SubjectType     ReviewSubjectType     `json:"subject_type"`
	SubjectCarID    *uuid.UUID            `json:"subject_car_id,omitempty"`
	SubjectUserID   *uuid.UUID            `json:"subject_user_id,omitempty"`
	TransactionType ReviewTransactionType `json:"transaction_type"`
	TransactionID   uuid.UUID             `json:"transaction_id"`
	Rating          int                   `json:"rating"`
	Comment         *string               `json:"comment,omitempty"`
	CreatedAt       time.Time             `json:"created_at"`
}

// RatingSummary is the aggregate exposed on cars, owners, and admin rows.
// Average is nil when Count == 0 so clients can render "No ratings yet"
// instead of a fake 5.0 or a misleading 0.0.
type RatingSummary struct {
	Average *float64 `json:"average,omitempty"`
	Count   int      `json:"count"`
}

// ErrAlreadyReviewed — the author already rated this subject kind for this
// transaction (unique-index violation surfaced as a 409).
var ErrAlreadyReviewed = &APIError{Code: "REVIEW_ALREADY_EXISTS", Message: "You have already submitted this rating"}
