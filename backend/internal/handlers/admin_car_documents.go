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
	"github.com/drivebai/backend/internal/repository"
)

// Admin car-document replacement (batch item 1): "owner sent the correct
// document in support chat → admin sets it on the car for them". Also
// accepts a direct admin upload for the same slot.

// SetCarDocumentDependencies wires the stores the replacement endpoint
// needs. Setter for the usual test-compat reason.
func (h *AdminHandler) SetCarDocumentDependencies(carRepo *repository.CarRepository, carDocRepo *repository.CarDocumentRepository, uploadDir string) {
	h.carRepo = carRepo
	h.carDocRepo = carDocRepo
	h.uploadDir = uploadDir
}

// carDocumentMimeTypes mirrors the OWNER upload whitelist exactly (a
// document slot never accepts video, whatever the chat allows).
var carDocumentMimeTypes = map[string]string{
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/png":       ".png",
	"application/pdf": ".pdf",
}

// ReplaceCarDocument — POST /api/v1/admin/cars/{id}/documents.
//
// Two source modes:
//   - multipart form (file + document_type): the admin uploads the corrected
//     file directly.
//   - JSON {support_attachment_id, document_type}: server-side COPY of a file
//     the owner sent in their support chat. The attachment must come from the
//     car owner's own chat — an admin cannot graft another user's file onto
//     a car.
//
// Either way it is a true REPLACE: after the new row lands, every older row
// of the same document_type (and its file) is removed — unlike the owner
// path, which historically accumulates duplicates. Files are COPIED into the
// car's documents directory; the chat attachment keeps its own file, so chat
// history stays intact and the two ACL domains never share bytes on disk.
func (h *AdminHandler) ReplaceCarDocument(w http.ResponseWriter, r *http.Request) {
	if h.carRepo == nil || h.carDocRepo == nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	carID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid car id"))
		return
	}
	car, err := h.carRepo.GetByID(r.Context(), carID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "car not found"))
		return
	}

	var (
		docType  models.CarDocumentType
		srcBytes []byte
		srcName  string
		mimeType string
	)

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// ── Direct admin upload ─────────────────────────────────────────
		r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data (max 25 MB)"))
			return
		}
		docType = models.CarDocumentType(r.FormValue("document_type"))
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
		docType = models.CarDocumentType(body.DocumentType)
		src, serr := h.supportRepo.GetAttachmentSource(r.Context(), body.SupportAttachmentID)
		if serr != nil {
			httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "chat attachment not found"))
			return
		}
		// The document must come from the CAR OWNER's own support chat.
		if src.ChatUserID != car.OwnerID {
			httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("WRONG_SENDER", "that attachment wasn't sent by this car's owner"))
			return
		}
		mimeType = src.MimeType
		srcBytes, err = os.ReadFile(src.FilePath)
		if err != nil {
			h.logger.Error("admin car doc: read chat attachment", "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		srcName = filepath.Base(src.FilePath)
	}

	if !validCarDocumentTypes[docType] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("document_type must be one of: inspection, registration, permit, insurance, title"))
		return
	}
	ext, ok := carDocumentMimeTypes[mimeType]
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Only JPEG, PNG, and PDF files can become vehicle documents"))
		return
	}

	// Write the new file into the car's own documents directory (never
	// point a car document at a chat file — separate ACLs, separate
	// lifecycles).
	docID := uuid.New()
	dir := filepath.Join(h.uploadDir, "cars", carID.String(), "documents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.logger.Error("admin car doc: mkdir", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	filename := fmt.Sprintf("%s_admin_%s%s", docType, docID.String()[:8], ext)
	destPath := filepath.Join(dir, filename)
	if err := os.WriteFile(destPath, srcBytes, 0644); err != nil {
		h.logger.Error("admin car doc: write", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Existing rows of this type — captured BEFORE the insert so the new
	// row can't be swept up in the replacement.
	existing, err := h.carDocRepo.GetByCarID(r.Context(), carID)
	if err != nil {
		os.Remove(destPath)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	now := time.Now().UTC()
	doc := &models.CarDocument{
		ID:           docID,
		CarID:        carID,
		DocumentType: docType,
		FileName:     srcName,
		FilePath:     destPath,
		FileURL:      fmt.Sprintf("/uploads/cars/%s/documents/%s", carID.String(), filename),
		FileSize:     len(srcBytes),
		MimeType:     mimeType,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.carDocRepo.Create(r.Context(), doc); err != nil {
		os.Remove(destPath)
		h.logger.Error("admin car doc: create row", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// True replace: remove superseded rows + files (best-effort — the new
	// document is already in place either way).
	for _, old := range existing {
		if old.DocumentType != docType {
			continue
		}
		if err := h.carDocRepo.Delete(r.Context(), old.ID); err != nil {
			h.logger.Error("admin car doc: delete old row", "error", err, "doc_id", old.ID)
			continue
		}
		if old.FilePath != "" {
			os.Remove(old.FilePath)
		}
	}

	// Tell the owner their paperwork changed — an admin write to their car
	// must never be silent.
	if h.notifHandler != nil {
		go h.notifHandler.Notify(car.OwnerID, models.NotificationTypeSystem,
			"Vehicle document updated",
			fmt.Sprintf("DriveBai support updated the %s document on your %d %s %s.", docType, car.Year, car.Make, car.Model),
			nil, nil)
	}

	signed := *doc
	signed.FileURL = h.urlSigner.Sign(signed.FileURL)
	httputil.WriteJSON(w, http.StatusCreated, signed)
}
