package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/urlsigner"
	"github.com/google/uuid"
)

// These tests cover the two support-attachment guarantees that don't need a
// database: in-chat evidence lives on a private (signature-gated) path, and
// every emitted attachment URL is signed. The DB-bound flow (upload → message
// row → fan-out) is exercised by the hand-test guide against a live server.

func testSupportHandler() *SupportHandler {
	return &SupportHandler{
		urlSigner: &PrivateURLSigner{Signer: urlsigner.New("test-secret"), TTL: time.Hour},
	}
}

// TestSupportAttachmentPathIsPrivate: /uploads/support/... must fall into the
// default-private branch so a chat photo is never publicly readable.
func TestSupportAttachmentPathIsPrivate(t *testing.T) {
	if !IsPrivateUploadPath("support/" + uuid.NewString() + "/attach_x.jpg") {
		t.Fatal("support attachment path must be classified PRIVATE (signature-gated)")
	}
}

// TestSignMessages_SignsEveryAttachment: signMessages must append ?sig=&exp= to
// every attachment on every message (the accident admin bug was forgetting
// exactly this on the admin side).
func TestSignMessages_SignsEveryAttachment(t *testing.T) {
	h := testSupportHandler()
	msgs := []repository.SupportMessageResponse{
		{
			ID: uuid.New(),
			Attachments: []repository.SupportMessageAttachment{
				{ID: uuid.New(), FileURL: "/uploads/support/abc/attach_1.jpg"},
				{ID: uuid.New(), FileURL: "/uploads/support/abc/attach_2.pdf"},
			},
		},
		{ID: uuid.New(), Body: "text only, no attachments"},
	}
	h.signMessages(msgs)
	for _, m := range msgs {
		for _, a := range m.Attachments {
			if !strings.Contains(a.FileURL, "sig=") || !strings.Contains(a.FileURL, "exp=") {
				t.Errorf("attachment URL not signed: %q", a.FileURL)
			}
		}
	}
}

// TestSignMessages_NilSignerSafe: a nil signer (local/test runs) must be a no-op,
// not a panic.
func TestSignMessages_NilSignerSafe(t *testing.T) {
	h := &SupportHandler{}
	h.signMessages([]repository.SupportMessageResponse{
		{ID: uuid.New(), Attachments: []repository.SupportMessageAttachment{{FileURL: "/uploads/support/abc/x.jpg"}}},
	})
}
