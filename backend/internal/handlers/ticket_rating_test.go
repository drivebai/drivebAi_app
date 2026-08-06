package handlers

import (
	"os"
	"strings"
	"testing"
)

// Source-level wiring locks for ticket ratings (7/24 item 3f), in the style
// of TestReservationGuard_WiredIntoCreateAndAccept: the client's rating rules
// live in RateTicket, and removing any of them fails the build tests.

// TestRateTicket_CommentRules: 4★ and below must require a comment, and the
// comment must be normalized (trim/empty→nil) BEFORE that check so a
// whitespace-only comment doesn't slip past the DB CHECK.
func TestRateTicket_CommentRules(t *testing.T) {
	src, err := os.ReadFile("ticket.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := extractFunc(t, string(src), "func (h *TicketHandler) RateTicket(")
	if !strings.Contains(body, "body.Rating <= 4 && comment == nil") {
		t.Error("RateTicket must require a comment for ratings of 4★ and below")
	}
	if !strings.Contains(body, "strings.TrimSpace(*body.Comment)") {
		t.Error("RateTicket must normalize the comment (trim, empty → nil) before the 4★-and-below check")
	}
}

// TestRateTicket_FollowupAndLifecycle: 3★ and below must flag follow-up
// through the transactional create path, and only resolved/closed tickets
// are rateable.
func TestRateTicket_FollowupAndLifecycle(t *testing.T) {
	src, err := os.ReadFile("ticket.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := extractFunc(t, string(src), "func (h *TicketHandler) RateTicket(")
	if !strings.Contains(body, "body.Rating <= 3") {
		t.Error("RateTicket must flag needs_followup for ratings of 3★ and below")
	}
	if !strings.Contains(body, "CreateTicketRating(") {
		t.Error("RateTicket must write through reviewRepo.CreateTicketRating (rating + flag in one DB transaction)")
	}
	if !strings.Contains(body, "TicketStatusResolved") || !strings.Contains(body, "TicketStatusClosed") {
		t.Error("RateTicket must gate on resolved/closed status")
	}
	if !strings.Contains(body, "GetByIDForUser(") {
		t.Error("RateTicket must load via GetByIDForUser so only the reporter can rate")
	}
	if !strings.Contains(body, "ErrAlreadyReviewed") {
		t.Error("RateTicket must surface the one-per-ticket unique index as 409 REVIEW_ALREADY_EXISTS")
	}
}

// TestGenericReviews_StillExcludeSupport: the generic POST /reviews endpoint
// serves purchase/rental only — ticket ratings have their own endpoint with
// their own rules, and widening IsValid would silently open a second,
// rule-free write path for support ratings.
func TestGenericReviews_StillExcludeSupport(t *testing.T) {
	src, err := os.ReadFile("../models/review.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(src)
	start := strings.Index(s, "func (t ReviewTransactionType) IsValid()")
	if start == -1 {
		t.Fatal("IsValid not found")
	}
	end := strings.Index(s[start:], "}")
	body := s[start : start+end]
	if strings.Contains(body, "ReviewTransactionSupport") {
		t.Error("IsValid must NOT accept 'support' — ticket ratings go through POST /tickets/{id}/rating only")
	}
}
