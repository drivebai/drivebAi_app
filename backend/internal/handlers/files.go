package handlers

import (
	"log/slog"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/drivebai/backend/internal/urlsigner"
)

// FilesHandler serves uploaded files from disk under `/uploads/*`.
//
// Two access modes coexist on the same root:
//
//   - PUBLIC paths (car photos, profile photos) are served unsigned. They
//     appear unauthenticated in /api/v1/listings and other public surfaces,
//     so requiring signatures would just break those screens for no privacy
//     gain — the bytes are already advertised.
//
//   - PRIVATE paths (chat attachments, driver documents, accident files,
//     handwritten signatures, …) are served ONLY if the request carries a
//     valid `?sig=…&exp=…` query string. The signature is produced by the
//     API handler that knew the caller was authorized to learn the URL
//     (e.g. ListAttachments after IsParticipant). Sharing the URL leaks
//     access only until `exp` elapses — typically minutes — after which
//     the link is dead.
//
// Path traversal is rejected unconditionally (anything containing "..",
// absolute paths, NUL bytes, or a cleaned result that escapes the upload
// root). The handler does NOT depend on the host filesystem — it never
// touches a path Stat() rejects with a permission/traversal error.
type FilesHandler struct {
	uploadDir string
	signer    *urlsigner.Signer
	logger    *slog.Logger
	// requireSigForPrivate controls whether private paths are served only
	// when a valid signature is presented. Production sets this to true; a
	// test/dev override leaves it off so existing flows keep working
	// while we migrate URL emission.
	requireSigForPrivate bool
}

// NewFilesHandler returns a handler bound to `uploadDir`. When `signer` is
// nil OR `requireSigForPrivate` is false, private paths fall through to
// the same unsigned serving the legacy code used — useful for dev. In
// production both must be set.
func NewFilesHandler(uploadDir string, signer *urlsigner.Signer, requireSigForPrivate bool, logger *slog.Logger) *FilesHandler {
	return &FilesHandler{
		uploadDir:            uploadDir,
		signer:               signer,
		logger:               logger,
		requireSigForPrivate: requireSigForPrivate,
	}
}

// Serve is the chi handler for `/uploads/*`. We strip the leading
// `/uploads/` here (chi doesn't do it by default for `r.Get("/uploads/*", …)`)
// so the function is also drop-in usable from raw httptest without an
// http.StripPrefix wrapper.
func (h *FilesHandler) Serve(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/")
	rel = strings.TrimPrefix(rel, "uploads/")
	if !isSafeRelPath(rel) {
		// Path is malformed (traversal attempt, NUL byte, absolute, etc.).
		// 404 to avoid leaking which paths exist.
		http.NotFound(w, r)
		return
	}

	if IsPrivateUploadPath(rel) && h.requireSigForPrivate {
		if h.signer == nil {
			h.logger.Error("files: private path requested but signer is nil; rejecting", "path", rel)
			http.NotFound(w, r)
			return
		}
		// Signatures are computed over "/uploads/<rel>", matching what the
		// API handler emitted to the client.
		if err := h.signer.VerifyFromQuery("/uploads/"+rel, r.URL.Query()); err != nil {
			http.NotFound(w, r)
			return
		}
	}

	// Resolve against uploadDir and verify we didn't escape (defence in
	// depth — isSafeRelPath should already have blocked this).
	fullPath := filepath.Join(h.uploadDir, filepath.FromSlash(rel))
	if !strings.HasPrefix(fullPath, filepath.Clean(h.uploadDir)+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, fullPath)
}

// PrivateURLSigner bundles a Signer and the TTL so handlers carry one
// dependency instead of two. Pass via constructor; call Sign per response.
// A zero value (nil signer) is safe — Sign degrades to a passthrough.
type PrivateURLSigner struct {
	Signer *urlsigner.Signer
	TTL    time.Duration
}

// Sign delegates to the package-level SignPrivateURL.
func (p *PrivateURLSigner) Sign(rel string) string {
	if p == nil {
		return rel
	}
	return SignPrivateURL(p.Signer, p.TTL, rel)
}

// SignPrivateURL is the helper API-response builders should call on every
// `file_url` they're about to emit. Behaviour:
//
//   - empty / non-/uploads path → returned as-is (already absolute URL,
//     missing data, etc.).
//   - PUBLIC /uploads path (car photo, profile photo) → returned as-is so
//     the iOS image cache hit rate stays high.
//   - PRIVATE /uploads path with a non-nil signer → returned with
//     `?sig=…&exp=…` appended. The FilesHandler will refuse the file
//     after the TTL elapses unless the client re-fetches the parent JSON.
//   - PRIVATE /uploads path with a nil signer → returned as-is. This is
//     a no-op for dev (where REQUIRE_PRIVATE_UPLOAD_SIGNATURES is off);
//     in production the FilesHandler will still 404 because it requires
//     a signature even when one isn't present.
//
// Always call this from the handler before writing the response — DO NOT
// store signed URLs in the database.
func SignPrivateURL(signer *urlsigner.Signer, ttl time.Duration, rel string) string {
	if rel == "" || signer == nil {
		return rel
	}
	if !strings.HasPrefix(rel, "/uploads/") {
		return rel
	}
	relWithoutPrefix := strings.TrimPrefix(rel, "/uploads/")
	if !IsPrivateUploadPath(relWithoutPrefix) {
		return rel
	}
	return signer.Sign(rel, ttl)
}

// IsPrivateUploadPath reports whether a relative path under /uploads/ must
// be served only with a valid signature. The rule is:
//
//   - `cars/<carID>/documents/...`  → PRIVATE (insurance, registration)
//   - `cars/<carID>/...other...`    → public (car photos shown in Discovery)
//   - `<userID>/profile_*`          → public (profile photos shown next to
//     chat messages, today cards, etc.)
//   - everything else               → private
//
// The user-folder split is awkward — driver licenses and profile photos
// live under the same `/uploads/<userID>/` prefix today, distinguished only
// by filename. We key off the filename prefix instead of moving files on
// disk so the migration stays a code-only change. Same trick for car docs:
// they share the `cars/` root with photos but live in a dedicated
// `documents/` subfolder, so we recognize them by that.
func IsPrivateUploadPath(rel string) bool {
	first := strings.SplitN(rel, "/", 2)[0]
	switch first {
	case "cars":
		// PRIVATE if the third segment is "documents" (insurance/registration);
		// otherwise PUBLIC (photos visible in Discovery).
		parts := strings.SplitN(rel, "/", 4)
		if len(parts) >= 3 && parts[2] == "documents" {
			return true
		}
		return false
	case "":
		return true
	}
	// Looks like /<userId>/<filename>: public if filename starts with "profile_".
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) == 2 && strings.HasPrefix(parts[1], "profile_") {
		return false
	}
	return true
}

// isSafeRelPath rejects anything that would let the caller escape the
// upload root or feed an unusual path into http.ServeFile. The cheap
// `path.Clean` round-trip catches `..` and `.` collapses; we additionally
// reject NUL bytes and absolute paths defensively.
func isSafeRelPath(rel string) bool {
	if rel == "" {
		return false
	}
	if strings.Contains(rel, "\x00") {
		return false
	}
	if strings.HasPrefix(rel, "/") {
		return false
	}
	cleaned := path.Clean(rel)
	if cleaned != rel {
		return false
	}
	if strings.HasPrefix(cleaned, "../") || cleaned == ".." || strings.Contains(cleaned, "/../") {
		return false
	}
	return true
}
