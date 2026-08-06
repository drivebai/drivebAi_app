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

// TicketHandler serves BOTH the user-facing and admin support-ticket endpoints
// (admin routes are gated by RequireRole at registration). One handler so the
// URL-signing path is shared — the accident module split admin into a separate
// handler and forgot to sign, 404-ing every admin attachment in production.
type TicketHandler struct {
	ticketRepo   *repository.TicketRepository
	adminRepo    *repository.AdminRepository
	wsHub        *ws.Hub
	uploadDir    string
	urlSigner    *PrivateURLSigner
	notifHandler *NotificationHandler
	logger       *slog.Logger
}

func NewTicketHandler(
	ticketRepo *repository.TicketRepository,
	adminRepo *repository.AdminRepository,
	wsHub *ws.Hub,
	uploadDir string,
	urlSigner *PrivateURLSigner,
	logger *slog.Logger,
) *TicketHandler {
	return &TicketHandler{
		ticketRepo: ticketRepo,
		adminRepo:  adminRepo,
		wsHub:      wsHub,
		uploadDir:  uploadDir,
		urlSigner:  urlSigner,
		logger:     logger,
	}
}

// SetNotificationHandler wires push/notification delivery (setter to avoid
// touching the constructor call site).
func (h *TicketHandler) SetNotificationHandler(n *NotificationHandler) { h.notifHandler = n }

// signTicket signs every attachment URL on the way out. DB keeps raw paths.
func (h *TicketHandler) signTicket(t *models.SupportTicket) {
	if t == nil {
		return
	}
	for i := range t.Attachments {
		t.Attachments[i].FileURL = h.urlSigner.Sign(t.Attachments[i].FileURL)
	}
}

// --- User surface ---

// Create — POST /tickets. Idempotent: returns the user's existing draft (200)
// or a new draft (201).
func (h *TicketHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	if existing, err := h.ticketRepo.GetDraftForUser(r.Context(), userID); err == nil {
		existing.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), existing.ID)
		h.signTicket(existing)
		httputil.WriteJSON(w, http.StatusOK, existing)
		return
	}
	ticket, err := h.ticketRepo.Create(r.Context(), userID)
	if err != nil {
		h.logger.Error("create ticket", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, ticket)
}

// GetDraft — GET /tickets/draft. Resume the user's open draft (404 when none).
func (h *TicketHandler) GetDraft(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticket, err := h.ticketRepo.GetDraftForUser(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "no draft found"))
		return
	}
	ticket.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), ticket.ID)
	h.signTicket(ticket)
	httputil.WriteJSON(w, http.StatusOK, ticket)
}

// List — GET /tickets. The user's "My requests" list.
func (h *TicketHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	tickets, err := h.ticketRepo.ListForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("list tickets", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Lightweight list: no attachments hydration (the detail endpoint does that).
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"tickets": tickets})
}

// Get — GET /tickets/{id}.
func (h *TicketHandler) Get(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid ticket id"))
		return
	}
	ticket, err := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "ticket not found"))
		return
	}
	ticket.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), ticketID)
	h.signTicket(ticket)
	httputil.WriteJSON(w, http.StatusOK, ticket)
}

// Patch — PATCH /tickets/{id}. Partial draft edit (category/subject/description/
// last_step). Returns 409 on a non-draft ticket (accidents silently no-op'd).
func (h *TicketHandler) Patch(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid ticket id"))
		return
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid JSON"))
		return
	}
	patch := repository.TicketPatch{}
	if v, ok := raw["category"]; ok {
		var s string
		json.Unmarshal(v, &s)
		if s != "" && !models.ValidTicketCategories[models.TicketCategory(s)] {
			httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid category"))
			return
		}
		patch.Category = &s
	}
	if v, ok := raw["subject"]; ok {
		var s string
		json.Unmarshal(v, &s)
		patch.Subject = &s
	}
	if v, ok := raw["description"]; ok {
		var s string
		json.Unmarshal(v, &s)
		patch.Description = &s
	}
	if v, ok := raw["last_step"]; ok {
		var n int
		json.Unmarshal(v, &n)
		patch.LastStep = &n
	}

	found, err := h.ticketRepo.Update(r.Context(), ticketID, userID, patch)
	if err != nil {
		h.logger.Error("patch ticket", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !found {
		// Not the user's ticket, or already submitted — a submitted ticket is
		// immutable. 409, not a silent 200.
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("TICKET_NOT_EDITABLE", "this request can no longer be edited"))
		return
	}
	ticket, err := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "ticket not found"))
		return
	}
	ticket.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), ticketID)
	h.signTicket(ticket)
	httputil.WriteJSON(w, http.StatusOK, ticket)
}

