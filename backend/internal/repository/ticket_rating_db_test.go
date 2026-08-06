package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/models"
)

// DB-gated tests for ticket ratings (7/24 item 3f). They execute the REAL
// queries — the transactional create+flag, the batch hydration, and the
// admin queue's LEFT JOIN — against a migrated Postgres, because this
// codebase has been bitten by queries that only failed once pgx prepared
// them in production (the $1 varchar/text deduction trap).
//
// Run with:
//
//	TEST_DATABASE_URL="postgres://…/drivebai_verify?sslmode=disable" \
//	  go test ./internal/repository/ -run TestTicketRating -v

func ticketRatingTestDB(t *testing.T) *database.DB {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL (migrated, disposable DB) to run ticket-rating tests")
	}
	db, err := database.Connect(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

// seedRatedTickets creates a user with one closed and one resolved ticket.
// Reviews cascade from the tickets, tickets cascade from the user.
func seedRatedTickets(t *testing.T, db *database.DB) (userID, closedID, resolvedID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]

	if err := db.Pool.QueryRow(ctx, `INSERT INTO users (email, role, first_name, last_name)
		VALUES ($1,'driver','Rate','Ticket') RETURNING id`, "rate_ticket_"+suffix+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	if err := db.Pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, category, description, status, submitted_at, closed_at)
		VALUES ($1,'other','closed one','closed',NOW(),NOW()) RETURNING id`, userID).Scan(&closedID); err != nil {
		t.Fatalf("seed closed ticket: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `INSERT INTO support_tickets (user_id, category, description, status, submitted_at, resolved_at)
		VALUES ($1,'other','resolved one','resolved',NOW(),NOW()) RETURNING id`, userID).Scan(&resolvedID); err != nil {
		t.Fatalf("seed resolved ticket: %v", err)
	}
	return userID, closedID, resolvedID
}

func TestTicketRating_CreateFlagHydrateAdmin(t *testing.T) {
	db := ticketRatingTestDB(t)
	ctx := context.Background()
	reviews := NewReviewRepository(db)
	tickets := NewTicketRepository(db)
	userID, closedID, resolvedID := seedRatedTickets(t, db)

	// 5★, no comment, no flag.
	five := &models.Review{
		AuthorID: userID, SubjectType: models.ReviewSubjectTicket,
		SubjectTicketID: &closedID, TransactionType: models.ReviewTransactionSupport,
		TransactionID: closedID, Rating: 5,
	}
	if err := reviews.CreateTicketRating(ctx, five, false); err != nil {
		t.Fatalf("5★ create: %v", err)
	}

	// Second rating on the same ticket → ErrAlreadyReviewed via unique index.
	dup := &models.Review{
		AuthorID: userID, SubjectType: models.ReviewSubjectTicket,
		SubjectTicketID: &closedID, TransactionType: models.ReviewTransactionSupport,
		TransactionID: closedID, Rating: 4, Comment: strPtr("again"),
	}
	if err := reviews.CreateTicketRating(ctx, dup, false); err != models.ErrAlreadyReviewed {
		t.Fatalf("duplicate: want ErrAlreadyReviewed, got %v", err)
	}

	// 2★ with comment + follow-up flag, in one transaction.
	two := &models.Review{
		AuthorID: userID, SubjectType: models.ReviewSubjectTicket,
		SubjectTicketID: &resolvedID, TransactionType: models.ReviewTransactionSupport,
		TransactionID: resolvedID, Rating: 2, Comment: strPtr("took too long"),
	}
	if err := reviews.CreateTicketRating(ctx, two, true); err != nil {
		t.Fatalf("2★ create: %v", err)
	}

	// The flag landed (and the widened ticketCols scan works).
	flagged, err := tickets.GetByIDForUser(ctx, resolvedID, userID)
	if err != nil {
		t.Fatalf("reload resolved ticket: %v", err)
	}
	if !flagged.NeedsFollowup {
		t.Error("2★ rating must set needs_followup")
	}
	unflagged, err := tickets.GetByIDForUser(ctx, closedID, userID)
	if err != nil {
		t.Fatalf("reload closed ticket: %v", err)
	}
	if unflagged.NeedsFollowup {
		t.Error("5★ rating must NOT set needs_followup")
	}

	// Batch hydration returns both, absent = unrated.
	m, err := reviews.GetTicketRatings(ctx, []uuid.UUID{closedID, resolvedID, uuid.New()})
	if err != nil {
		t.Fatalf("GetTicketRatings: %v", err)
	}
	if got := m[closedID]; got.Rating != 5 || got.Comment != nil {
		t.Errorf("closed ticket rating: want 5/no comment, got %+v", got)
	}
	if got := m[resolvedID]; got.Rating != 2 || got.Comment == nil || *got.Comment != "took too long" {
		t.Errorf("resolved ticket rating: want 2/'took too long', got %+v", got)
	}
	if len(m) != 2 {
		t.Errorf("unknown ticket must be absent from the map, got %d entries", len(m))
	}

	// Admin queue queries execute with the reviews LEFT JOIN and carry the
	// rating fields into the drawer shape.
	if _, err := tickets.AdminList(ctx, 1, 200, ""); err != nil {
		t.Fatalf("AdminList: %v", err)
	}
	row, err := tickets.AdminGet(ctx, resolvedID)
	if err != nil {
		t.Fatalf("AdminGet: %v", err)
	}
	if row.Rating == nil || *row.Rating != 2 {
		t.Errorf("AdminGet rating: want 2, got %v", row.Rating)
	}
	if row.RatingComment == nil || *row.RatingComment != "took too long" {
		t.Errorf("AdminGet rating comment: want 'took too long', got %v", row.RatingComment)
	}
	if !row.NeedsFollowup {
		t.Error("AdminGet must carry needs_followup")
	}
}

func strPtr(s string) *string { return &s }
