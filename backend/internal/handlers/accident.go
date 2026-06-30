package handlers

import (
	"encoding/json"
	"fmt"
	"io"
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
	chatRepo     *repository.ChatRepository
	wsHub        *ws.Hub
	uploadDir    string
	urlSigner    *PrivateURLSigner
	notifHandler *NotificationHandler
	logger       *slog.Logger
}

func NewAccidentHandler(
	accidentRepo *repository.AccidentRepository,
	adminRepo *repository.AdminRepository,
	wsHub *ws.Hub,
	uploadDir string,
	urlSigner *PrivateURLSigner,
	logger *slog.Logger,
) *AccidentHandler {
	return &AccidentHandler{
		accidentRepo: accidentRepo,
		adminRepo:    adminRepo,
		wsHub:        wsHub,
		uploadDir:    uploadDir,
		urlSigner:    urlSigner,
		logger:       logger,
	}
}

// SetNotificationHandler / SetChatRepository wire the optional dependencies
// for cross-party push notifications on accident submit. Setters rather than
// ctor args so the existing call sites don't need to change.
func (h *AccidentHandler) SetNotificationHandler(n *NotificationHandler) { h.notifHandler = n }
func (h *AccidentHandler) SetChatRepository(c *repository.ChatRepository) { h.chatRepo = c }

// Create — POST /accidents
// Idempotent: returns the existing draft (200) if one already exists for the same user/chat/car,
// otherwise creates a new draft and returns it (201).
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

	// Return existing draft rather than creating a duplicate.
	if existing, err := h.accidentRepo.GetDraftForUser(r.Context(), userID, chatID, carID); err == nil {
		if atts, _ := h.accidentRepo.ListAttachments(r.Context(), existing.ID); atts != nil {
			existing.Attachments = atts
		}
		h.signURLs(existing)
		httputil.WriteJSON(w, http.StatusOK, existing)
		return
	}

	accident, err := h.accidentRepo.Create(r.Context(), userID, chatID, carID)
	if err != nil {
		h.logger.Error("create accident", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	h.signURLs(accident)
	httputil.WriteJSON(w, http.StatusCreated, accident)
}

// GetDraft — GET /accidents/draft?chat_id=&car_id=
// Returns the most recent draft for the authenticated user, optionally scoped to a chat/car.
// Returns 404 when no draft exists.
func (h *AccidentHandler) GetDraft(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var chatID, carID *uuid.UUID
	if v := r.URL.Query().Get("chat_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			chatID = &id
		}
	}
	if v := r.URL.Query().Get("car_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			carID = &id
		}
	}

	accident, err := h.accidentRepo.GetDraftForUser(r.Context(), userID, chatID, carID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "no draft found"))
		return
	}

	if atts, _ := h.accidentRepo.ListAttachments(r.Context(), accident.ID); atts != nil {
		accident.Attachments = atts
	}
	h.signURLs(accident)
	httputil.WriteJSON(w, http.StatusOK, accident)
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
	for i := range accidents {
		h.signURLs(&accidents[i])
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

	h.signURLs(accident)
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
	if attachments, e := h.accidentRepo.ListAttachments(r.Context(), accidentID); e == nil {
		accident.Attachments = attachments
	}
	h.signURLs(accident)
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
	data, err := io.ReadAll(file)
	if err != nil {
		h.logger.Error("read accident file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		h.logger.Error("write accident file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	fileURL := fmt.Sprintf("/uploads/accidents/%s/%s", accidentID.String(), filename)

	att, err := h.accidentRepo.AddAttachment(r.Context(), accidentID, slot, fileURL, filePath, int64(len(data)), contentType)
	if err != nil {
		os.Remove(filePath)
		h.logger.Error("save attachment record", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Sign the URL we hand back so the iOS client can fetch the file from
	// the protected /uploads endpoint. The DB row keeps the raw path.
	att.FileURL = h.urlSigner.Sign(att.FileURL)
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

	data, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
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

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"signature_url": h.urlSigner.Sign(fileURL)})
}

// signURLs walks an accident response and signs every private file URL in
// place. The DB rows keep raw `/uploads/...` paths; only the on-the-wire
// JSON gets `?sig=&exp=` appended. Callers should hit this immediately
// before WriteJSON.
func (h *AccidentHandler) signURLs(a *models.Accident) {
	if a == nil {
		return
	}
	a.SignatureURL = h.urlSigner.Sign(a.SignatureURL)
	for i := range a.Attachments {
		a.Attachments[i].FileURL = h.urlSigner.Sign(a.Attachments[i].FileURL)
	}
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
	if attachments, e := h.accidentRepo.ListAttachments(r.Context(), accidentID); e == nil {
		accident.Attachments = attachments
	}
	h.signURLs(accident)

	// Broadcast to all admins so the panel updates in real-time
	adminIDs, _ := h.adminRepo.GetAdminUserIDs(r.Context())
	if len(adminIDs) > 0 {
		h.wsHub.Broadcast(&ws.Event{
			Type:          "accident_submitted",
			TargetUserIDs: adminIDs,
			Payload:       map[string]any{"accident_id": accident.ID, "reporter_id": userID},
		})
	}

	// Notify the other party (owner if reporter is driver, and vice-versa)
	// when the accident is linked to a chat — without this, the counterparty
	// can find out about the incident only by re-opening chat. The reporter
	// themself is skipped because they're the actor.
	if h.notifHandler != nil && h.chatRepo != nil && accident.RelatedChatID != nil {
		if chat, cerr := h.chatRepo.GetChatByID(r.Context(), *accident.RelatedChatID); cerr == nil && chat != nil {
			otherUserID := chat.OwnerID
			if userID == chat.OwnerID {
				otherUserID = chat.DriverID
			}
			if otherUserID != uuid.Nil && otherUserID != userID {
				go h.notifHandler.Notify(otherUserID, models.NotificationTypeSystem,
					"Accident reported",
					"The other party reported an accident for your rental. Our support team will be in touch shortly.",
					accident.RelatedChatID, nil)
			}
		}
	}

	httputil.WriteJSON(w, http.StatusOK, accident)
}