// Upload — POST /tickets/{id}/attachments. Multipart evidence upload against a
// draft. Stored private under /uploads/tickets/{id}/.
func (h *TicketHandler) Upload(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid ticket id"))
		return
	}
	ticket, err := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "ticket not found"))
		return
	}
	if ticket.Status != models.TicketStatusDraft {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("TICKET_NOT_EDITABLE", "evidence can only be added while the request is a draft"))
		return
	}

	if err := r.ParseMultipartForm(25 << 20); err != nil { // 25 MB
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("failed to parse form data"))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("file is required"))
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		buf := make([]byte, 512)
		file.Read(buf)
		contentType = http.DetectContentType(buf)
		file.Seek(0, 0)
	}
	validTypes := map[string]string{
		"image/jpeg":      ".jpg",
		"image/jpg":       ".jpg",
		"image/png":       ".png",
		"image/heic":      ".heic",
		"image/heif":      ".heif",
		"application/pdf": ".pdf",
	}
	ext, valid := validTypes[contentType]
	if !valid {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("unsupported file type: "+contentType))
		return
	}

	dir := filepath.Join(h.uploadDir, "tickets", ticketID.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		h.logger.Error("mkdir tickets", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	filename := fmt.Sprintf("evidence_%s%s", uuid.New().String(), ext)
	filePath := filepath.Join(dir, filename)
	data, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		h.logger.Error("write ticket file", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	fileURL := fmt.Sprintf("/uploads/tickets/%s/%s", ticketID.String(), filename)
	att, err := h.ticketRepo.AddAttachment(r.Context(), ticketID, fileURL, filePath, int64(len(data)), contentType)
	if err != nil {
		os.Remove(filePath)
		h.logger.Error("save ticket attachment", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	att.FileURL = h.urlSigner.Sign(att.FileURL)
	httputil.WriteJSON(w, http.StatusCreated, att)
}

// DeleteAttachment — DELETE /tickets/{id}/attachments/{attachId}.
func (h *TicketHandler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid ticket id"))
		return
	}
	attachID, err := uuid.Parse(chi.URLParam(r, "attachId"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid attachment id"))
		return
	}
	ticket, err := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "ticket not found"))
		return
	}
	if ticket.Status != models.TicketStatusDraft {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("TICKET_NOT_EDITABLE", "evidence can only be changed while the request is a draft"))
		return
	}
	filePath, err := h.ticketRepo.DeleteAttachment(r.Context(), attachID, ticketID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "attachment not found"))
		return
	}
	os.Remove(filePath)
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Submit — POST /tickets/{id}/submit. draft -> open, with REQUIRED-FIELD
// validation the accident flow lacked: category valid + description non-empty.
func (h *TicketHandler) Submit(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid ticket id"))
		return
	}
	ticket, err := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "ticket not found"))
		return
	}
	if ticket.Status != models.TicketStatusDraft {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_SUBMITTED", "this request has already been submitted"))
		return
	}
	if !models.ValidTicketCategories[ticket.Category] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("please choose a category"))
		return
	}
	if strings.TrimSpace(ticket.Description) == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("please describe your problem"))
		return
	}

	found, err := h.ticketRepo.Submit(r.Context(), ticketID, userID)
	if err != nil {
		h.logger.Error("submit ticket", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !found {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_SUBMITTED", "this request has already been submitted"))
		return
	}

	fresh, _ := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if fresh != nil {
		fresh.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), ticketID)
		h.signTicket(fresh)
	}

	h.fanOutToAdmins(r, ticketID, userID, "New support request",
		fmt.Sprintf("%s — %s", categoryLabel(ticket.Category), snippet(ticket.Description)))

	httputil.WriteJSON(w, http.StatusOK, fresh)
}

