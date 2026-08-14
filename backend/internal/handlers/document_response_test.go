package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/urlsigner"
)

// Wire-shape and signing tests for the driver's own document responses
// (client point 1a). DB-free, per the package's style: every branch
// exercised returns before repo access.

func testDoc(userID uuid.UUID, dt models.DocumentType) *models.Document {
	return &models.Document{
		ID:        uuid.New(),
		UserID:    userID,
		Type:      dt,
		FileName:  "license.jpg",
		FilePath:  "/data/uploads/" + userID.String() + "/drivers_license_abc123.jpg",
		FileSize:  1234,
		MimeType:  "image/jpeg",
		Status:    models.DocumentStatusUploaded,
		CreatedAt: time.Now(),
	}
}

// file_url must be present under that exact key and carry a signature —
// iOS decodes by key name, so a tag drift silently re-breaks the exact bug
// the client reported ("cannot click to view the file I just uploaded").
func TestDocumentResponse_WireShape_Signed(t *testing.T) {
	h := &UserHandler{urlSigner: &PrivateURLSigner{Signer: urlsigner.New("test-secret"), TTL: time.Hour}}
	userID := uuid.New()

	resp := h.documentResponse(testDoc(userID, models.DocumentDriversLicense))
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	fileURL, ok := decoded["file_url"].(string)
	if !ok || fileURL == "" {
		t.Fatalf("file_url must be present and non-empty, got %v", decoded["file_url"])
	}
	if !strings.Contains(fileURL, "sig=") || !strings.Contains(fileURL, "exp=") {
		t.Errorf("private document URL must be signed (sig=, exp=), got %q", fileURL)
	}
	if !strings.HasPrefix(fileURL, "/uploads/"+userID.String()+"/") {
		t.Errorf("URL must follow /uploads/{userID}/{file}, got %q", fileURL)
	}
	// The absolute on-disk path must never leak into the response.
	if strings.Contains(string(raw), "/data/uploads") {
		t.Errorf("disk path leaked into response: %s", raw)
	}
	if decoded["mime_type"] != "image/jpeg" {
		t.Errorf("mime_type must round-trip, got %v", decoded["mime_type"])
	}
}

// A nil signer degrades to the unsigned relative path — the dev config.
func TestDocumentResponse_NilSigner_RelativePath(t *testing.T) {
	h := &UserHandler{} // no signer wired
	userID := uuid.New()

	resp := h.documentResponse(testDoc(userID, models.DocumentTLCLicense))
	want := "/uploads/" + userID.String() + "/drivers_license_abc123.jpg"
	if resp.FileURL != want {
		t.Errorf("nil signer must yield the bare relative path %q, got %q", want, resp.FileURL)
	}
}

// The client's complaint was specifically about a non-licence type (TLC):
// every one of the five document types must produce a usable URL.
func TestDocumentResponse_AllTypes(t *testing.T) {
	h := &UserHandler{urlSigner: &PrivateURLSigner{Signer: urlsigner.New("test-secret"), TTL: time.Hour}}
	userID := uuid.New()

	for _, dt := range []models.DocumentType{
		models.DocumentDriversLicense,
		models.DocumentRegistration,
		models.DocumentCommercialLicense,
		models.DocumentTLCLicense,
		models.DocumentOther,
	} {
		resp := h.documentResponse(testDoc(userID, dt))
		if !strings.HasPrefix(resp.FileURL, "/uploads/") || !strings.Contains(resp.FileURL, "sig=") {
			t.Errorf("type %s: unusable URL %q", dt, resp.FileURL)
		}
	}
}
