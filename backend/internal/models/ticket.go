package models

import (
	"time"

	"github.com/google/uuid"
)

// TicketStatus is the lifecycle state of a support ticket.
//
// draft -> open -> resolved -> closed, with reopen (resolved -> open). There is
// deliberately no "in_progress": with a single admin it is a state that exists
// to be skipped, and every state an admin must remember to move is one that goes
// stale and makes the queue lie. The "a human has this" signal comes from the
// admin replying in the support chat, which is stronger evidence than a chip.
type TicketStatus string

const (
	TicketStatusDraft    TicketStatus = "draft"
	TicketStatusOpen     TicketStatus = "open"
	TicketStatusResolved TicketStatus = "resolved"
	TicketStatusClosed   TicketStatus = "closed"
)

// TicketCategory is a SERVER-SIDE enum (not a bare int whose labels live only in
// the iOS binary, as accident.diagram is) so the admin queue can triage by it.
// The six values are derived from the client's own issue tracker distribution,
// not a generic helpdesk template.
type TicketCategory string

const (
	TicketCategoryAccount  TicketCategory = "account"  // sign-up, verification, mode switch, documents
	TicketCategoryListing  TicketCategory = "listing"  // owner: creating/editing a listing, photos, VIN
	TicketCategoryRenting  TicketCategory = "renting"  // driver: booking, pickup/handover, returns
	TicketCategoryBuySell  TicketCategory = "buy_sell" // offers, Bill of Sale, title, inspection, sold
	TicketCategoryPayments TicketCategory = "payments" // a charge, a stuck payment, a refund
	TicketCategoryOther    TicketCategory = "other"    // report a problem / safety concern / anything else
)

// ValidTicketCategories gates what a client may set. Unknown categories are
// rejected at submit so the queue never shows a category the admin can't read.
var ValidTicketCategories = map[TicketCategory]bool{
	TicketCategoryAccount:  true,
	TicketCategoryListing:  true,
	TicketCategoryRenting:  true,
	TicketCategoryBuySell:  true,
	TicketCategoryPayments: true,
	TicketCategoryOther:    true,
}

// SupportTicket is the core domain model — the structured record.
type SupportTicket struct {
	ID          uuid.UUID          `json:"id"`
	UserID      uuid.UUID          `json:"user_id"`
	Category    TicketCategory     `json:"category"`
	Subject     string             `json:"subject"`
	Description string             `json:"description"`
	Status      TicketStatus       `json:"status"`
	LastStep    int                `json:"last_step"`
	// NeedsFollowup is set (in the same transaction as the rating insert)
	// when the user rates a finished request 3★ or below.
	NeedsFollowup bool       `json:"needs_followup"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	// Rating is the user's 1–5★ rating of how the request was handled,
	// hydrated from reviews (subject_ticket_id); nil = not rated yet.
	Rating      *int               `json:"rating,omitempty"`
	Attachments []TicketAttachment `json:"attachments"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// TicketAttachment is a single piece of evidence on a ticket.
type TicketAttachment struct {
	ID        uuid.UUID `json:"id"`
	TicketID  uuid.UUID `json:"ticket_id"`
	FileURL   string    `json:"file_url"`
	FileSize  int64     `json:"file_size"`
	MimeType  string    `json:"mime_type"`
	CreatedAt time.Time `json:"created_at"`
}

// AdminTicketRow is the admin-queue-facing shape: the ticket plus the reporter's
// identity, so the queue and drawer don't need a second lookup.
type AdminTicketRow struct {
	SupportTicket
	UserName  string `json:"user_name"`
	UserEmail string `json:"user_email"`
	UserRole  string `json:"user_role"`
	// RatingComment accompanies the embedded Rating for the drawer (the
	// user's "what could have gone better"); nil when unrated or 5★-silent.
	RatingComment *string `json:"rating_comment,omitempty"`
}