// Reopen — POST /tickets/{id}/reopen. User reopens a resolved ticket that
// wasn't actually fixed: resolved -> open. Re-notifies admins.
func (h *TicketHandler) Reopen(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid ticket id"))
		return
	}
	found, err := h.ticketRepo.Reopen(r.Context(), ticketID, userID)
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if !found {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("CANNOT_REOPEN", "only a resolved request can be reopened"))
		return
	}
	fresh, _ := h.ticketRepo.GetByIDForUser(r.Context(), ticketID, userID)
	if fresh != nil {
		fresh.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), ticketID)
		h.signTicket(fresh)
	}
	h.fanOutToAdmins(r, ticketID, userID, "Support request reopened", "A user reopened a resolved request.")
	httputil.WriteJSON(w, http.StatusOK, fresh)
}

// fanOutToAdmins refreshes the admin queue live (WS) and drops a durable
// notification for every admin (so an offline admin still sees it).
func (h *TicketHandler) fanOutToAdmins(r *http.Request, ticketID, actorID uuid.UUID, title, body string) {
	adminIDs, _ := h.adminRepo.GetAdminUserIDs(r.Context())
	if len(adminIDs) == 0 {
		return
	}
	h.wsHub.Broadcast(&ws.Event{
		Type:          "ticket_submitted",
		TargetUserIDs: adminIDs,
		Payload:       map[string]any{"ticket_id": ticketID, "user_id": actorID},
	})
	if h.notifHandler != nil {
		for _, adminID := range adminIDs {
			go h.notifHandler.Notify(adminID, models.NotificationTypeSystem, title, body, nil, nil)
		}
	}
}

// --- Admin surface ---

// AdminList — GET /admin/tickets. The queue.
func (h *TicketHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	page, limit := parsePage(r)
	status := r.URL.Query().Get("status")
	result, err := h.ticketRepo.AdminList(r.Context(), page, limit, status)
	if err != nil {
		h.logger.Error("admin list tickets", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	for i := range result.Items {
		h.signTicket(&result.Items[i].SupportTicket)
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

// AdminGet — GET /admin/tickets/{id}. Detail with reporter identity + signed evidence.
func (h *TicketHandler) AdminGet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	row, err := h.ticketRepo.AdminGet(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "ticket not found"))
		return
	}
	row.Attachments, _ = h.ticketRepo.ListAttachments(r.Context(), id)
	h.signTicket(&row.SupportTicket)
	httputil.WriteJSON(w, http.StatusOK, row)
}

// AdminUpdateStatus — PATCH /admin/tickets/{id}/status. open/resolved/closed;
// notifies the ticket's owner on the change (the accident flow notified nobody).
func (h *TicketHandler) AdminUpdateStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("status is required"))
		return
	}
	// Admins can't move a ticket back to draft; that state is the user's alone.
	valid := map[string]bool{"open": true, "resolved": true, "closed": true}
	if !valid[body.Status] {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid status"))
		return
	}
	userID, err := h.ticketRepo.AdminUpdateStatus(r.Context(), id, models.TicketStatus(body.Status))
	if err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	// Tell the user their request moved — this is the difference between a
	// tracked request and a black hole.
	if h.notifHandler != nil && userID != uuid.Nil {
		title, msg := statusChangeMessage(models.TicketStatus(body.Status))
		go h.notifHandler.Notify(userID, models.NotificationTypeSystem, title, msg, nil, nil)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- helpers ---

func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 100 {
		return s[:100] + "…"
	}
	return s
}

func categoryLabel(c models.TicketCategory) string {
	switch c {
	case models.TicketCategoryAccount:
		return "Account & verification"
	case models.TicketCategoryListing:
		return "Listing a car"
	case models.TicketCategoryRenting:
		return "Renting a car"
	case models.TicketCategoryBuySell:
		return "Buying or selling a car"
	case models.TicketCategoryPayments:
		return "Payments & refunds"
	default:
		// The submission screen itself is titled "Report a Problem" (7/24
		// item 3c), so the catch-all category reads "Other" everywhere.
		return "Other"
	}
}

func statusChangeMessage(s models.TicketStatus) (title, body string) {
	switch s {
	case models.TicketStatusResolved:
		return "Your request was resolved", "We've marked your support request resolved. If it's not fixed, you can reopen it from My requests."
	case models.TicketStatusClosed:
		return "Your request was closed", "Your support request has been closed."
	default:
		return "Your request was updated", "There's an update on your support request."
	}
}
