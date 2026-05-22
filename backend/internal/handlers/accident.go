package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/drivebai/backend/internal/ws"
)

// AccidentHandler serves user-facing accident report endpoints.
// All routes require AuthMiddleware.
type AccidentHandler struct {
	accidentRepo *repository.AccidentRepository
	adminRepo    *repository.AdminRepository
	wsHub        *ws.Hub
	uploadDir    string
	logger       *slog.Logger
}

func NewAccidentHandler(
	accidentRepo *repository.AccidentRepository,
	adminRepo *repository.AdminRepository,
	wsHub *ws.Hub,
	uploadDir string,
	logger *slog.Logger,
) *AccidentHandler {
	return &AccidentHandler{
		accidentRepo: accidentRepo,
		adminRepo:    adminRepo,
		wsHub:        wsHub,
		uploadDir:    uploadDir,
		logger:       logger,
	}
}

// Create — POST /accidents
// Creates a new draft accident report.
func (h *AccidentHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var body struct {
		RelatedChatID *string `json:"related_chat_id"`
		RelatedCarID  *string `json:"related_car_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	var chatID, carID *uuid.UUID
	if body.RelatedChatID != nil {
		if id, err := uuid.Parse(*body.RelatedChatID); err == nil {
			chatID = &id
		}
	}
	if body.RelatedCarID != nil {
		if id, err := uuid.Parse(*body.RelatedCarID); err == nil {
			carID = &id
		}
	}

	accident, err := h.accidentRepo.Create(r.Context(), userID, chatID, carID)
	if err != nil {
		h.logger.Error("create accident", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, accident)
}

// List — GET /accidents
// Returns all accident reports for the authenticated user.
func (h *AccidentHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidents, err := h.accidentRepo.ListForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("list accidents", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if accidents == nil {
		accidents = []models.Accident{}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"accidents": accidents})
}

// Get — GET /accidents/{id}
func (h *AccidentHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid accident id"))
		return
	}

	accident, err := h.accidentRepo.GetByIDForUser(r.Context(), accidentID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "accident not found"))
		return
	}

	attachments, _ := h.accidentRepo.ListAttachments(r.Context(), accidentID)
	if attachments != nil {
		accident.Attachments = attachments
	}

	httputil.WriteJSON(w, http.StatusOK, accident)
}

// Patch — PATCH /accidents/{id}
// Accepts partial step data; only provided fields are written.
func (h *AccidentHandler) Patch(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid accident id"))
		return
	}

	// Decode into a raw map first so we know which keys were provided.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid JSON"))
		return
	}

	patch := repository.AccidentPatch{}

	if v, ok := raw["driver1_info"]; ok {
		patch.Driver1Info = new(models.DriverInfo)
		json.Unmarshal(v, patch.Driver1Info)
	}
	if v, ok := raw["driver2_info"]; ok {
		patch.Driver2Info = new(models.DriverInfo)
		json.Unmarshal(v, patch.Driver2Info)
	}
	if v, ok := raw["vehicle_damage"]; ok {
		patch.VehicleDamage = new(models.VehicleDamage)
		json.Unmarshal(v, patch.VehicleDamage)
	}
	if v, ok := raw["accident_description"]; ok {
		var s string
		json.Unmarshal(v, &s)
		patch.AccidentDescription = &s
	}
	if v, ok := raw["insurance_info"]; ok {
		patch.InsuranceInfo = new(models.InsuranceInfo)
		json.Unmarshal(v, patch.InsuranceInfo)
	}
	if v, ok := raw["other_info"]; ok {
		patch.OtherInfo = new(models.OtherInfo)
		json.Unmarshal(v, patch.OtherInfo)
	}

	accident, err := h.accidentRepo.Update(r.Context(), accidentID, userID, patch)
	if err != nil {
		h.logger.Error("patch accident", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, accident)
}

// Upload — POST /accidents/{id}/attachments
// Multipart upload: form fields "slot" + "file".
// Allowed slots: accident_photo, accident_video, driver1_license, driver2_plate, second_vehicle_docs
func (h *AccidentHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid accident id"))
		return
	}

	// Ownership check
	if _, err := h.accidentRepo.GetByIDForUser(r.Context(), accidentID, userID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "accident not found"))
		return
	}

	// 50 MB limit for video
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data"))
		return
	}

	slotStr := r.FormValue("slot")
	validSlots := map[models.AttachmentSlot]bool{
		models.SlotAccidentPhoto:     true,
		models.SlotAccidentVideo:     true,
		models.SlotDriver1License:    true,
		models.SlotDriver2Plate:      true,
		models.SlotSecondVehicleDocs: true,
	}
	slot := models.AttachmentSlot(slotStr)
	if !validSlots[slot] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid slot; allowed: accident_photo, accident_video, driver1_license, driver2_plate, second_vehicle_docs"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	// Detect content-type
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		buf := make([]byte, 512)
		file.Read(buf)
		contentType = http.DetectContentType(buf)
		file.Seek(0, 0)
	}

	validTypes := map[string]string{
		"image/jpeg":  ".jpg",
		"image/jpg":   ".jpg",
		"image/png":   ".png",
		"image/heic":  ".heic",
		"image/heif":  ".heif",
		"video/mp4":   ".mp4",
		"video/quicktime": ".mov",
		"application/pdf": ".pdf",
	}
	ext, valid := validTypes[contentType]
	if !valid {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("unsupported file type: "+contentType))
		return
	}

	// Build storage path: /uploads/accidents/{accidentID}/{slot}_{uuid}{ext}
	dir := filepath.Join(h.uploadDir, "accidents", accidentID.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.logger.Error("mkdir accidents", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	fileID := uuid.New().String()
	filename := fmt.Sprintf("%s_%s%s", strings.ReplaceAll(slotStr, "_", "-"), fileID, ext)
	filePath := filepath.Join(dir, filename)

	// Write to disk
	data := make([]byte, header.Size)
	file.Read(data)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		h.logger.Error("write accident file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	fileURL := fmt.Sprintf("/uploads/accidents/%s/%s", accidentID.String(), filename)

	att, err := h.accidentRepo.AddAttachment(r.Context(), accidentID, slot, fileURL, filePath, header.Size, contentType)
	if err != nil {
		os.Remove(filePath)
		h.logger.Error("save attachment record", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, att)
}

// DeleteAttachment — DELETE /accidents/{id}/attachments/{attachId}
func (h *AccidentHandler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid accident id"))
		return
	}
	attachID, err := uuid.Parse(chi.URLParam(r, "attachId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid attachment id"))
		return
	}

	if _, err := h.accidentRepo.GetByIDForUser(r.Context(), accidentID, userID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "accident not found"))
		return
	}

	filePath, err := h.accidentRepo.DeleteAttachment(r.Context(), attachID, accidentID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "attachment not found"))
		return
	}
	os.Remove(filePath)

	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Sign — POST /accidents/{id}/sign
// Multipart upload of the signature PNG image.
func (h *AccidentHandler) Sign(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid accident id"))
		return
	}

	if _, err := h.accidentRepo.GetByIDForUser(r.Context(), accidentID, userID); err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "accident not found"))
		return
	}

	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()
	_ = header

	dir := filepath.Join(h.uploadDir, "accidents", accidentID.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	filename := fmt.Sprintf("signature_%s.png", uuid.New().String())
	filePath := filepath.Join(dir, filename)

	data := make([]byte, header.Size)
	file.Read(data)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	fileURL := fmt.Sprintf("/uploads/accidents/%s/%s", accidentID.String(), filename)

	if err := h.accidentRepo.SetSignature(r.Context(), accidentID, userID, fileURL); err != nil {
		os.Remove(filePath)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"signature_url": fileURL})
}

// Submit — POST /accidents/{id}/submit
// Transitions the accident from draft → submitted and notifies admins via WS.
func (h *AccidentHandler) Submit(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	accidentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid accident id"))
		return
	}

	accident, err := h.accidentRepo.Submit(r.Context(), accidentID, userID)
	if err != nil {
		h.logger.Error("submit accident", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Broadcast to all admins so the panel updates in real-time
	adminIDs, _ := h.adminRepo.GetAdminUserIDs(r.Context())
	if len(adminIDs) > 0 {
		h.wsHub.Broadcast(&ws.Event{
			Type:          "accident_submitted",
			TargetUserIDs: adminIDs,
			Payload:       map[string]any{"accident_id": accident.ID, "reporter_id": userID},
		})
	}

	httputil.WriteJSON(w, http.StatusOK, accident)
}
