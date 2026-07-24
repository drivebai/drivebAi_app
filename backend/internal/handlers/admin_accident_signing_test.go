package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/urlsigner"
	"github.com/google/uuid"
)

// The admin accident endpoints (ListAccidents/GetAccident) emitted raw /uploads
// URLs, so admin accident evidence + the signature image 404'd in production
// under signature enforcement. These tests pin the fix: every private URL on an
// admin accident row is signed on the way out.

func testAdminHandler() *AdminHandler {
	return &AdminHandler{urlSigner: &PrivateURLSigner{Signer: urlsigner.New("test-secret"), TTL: time.Hour}}
}

func TestSignAccidentRow_SignsEveryURL(t *testing.T) {
	h := testAdminHandler()
	row := &repository.AdminAccidentRow{
		SignatureURL: "/uploads/accidents/abc/signature_1.png",
		Attachments: []models.AccidentAttachment{
			{ID: uuid.New(), FileURL: "/uploads/accidents/abc/accident_photo_1.jpg"},
			{ID: uuid.New(), FileURL: "/uploads/accidents/abc/driver1_license_1.jpg"},
		},
	}
	h.signAccidentRow(row)

	if !strings.Contains(row.SignatureURL, "sig=") || !strings.Contains(row.SignatureURL, "exp=") {
		t.Errorf("signature URL not signed: %q", row.SignatureURL)
	}
	for _, a := range row.Attachments {
		if !strings.Contains(a.FileURL, "sig=") || !strings.Contains(a.FileURL, "exp=") {
			t.Errorf("attachment URL not signed: %q", a.FileURL)
		}
	}
}

// A row with no signature and no attachments must not panic, and an empty
// signature URL stays empty (Sign is a no-op on "").
func TestSignAccidentRow_NilAndEmptySafe(t *testing.T) {
	h := testAdminHandler()
	h.signAccidentRow(nil)
	row := &repository.AdminAccidentRow{}
	h.signAccidentRow(row)
	if row.SignatureURL != "" {
		t.Errorf("empty signature URL should stay empty, got %q", row.SignatureURL)
	}
}

// Accident evidence paths must classify PRIVATE so they are signature-gated.
func TestAccidentEvidencePathIsPrivate(t *testing.T) {
	if !IsPrivateUploadPath("accidents/" + uuid.NewString() + "/accident_photo_1.jpg") {
		t.Fatal("accident evidence path must be classified PRIVATE")
	}
}
