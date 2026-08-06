package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ReviewRepository struct {
	db *database.DB
}

func NewReviewRepository(db *database.DB) *ReviewRepository {
	return &ReviewRepository{db: db}
}

// Create inserts one review row. A unique-index violation (author already
// rated this subject kind for this transaction) comes back as
// models.ErrAlreadyReviewed.
func (r *ReviewRepository) Create(ctx context.Context, rev *models.Review) error {
	if rev.ID == uuid.Nil {
		rev.ID = uuid.New()
	}
	err := r.db.Pool.QueryRow(ctx, `
		INSERT INTO reviews (id, author_id, subject_type, subject_car_id, subject_user_id,
		                     transaction_type, transaction_id, rating, comment)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at
	`, rev.ID, rev.AuthorID, rev.SubjectType, rev.SubjectCarID, rev.SubjectUserID,
		rev.TransactionType, rev.TransactionID, rev.Rating, rev.Comment,
	).Scan(&rev.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "reviews_once_per_transaction_idx") {
			return models.ErrAlreadyReviewed
		}
		return err
	}
	return nil
}

// CreateTicketRating inserts a support-ticket rating and — for ratings of 3★
// or below — flags the ticket for admin follow-up in the SAME transaction, so
// a flag can never exist without its rating or vice versa. Both unique
// indexes (one-per-ticket and the 000038 composite) surface as
// models.ErrAlreadyReviewed.
func (r *ReviewRepository) CreateTicketRating(ctx context.Context, rev *models.Review, flagFollowup bool) error {
	if rev.ID == uuid.Nil {
		rev.ID = uuid.New()
	}
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO reviews (id, author_id, subject_type, subject_ticket_id,
		                     transaction_type, transaction_id, rating, comment)
		VALUES ($1, $2, 'ticket', $3, 'support', $4, $5, $6)
		RETURNING created_at
	`, rev.ID, rev.AuthorID, rev.SubjectTicketID, rev.TransactionID, rev.Rating, rev.Comment,
	).Scan(&rev.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "reviews_one_per_ticket_idx") ||
			strings.Contains(err.Error(), "reviews_once_per_transaction_idx") {
			return models.ErrAlreadyReviewed
		}
		return err
	}

	if flagFollowup {
		if _, err := tx.Exec(ctx, `
			UPDATE support_tickets SET needs_followup = TRUE, updated_at = NOW()
			WHERE id = $1`, rev.SubjectTicketID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetTicketRatings batch-loads ratings for many tickets at once (My requests /
// admin queue) — tickets with no rating are simply absent from the map.
func (r *ReviewRepository) GetTicketRatings(ctx context.Context, ticketIDs []uuid.UUID) (map[uuid.UUID]models.TicketRating, error) {
	out := map[uuid.UUID]models.TicketRating{}
	if len(ticketIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT subject_ticket_id, rating, comment
		FROM reviews
		WHERE subject_ticket_id = ANY($1)
	`, ticketIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var tr models.TicketRating
		if err := rows.Scan(&id, &tr.Rating, &tr.Comment); err != nil {
			return nil, err
		}
		out[id] = tr
	}
	return out, rows.Err()
}

// GetUserRating returns the aggregate rating a user has received (as seller,
// buyer, owner, or driver). Average is nil when there are no reviews.
func (r *ReviewRepository) GetUserRating(ctx context.Context, userID uuid.UUID) (models.RatingSummary, error) {
	var s models.RatingSummary
	err := r.db.Pool.QueryRow(ctx, `
		SELECT AVG(rating)::float8, COUNT(*)
		FROM reviews
		WHERE subject_user_id = $1
	`, userID).Scan(&s.Average, &s.Count)
	return s, err
}

// GetCarRating returns the aggregate rating of one car.
func (r *ReviewRepository) GetCarRating(ctx context.Context, carID uuid.UUID) (models.RatingSummary, error) {
	var s models.RatingSummary
	err := r.db.Pool.QueryRow(ctx, `
		SELECT AVG(rating)::float8, COUNT(*)
		FROM reviews
		WHERE subject_car_id = $1
	`, carID).Scan(&s.Average, &s.Count)
	return s, err
}

// GetUserRatings batch-loads aggregates for many users at once (list paths —
// avoids an N+1 over Discover / admin pages). Users with no reviews are
// simply absent from the map.
func (r *ReviewRepository) GetUserRatings(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]models.RatingSummary, error) {
	out := map[uuid.UUID]models.RatingSummary{}
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.Pool.Query(ctx, `
		SELECT subject_user_id, AVG(rating)::float8, COUNT(*)
		FROM reviews
		WHERE subject_user_id = ANY($1)
		GROUP BY subject_user_id
	`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var s models.RatingSummary
		if err := rows.Scan(&id, &s.Average, &s.Count); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// ReviewTransaction is the resolved anchor a review request points at.
type ReviewTransaction struct {
	CarID          uuid.UUID
	OwnerSideID    uuid.UUID // seller (purchase) / owner (rental)
	ConsumerSideID uuid.UUID // buyer (purchase) / driver (rental)
}

// ResolveCompletedTransaction loads the transaction a review is anchored to
// and returns its parties — ONLY if it is completed. Returns (nil, nil) when
// the row doesn't exist or isn't completed, so the handler can 404 without
// leaking which of the two it was.
func (r *ReviewRepository) ResolveCompletedTransaction(ctx context.Context, txType models.ReviewTransactionType, txID uuid.UUID) (*ReviewTransaction, error) {
	var t ReviewTransaction
	var err error
	switch txType {
	case models.ReviewTransactionPurchase:
		err = r.db.Pool.QueryRow(ctx, `
			SELECT car_id, seller_id, buyer_id
			FROM purchase_requests
			WHERE id = $1 AND status = 'completed'
		`, txID).Scan(&t.CarID, &t.OwnerSideID, &t.ConsumerSideID)
	case models.ReviewTransactionRental:
		err = r.db.Pool.QueryRow(ctx, `
			SELECT car_id, owner_id, driver_id
			FROM vehicle_returns
			WHERE id = $1 AND status = 'completed'
		`, txID).Scan(&t.CarID, &t.OwnerSideID, &t.ConsumerSideID)
	default:
		return nil, nil
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}
