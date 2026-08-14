package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// Admin driver-document replacement (client points 3+4): the sibling of
// ReplaceCarDocument for PERSONAL documents — "driver sent the corrected
// licence in support chat → admin sets it on their account", plus a direct
// admin upload for the same slot. Read admin_car_documents.go first; this
// file deliberately mirrors it, diverging only where the documents table
// forces it (see the replacement note below).

// userDocumentMimeExt mirrors the driver's own upload path
// (isValidDocumentMimeType): JPEG, PNG, PDF only — a document slot never
// accepts video, whatever the support chat allows. The extension comes
// from the mime type, never from the client-supplied filename.
var userDocumentMimeExt = map[string]string{
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/png":       ".png",
	"application/pdf": ".pdf",
}

// ReplaceUserDocument — POST /api/v1/admin/users/{id}/documents.
//
// Two source modes, same contract as the car version:
//   - multipart form (file + document_type): direct admin upload.
//   - JSON {support_attachment_id, document_type}: server-side COPY of a
//     file the user sent in their own support chat.
//
// The replacement resets status to 'uploaded' (pending review) — an
// approval attests to specific bytes, and the bytes just changed; admin
// can re-approve in one click.
func (h *AdminHandler) ReplaceUserDocument(w http.ResponseWriter, r *http.Request) {
	// Fail closed if wiring is incomplete — a nil deref here would panic
	// mid-request.
	if h.docRepo == nil || h.userRepo == nil || h.uploadDir == "" {
		h.logger.Error("admin replace user document: dependencies not wired")
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}

	var (
		docType  models.DocumentType
		srcBytes []byte
		srcName  string
		mimeType string
	)

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// ── Direct admin upload ─────────────────────────────────────────
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // driver path caps at 10 MB
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data (max 10 MB)"))
			return
		}
		docType = models.DocumentType(r.FormValue("document_type"))
		file, header, ferr := r.FormFile("file")
		if ferr != nil {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
			return
		}
		defer file.Close()
		mimeType = header.Header.Get("Content-Type")
		if mimeType == "" {
			buf := make([]byte, 512)
			file.Read(buf)
			mimeType = http.DetectContentType(buf)
			file.Seek(0, 0)
		}
		srcBytes, err = io.ReadAll(file)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		srcName = header.Filename
	} else {
		// ── Copy from a support-chat attachment ─────────────────────────
		if h.supportRepo == nil {
			h.logger.Error("admin replace user document: support repo not wired")
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		var body struct {
			SupportAttachmentID uuid.UUID `json:"support_attachment_id"`
			DocumentType        string    `json:"document_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid JSON"))
			return
		}
		docType = models.DocumentType(body.DocumentType)
		src, serr := h.supportRepo.GetAttachmentSource(r.Context(), body.SupportAttachmentID)
		if serr != nil {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "chat attachment not found"))
			return
		}
		// THE guard: an identity document may only come from the target
		// user's OWN support chat — without this an admin could graft one
		// person's licence onto another person's account.
		if src.ChatUserID != userID {
			httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("WRONG_SENDER", "that attachment wasn't sent by this user"))
			return
		}
		mimeType = src.MimeType
		srcBytes, err = os.ReadFile(src.FilePath)
		if err != nil {
			h.logger.Error("admin user doc: read chat attachment", "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		srcName = filepath.Base(src.FilePath)
	}

	if !docType.IsValid() {
		httputil.WriteError(w, http.StatusBadRequest, models.NewAPIError(
			"INVALID_DOCUMENT_TYPE",
			"Document type must be one of: drivers_license, registration, commercial_license, tlc_license, other",
		))
		return
	}
	ext, ok := userDocumentMimeExt[strings.ToLower(mimeType)]
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_FILE_TYPE", "File must be an image (JPEG, PNG) or PDF"))
		return
	}

	// Existence check AFTER input validation (validation branches stay
	// DB-free and testable): a typo'd-but-well-formed user id must 404
	// before any file lands on disk.
	target, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "user not found"))
		return
	}

	// Write path MUST follow {uploadDir}/{userID}/{name}{ext} — the
	// two-segment convention publicURLForDocument depends on; anything
	// deeper is written but unviewable.
	docID := uuid.New()
	userDir := filepath.Join(h.uploadDir, userID.String())
	if err := os.MkdirAll(userDir, 0755); err != nil {
		h.logger.Error("admin user doc: mkdir", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	newFileName := fmt.Sprintf("%s_admin_%s%s", docType, docID.String()[:8], ext)
	destPath := filepath.Join(userDir, newFileName)
	if err := os.WriteFile(destPath, srcBytes, 0644); err != nil {
		h.logger.Error("admin user doc: write", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Replacement differs from the car version on purpose: documents has a
	// UNIQUE (user_id, type) index, so insert-first is impossible — and a
	// non-transactional delete-then-insert would destroy the existing row
	// if anything failed between the two statements (flipping
	// HasRequiredDocuments and ejecting the driver). The swap is one DB
	// transaction; only the disk cleanup happens after, best-effort — an
	// orphaned blob is far better than a row pointing at deleted bytes.
	now := time.Now().UTC()
	doc := &models.Document{
		ID:       docID,
		UserID:   userID,
		Type:     docType,
		FileName: srcName,
		FilePath: destPath,
		FileSize: int64(len(srcBytes)),
		MimeType: mimeType,
		// Pending review, never pre-approved: an approval attests to
		// specific bytes, and this replacement just changed them.
		Status:    models.DocumentStatusUploaded,
		CreatedAt: now,
		UpdatedAt: now,
	}
	oldFilePath, err := h.docRepo.ReplaceForUserAndType(r.Context(), doc)
	if err != nil {
		os.Remove(destPath)
		h.logger.Error("admin user doc: replace", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if oldFilePath != "" && oldFilePath != destPath {
		os.Remove(oldFilePath)
	}

	// An admin write to someone's identity documents must never be silent.
	if h.notifHandler != nil {
		go h.notifHandler.Notify(target.ID, models.NotificationTypeSystem,
			"Document updated",
			fmt.Sprintf("DriveBai support updated your %s. It's pending review.", docType.DisplayName()),
			nil, nil)
	}

	// Signed URL so the console can swap the card without a refetch.
	httputil.WriteJSON(w, http.StatusCreated, AdminReplacedUserDocument{
		ID:        doc.ID,
		Type:      doc.Type,
		FileName:  doc.FileName,
		FileURL:   h.urlSigner.Sign(publicURLForDocument(userID, destPath)),
		MimeType:  doc.MimeType,
		Status:    doc.Status,
		CreatedAt: doc.CreatedAt,
		UpdatedAt: doc.UpdatedAt,
	})
}

// AdminReplacedUserDocument is the wire shape for a just-replaced document
// — key-compatible with AdminUserDocument so the console swaps the card in
// place.
type AdminReplacedUserDocument struct {
	ID        uuid.UUID             `json:"id"`
	Type      models.DocumentType   `json:"type"`
	FileName  string                `json:"file_name"`
	FileURL   string                `json:"file_url"`
	MimeType  string                `json:"mime_type,omitempty"`
	Status    models.DocumentStatus `json:"status"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
}
