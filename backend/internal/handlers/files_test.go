package handlers

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drivebai/backend/internal/urlsigner"
)

func TestIsPrivateUploadPath(t *testing.T) {
	cases := []struct {
		path    string
		private bool
	}{
		// Public surfaces — must not require signatures.
		{"cars/abc/cover.jpg", false},
		{"cars/abc/photos/00.jpg", false},
		{"a1b2c3d4-e5f6-7890-abcd-ef1234567890/profile_xyz.png", false},
		// Private surfaces.
		{"chats/abc/file.jpg", true},
		{"accidents/abc/signature_xyz.png", true},
		{"accidents/abc/photo_0.jpg", true},
		{"a1b2c3d4-e5f6-7890-abcd-ef1234567890/drivers_license_xyz.jpg", true},
		{"a1b2c3d4-e5f6-7890-abcd-ef1234567890/registration_xyz.pdf", true},
		{"documents/abc/file.pdf", true},
		// Car documents (insurance/registration) live under the same /cars/
		// root as photos but in a `documents/` subfolder. They must be
		// private — the owner's insurance details are PII.
		{"cars/abc/documents/insurance.pdf", true},
		{"cars/abc/documents/registration_xyz.jpg", true},
	}
	for _, c := range cases {
		if got := IsPrivateUploadPath(c.path); got != c.private {
			t.Errorf("IsPrivateUploadPath(%q): got %v, want %v", c.path, got, c.private)
		}
	}
}

func TestIsSafeRelPath(t *testing.T) {
	safe := []string{
		"cars/abc/cover.jpg",
		"chats/abc/file.jpg",
		"user/profile_x.png",
	}
	for _, p := range safe {
		if !isSafeRelPath(p) {
			t.Errorf("isSafeRelPath(%q) = false, want true", p)
		}
	}

	unsafe := []string{
		"",
		"/leading-slash.jpg",
		"../escape.jpg",
		"chats/../../../etc/passwd",
		"foo/./bar.jpg",
		"with\x00null.jpg",
	}
	for _, p := range unsafe {
		if isSafeRelPath(p) {
			t.Errorf("isSafeRelPath(%q) = true, want false (traversal)", p)
		}
	}
}

// End-to-end: a fake upload root + the handler. Confirms public is served,
// private is 404 without sig, private succeeds with valid sig.
func TestFilesHandler_AccessControl(t *testing.T) {
	dir := t.TempDir()

	// Lay out: cars/<uuid>/cover.jpg (public) + chats/<uuid>/file.txt (private).
	if err := os.MkdirAll(filepath.Join(dir, "cars", "car1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cars", "car1", "cover.jpg"), []byte("PUBLIC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "chats", "chat1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chats", "chat1", "file.txt"), []byte("PRIVATE"), 0o644); err != nil {
		t.Fatal(err)
	}

	signer := urlsigner.New("secret")
	h := NewFilesHandler(dir, signer, true, slog.Default())

	// Helper to build a request and capture the response.
	do := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		h.Serve(rr, req)
		return rr
	}

	// Public path: served without auth.
	rr := do("/uploads/cars/car1/cover.jpg")
	if rr.Code != http.StatusOK {
		t.Fatalf("public file: got status %d, body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PUBLIC") {
		t.Errorf("public file body mismatch: %q", rr.Body.String())
	}

	// Private path WITHOUT signature: 404.
	rr = do("/uploads/chats/chat1/file.txt")
	if rr.Code != http.StatusNotFound {
		t.Errorf("unsigned private file should be 404, got %d", rr.Code)
	}

	// Private path WITH valid signature: 200.
	signed := signer.Sign("/uploads/chats/chat1/file.txt", 60*1_000_000_000) // 60s in nanoseconds
	rr = do(signed)
	if rr.Code != http.StatusOK {
		t.Fatalf("signed private file: got status %d, body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "PRIVATE") {
		t.Errorf("private file body mismatch: %q", rr.Body.String())
	}

	// Traversal attempt: 404.
	rr = do("/uploads/../etc/passwd")
	if rr.Code != http.StatusNotFound {
		t.Errorf("traversal should be 404, got %d", rr.Code)
	}
}

// When signer is nil and requireSig is true, private files MUST be denied
// (refuse-by-default rather than silently fall through to insecure serving).
func TestFilesHandler_NilSignerRejectsPrivate(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "chats", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chats", "c", "x.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewFilesHandler(dir, nil, true, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/uploads/chats/c/x.txt", nil)
	rr := httptest.NewRecorder()
	h.Serve(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("nil signer should reject private path, got %d", rr.Code)
	}
}
