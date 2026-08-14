package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/urlsigner"
)

// Wire-shape and signing tests for the driver's own document responses
// (client point 1a) and validation-branch tests for the admin document
// replacement (points 3+4). DB-free, per the package's style: every branch
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

// ── ReplaceUserDocument validation branches ──────────────────────────────
// (discardLogger comes from admin_qa_round_test.go — same package.)

func replaceUserDocReq(t *testing.T, id string, contentType string, body *bytes.Buffer) *http.Request {
	t.Helper()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+id+"/documents", body)
	req.Header.Set("Content-Type", contentType)
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

// Missing wiring must 500 cleanly, never nil-panic.
func TestReplaceUserDocument_DependencyGuard(t *testing.T) {
	h := &AdminHandler{logger: discardLogger()}
	rr := httptest.NewRecorder()
	h.ReplaceUserDocument(rr, replaceUserDocReq(t, uuid.New().String(), "application/json", bytes.NewBufferString(`{}`)))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("unwired handler must 500, got %d", rr.Code)
	}
}

func wiredButDBFreeAdminHandler() *AdminHandler {
	// Non-nil deps satisfy the wiring guard; the branches under test return
	// before any repo method runs, so the nil inner DB is never touched.
	return &AdminHandler{
		docRepo:   repository.NewDocumentRepository(nil),
		userRepo:  repository.NewUserRepository(nil),
		uploadDir: "/tmp/never-used",
		logger:    discardLogger(),
	}
}

func TestReplaceUserDocument_InvalidID(t *testing.T) {
	h := wiredButDBFreeAdminHandler()
	rr := httptest.NewRecorder()
	h.ReplaceUserDocument(rr, replaceUserDocReq(t, "not-a-uuid", "application/json", bytes.NewBufferString(`{}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("malformed user id must 400, got %d", rr.Code)
	}
}

// The mime allow-list must match the driver's own upload whitelist:
// JPEG/PNG/PDF in; video, HTML, SVG and octet-stream out — whatever the
// support chat accepts.
func TestReplaceUserDocument_MimeAllowList(t *testing.T) {
	h := wiredButDBFreeAdminHandler()

	buildMultipart := func(mime string) (*bytes.Buffer, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("document_type", string(models.DocumentDriversLicense))
		hdr := make(map[string][]string)
		hdr["Content-Disposition"] = []string{`form-data; name="file"; filename="x.bin"`}
		hdr["Content-Type"] = []string{mime}
		pw, _ := mw.CreatePart(hdr)
		pw.Write([]byte("payload"))
		mw.Close()
		return &buf, mw.FormDataContentType()
	}

	for _, mime := range []string{"video/mp4", "video/quicktime", "text/html", "image/svg+xml", "application/octet-stream"} {
		body, ct := buildMultipart(mime)
		rr := httptest.NewRecorder()
		h.ReplaceUserDocument(rr, replaceUserDocReq(t, uuid.New().String(), ct, body))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("mime %s must be rejected with 400, got %d", mime, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "INVALID_FILE_TYPE") {
			t.Errorf("mime %s: expected INVALID_FILE_TYPE, got %s", mime, rr.Body.String())
		}
	}

	// And the allowed set passes the mime gate (failing later on the DB-free
	// user lookup is fine — the branch under test is the whitelist).
	for _, mime := range []string{"image/jpeg", "image/png", "application/pdf"} {
		body, ct := buildMultipart(mime)
		rr := httptest.NewRecorder()
		func() {
			defer func() { recover() }() // GetByID on the nil-DB repo may panic; the mime gate has passed by then
			h.ReplaceUserDocument(rr, replaceUserDocReq(t, uuid.New().String(), ct, body))
		}()
		if rr.Code == http.StatusBadRequest && strings.Contains(rr.Body.String(), "INVALID_FILE_TYPE") {
			t.Errorf("mime %s must pass the whitelist", mime)
		}
	}
}

// ── Admin email edit validation branches (point 2) ───────────────────────

func adminEmailReq(t *testing.T, emailJSON string) *http.Request {
	t.Helper()
	id := uuid.New().String()
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/users/"+id+"/profile", strings.NewReader(emailJSON))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), httputil.UserIDKey, uuid.New())
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestAdminUpdateUserProfile_EmailFormat(t *testing.T) {
	h := &AdminHandler{}
	for _, bad := range []string{`{"email":"no-at-sign"}`, `{"email":"   "}`, `{"email":""}`} {
		rr := httptest.NewRecorder()
		h.UpdateUserProfile(rr, adminEmailReq(t, bad))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s: expected 400, got %d", bad, rr.Code)
		}
	}
}

func TestAdminUpdateUserProfile_EmailTooLong(t *testing.T) {
	h := &AdminHandler{}
	long := strings.Repeat("a", 250) + "@example.com" // > 255 total
	rr := httptest.NewRecorder()
	h.UpdateUserProfile(rr, adminEmailReq(t, `{"email":"`+long+`"}`))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("over-length email must 400, got %d", rr.Code)
	}
}
