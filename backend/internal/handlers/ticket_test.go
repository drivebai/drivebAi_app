package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/urlsigner"
	"github.com/google/uuid"
)

// These tests cover the two ticket guarantees that don't need a database: every
// emitted attachment URL is signed, ticket evidence is classified private, and
// the category enum is exactly the six approved values. The DB-bound flows
// (draft lifecycle, 409-on-submitted-edit, submit validation) are exercised by
// the hand-test guide against a live server.

func testTicketHandler() *TicketHandler {
	return &TicketHandler{
		urlSigner: &PrivateURLSigner{Signer: urlsigner.New("test-secret"), TTL: time.Hour},
	}
}

// TestTicketEvidencePathIsPrivate: the /uploads/tickets/... prefix must fall
// into the default-private branch so evidence is never publicly readable.
func TestTicketEvidencePathIsPrivate(t *testing.T) {
	if !IsPrivateUploadPath("tickets/" + uuid.NewString() + "/evidence_x.jpg") {
		t.Fatal("ticket evidence path must be classified PRIVATE (signature-gated)")
	}
	// Sanity: a public car photo is NOT private, so the classifier isn't just
	// returning true for everything.
	if IsPrivateUploadPath("cars/" + uuid.NewString() + "/cover_front_x.jpg") {
		t.Fatal("car photo path must be PUBLIC")
	}
}

// TestSignTicket_SignsEveryAttachment: signTicket must append ?sig=&exp= to
// every attachment URL (the accident admin bug was forgetting exactly this).
func TestSignTicket_SignsEveryAttachment(t *testing.T) {
	h := testTicketHandler()
	tk := &models.SupportTicket{
		ID: uuid.New(),
		Attachments: []models.TicketAttachment{
			{ID: uuid.New(), FileURL: "/uploads/tickets/abc/evidence_1.jpg"},
			{ID: uuid.New(), FileURL: "/uploads/tickets/abc/evidence_2.pdf"},
		},
	}
	h.signTicket(tk)
	for _, a := range tk.Attachments {
		if !strings.Contains(a.FileURL, "sig=") || !strings.Contains(a.FileURL, "exp=") {
			t.Errorf("attachment URL not signed: %q", a.FileURL)
		}
	}
}

// TestSignTicket_NilSafe: signing a nil ticket or empty attachments must not panic.
func TestSignTicket_NilSafe(t *testing.T) {
	h := testTicketHandler()
	h.signTicket(nil)
	h.signTicket(&models.SupportTicket{Attachments: nil})
}

// TestTicketCategories_ExactlySix: the server-side enum is the six approved
// categories, no more, no fewer.
func TestTicketCategories_ExactlySix(t *testing.T) {
	want := map[models.TicketCategory]bool{
		models.TicketCategoryAccount:  true,
		models.TicketCategoryListing:  true,
		models.TicketCategoryRenting:  true,
		models.TicketCategoryBuySell:  true,
		models.TicketCategoryPayments: true,
		models.TicketCategoryOther:    true,
	}
	if len(models.ValidTicketCategories) != len(want) {
		t.Fatalf("expected %d categories, got %d", len(want), len(models.ValidTicketCategories))
	}
	for c := range want {
		if !models.ValidTicketCategories[c] {
			t.Errorf("missing category %q", c)
		}
	}
	// A made-up category is rejected.
	if models.ValidTicketCategories["banana"] {
		t.Error("unknown category must not validate")
	}
}
