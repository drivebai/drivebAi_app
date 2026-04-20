package handlers

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type UserHandler struct {
	userRepo    *repository.UserRepository
	docRepo     *repository.DocumentRepository
	profileRepo *repository.ProfileRepository
	tokenRepo   *repository.TokenRepository
	jwtSvc      *auth.JWTService
	logger      *slog.Logger
	uploadDir   string
}

func NewUserHandler(
	userRepo *repository.UserRepository,
	docRepo *repository.DocumentRepository,
	profileRepo *repository.ProfileRepository,
	tokenRepo *repository.TokenRepository,
	jwtSvc *auth.JWTService,
	uploadDir string,
	logger *slog.Logger,
) *UserHandler {
	return &UserHandler{
		userRepo:    userRepo,
		docRepo:     docRepo,
		profileRepo: profileRepo,
		tokenRepo:   tokenRepo,
		jwtSvc:      jwtSvc,
		logger:      logger,
		uploadDir:   uploadDir,
	}
}

func (h *UserHandler) GetCurrentUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			WriteError(w, http.StatusNotFound, apiErr)
		} else {
			h.logger.Error("failed to get user", "error", err)
			WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	profile := UserProfile{
		ID:               user.ID,
		Email:            user.Email,
		Role:             user.Role,
		FirstName:        user.FirstName,
		LastName:         user.LastName,
		Phone:            user.Phone,
		IsEmailVerified:  user.IsEmailVerified,
		OnboardingStatus: user.OnboardingStatus,
		ProfilePhotoURL:  user.ProfilePhotoURL,
	}

	WriteJSON(w, http.StatusOK, profile)
}

type UpdateProfileRequest struct {
	Role      *models.Role `json:"role,omitempty"`
	FirstName *string      `json:"first_name,omitempty"`
	LastName  *string      `json:"last_name,omitempty"`
	Phone     *string      `json:"phone,omitempty"`
}

func (h *UserHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var req UpdateProfileRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_REQUEST", "Invalid request body"))
		return
	}

	// Get current user
	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		if apiErr := models.GetAPIError(err); apiErr != nil {
			WriteError(w, http.StatusNotFound, apiErr)
		} else {
			h.logger.Error("failed to get user", "error", err)
			WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		}
		return
	}

	// Update role if provided
	if req.Role != nil {
		// Validate role - only driver and car_owner allowed via API
		if *req.Role != models.RoleDriver && *req.Role != models.RoleCarOwner {
			WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_ROLE", "Role must be 'driver' or 'car_owner'"))
			return
		}
		if err := h.userRepo.UpdateRole(r.Context(), userID, *req.Role); err != nil {
			h.logger.Error("failed to update role", "error", err)
			WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		user.Role = *req.Role
	}

	// Update other fields if provided
	updated := false
	if req.FirstName != nil {
		user.FirstName = *req.FirstName
		updated = true
	}
	if req.LastName != nil {
		user.LastName = *req.LastName
		updated = true
	}
	if req.Phone != nil {
		user.Phone = req.Phone
		updated = true
	}

	if updated {
		if err := h.userRepo.Update(r.Context(), user); err != nil {
			h.logger.Error("failed to update user", "error", err)
			WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
	}

	// Reload user to get updated data
	user, _ = h.userRepo.GetByID(r.Context(), userID)

	profile := UserProfile{
		ID:               user.ID,
		Email:            user.Email,
		Role:             user.Role,
		FirstName:        user.FirstName,
		LastName:         user.LastName,
		Phone:            user.Phone,
		IsEmailVerified:  user.IsEmailVerified,
		OnboardingStatus: user.OnboardingStatus,
		ProfilePhotoURL:  user.ProfilePhotoURL,
	}

	WriteSuccess(w, http.StatusOK, "Profile updated successfully", profile)
}

// Document upload handlers

type DocumentResponse struct {
	ID        uuid.UUID             `json:"id"`
	Type      models.DocumentType   `json:"type"`
	FileName  string                `json:"file_name"`
	FileSize  int64                 `json:"file_size"`
	Status    models.DocumentStatus `json:"status"`
	CreatedAt string                `json:"created_at"`
}

func (h *UserHandler) UploadDocument(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	// Get document type from URL
	docTypeStr := chi.URLParam(r, "type")
	docType := models.DocumentType(docTypeStr)
	if !docType.IsValid() {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_DOCUMENT_TYPE", "Document type must be 'drivers_license' or 'registration'"))
		return
	}

	// Parse multipart form (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_REQUEST", "File too large or invalid form data"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_REQUEST", "File is required"))
		return
	}
	defer file.Close()

	// Validate file type
	contentType := header.Header.Get("Content-Type")
	if !isValidDocumentMimeType(contentType) {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_FILE_TYPE", "File must be an image (JPEG, PNG) or PDF"))
		return
	}

	// Create user upload directory
	userDir := filepath.Join(h.uploadDir, userID.String())
	if err := os.MkdirAll(userDir, 0755); err != nil {
		h.logger.Error("failed to create upload directory", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Generate unique filename
	ext := filepath.Ext(header.Filename)
	newFileName := fmt.Sprintf("%s_%s%s", docType, uuid.New().String(), ext)
	filePath := filepath.Join(userDir, newFileName)

	// Save file
	dst, err := os.Create(filePath)
	if err != nil {
		h.logger.Error("failed to create file", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		h.logger.Error("failed to save file", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Delete existing document of same type
	h.docRepo.DeleteByUserIDAndType(r.Context(), userID, docType)

	// Save document record
	doc := &models.Document{
		UserID:   userID,
		Type:     docType,
		FileName: header.Filename,
		FilePath: filePath,
		FileSize: written,
		MimeType: contentType,
		Status:   models.DocumentStatusUploaded,
	}

	if err := h.docRepo.Create(r.Context(), doc); err != nil {
		h.logger.Error("failed to save document record", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Check if user has all required documents and update onboarding status
	hasAllDocs, _ := h.docRepo.HasRequiredDocuments(r.Context(), userID)
	if hasAllDocs {
		h.userRepo.UpdateOnboardingStatus(r.Context(), userID, models.OnboardingDocumentsUploaded)
	}

	WriteJSON(w, http.StatusCreated, DocumentResponse{
		ID:        doc.ID,
		Type:      doc.Type,
		FileName:  doc.FileName,
		FileSize:  doc.FileSize,
		Status:    doc.Status,
		CreatedAt: doc.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

func (h *UserHandler) GetDocuments(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	docs, err := h.docRepo.GetByUserID(r.Context(), userID)
	if err != nil {
		h.logger.Error("failed to get documents", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	response := make([]DocumentResponse, 0, len(docs))
	for _, doc := range docs {
		response = append(response, DocumentResponse{
			ID:        doc.ID,
			Type:      doc.Type,
			FileName:  doc.FileName,
			FileSize:  doc.FileSize,
			Status:    doc.Status,
			CreatedAt: doc.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	WriteJSON(w, http.StatusOK, response)
}

func (h *UserHandler) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	docID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_ID", "Invalid document ID"))
		return
	}

	// Get document to verify ownership
	doc, err := h.docRepo.GetByID(r.Context(), docID)
	if err != nil {
		h.logger.Error("failed to get document", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	if doc == nil {
		WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "Document not found"))
		return
	}

	if doc.UserID != userID {
		WriteError(w, http.StatusForbidden, models.NewAPIError("FORBIDDEN", "Not authorized to delete this document"))
		return
	}

	// Delete file from disk
	os.Remove(doc.FilePath)

	// Delete from database
	if err := h.docRepo.Delete(r.Context(), docID); err != nil {
		h.logger.Error("failed to delete document", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"message": "Document deleted"})
}

// Profile photo upload

func (h *UserHandler) UploadProfilePhoto(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	// Parse multipart form (max 5MB for photos)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_REQUEST", "File too large or invalid form data"))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_REQUEST", "File is required"))
		return
	}
	defer file.Close()

	// Validate file type (images only)
	contentType := header.Header.Get("Content-Type")
	if !isValidImageMimeType(contentType) {
		WriteError(w, http.StatusBadRequest, models.NewAPIError("INVALID_FILE_TYPE", "File must be an image (JPEG, PNG)"))
		return
	}

	// Get current user to check for existing photo
	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("failed to get user", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Create user upload directory
	userDir := filepath.Join(h.uploadDir, userID.String())
	if err := os.MkdirAll(userDir, 0755); err != nil {
		h.logger.Error("failed to create upload directory", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Delete old profile photo if exists
	if user.ProfilePhotoURL != nil && *user.ProfilePhotoURL != "" {
		oldPath := filepath.Join(h.uploadDir, "..", *user.ProfilePhotoURL)
		os.Remove(oldPath) // Ignore error - old file may not exist
	}

	// Generate unique filename
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		// Default to .jpg if no extension
		ext = ".jpg"
	}
	newFileName := fmt.Sprintf("profile_%s%s", uuid.New().String(), ext)
	filePath := filepath.Join(userDir, newFileName)

	// Save file
	dst, err := os.Create(filePath)
	if err != nil {
		h.logger.Error("failed to create file", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		h.logger.Error("failed to save file", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Update user profile photo URL (relative path for now)
	photoURL := fmt.Sprintf("/uploads/%s/%s", userID.String(), newFileName)
	if err := h.userRepo.UpdateProfilePhoto(r.Context(), userID, photoURL); err != nil {
		h.logger.Error("failed to update profile photo", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Update onboarding status if needed
	if user.OnboardingStatus == models.OnboardingRoleSelected || user.OnboardingStatus == models.OnboardingCreated {
		h.userRepo.UpdateOnboardingStatus(r.Context(), userID, models.OnboardingPhotoUploaded)
	}

	// Reload user to get updated data
	user, err = h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("failed to reload user", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Return full user profile (same format as GET /me)
	profile := UserProfile{
		ID:               user.ID,
		Email:            user.Email,
		Role:             user.Role,
		FirstName:        user.FirstName,
		LastName:         user.LastName,
		Phone:            user.Phone,
		IsEmailVerified:  user.IsEmailVerified,
		OnboardingStatus: user.OnboardingStatus,
		ProfilePhotoURL:  user.ProfilePhotoURL,
	}

	WriteJSON(w, http.StatusOK, profile)
}

// Onboarding completion

func (h *UserHandler) CompleteOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("failed to get user", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// For drivers, require documents
	if user.Role == models.RoleDriver {
		hasAllDocs, _ := h.docRepo.HasRequiredDocuments(r.Context(), userID)
		if !hasAllDocs {
			WriteError(w, http.StatusBadRequest, models.NewAPIError("INCOMPLETE_ONBOARDING", "Please upload required documents"))
			return
		}
	}

	// Update onboarding status to complete
	if err := h.userRepo.UpdateOnboardingStatus(r.Context(), userID, models.OnboardingComplete); err != nil {
		h.logger.Error("failed to update onboarding status", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{"message": "Onboarding completed"})
}

// ─── Mode profiles ──────────────────────────────────────────────────────────
//
// A single user identity (users row, unique by email) may own one profile per
// non-admin role (driver, car_owner). The "active" profile determines which
// role-scoped tabs + permissions the app uses. Switching profiles is what the
// iOS "Switch to Driver/Owner mode" UI calls.

type ProfileSummary struct {
	ID               uuid.UUID              `json:"id"`
	Role             models.Role            `json:"role"`
	OnboardingStatus models.OnboardingStatus `json:"onboarding_status"`
	HasRequiredDocs  bool                   `json:"has_required_docs"`
	IsActive         bool                   `json:"is_active"`
	CreatedAt        string                 `json:"created_at"`
}

type ListProfilesResponse struct {
	Profiles         []ProfileSummary `json:"profiles"`
	ActiveProfileID  *uuid.UUID       `json:"active_profile_id,omitempty"`
	ActiveRole       *models.Role     `json:"active_role,omitempty"`
}

type CreateProfileRequest struct {
	Role models.Role `json:"role"`
}

type SwitchProfileRequest struct {
	Role      *models.Role `json:"role,omitempty"`
	ProfileID *uuid.UUID   `json:"profile_id,omitempty"`
}

type SwitchProfileResponse struct {
	AccessToken    string             `json:"access_token"`
	RefreshToken   string             `json:"refresh_token"`
	ExpiresAt      models.RFC3339Time `json:"expires_at"`
	User           UserProfile        `json:"user"`
	ActiveProfile  ProfileSummary     `json:"active_profile"`
}

// ensureActiveProfile self-heals users missing an active_profile_id (e.g. legacy
// rows predating migration 000013, or rows that drifted via the legacy PATCH
// /profile role-change path). It makes sure the user's current users.role has
// a matching user_profiles row and that it is pointed to by active_profile_id.
func (h *UserHandler) ensureActiveProfile(ctx context.Context, user *models.User) (*models.Profile, error) {
	if user.Role != models.RoleDriver && user.Role != models.RoleCarOwner {
		return nil, nil
	}

	activeID, err := h.userRepo.GetActiveProfileID(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	if activeID != nil {
		profile, err := h.profileRepo.GetByID(ctx, *activeID)
		if err != nil {
			return nil, err
		}
		if profile != nil {
			return profile, nil
		}
	}

	profile, err := h.profileRepo.Create(ctx, user.ID, user.Role, user.OnboardingStatus)
	if err != nil {
		return nil, err
	}
	if err := h.userRepo.SetActiveProfile(ctx, user.ID, profile.ID, profile.Role); err != nil {
		return nil, err
	}
	return profile, nil
}

// ListMyProfiles returns all mode profiles belonging to the current user,
// plus a pointer to which one is active.
func (h *UserHandler) ListMyProfiles(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("list profiles: failed to get user", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	if _, err := h.ensureActiveProfile(r.Context(), user); err != nil {
		h.logger.Error("list profiles: ensureActiveProfile failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	profiles, err := h.profileRepo.ListByUserID(r.Context(), userID)
	if err != nil {
		h.logger.Error("list profiles: query failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	activeID, _ := h.userRepo.GetActiveProfileID(r.Context(), userID)

	hasDocs, _ := h.docRepo.HasRequiredDocuments(r.Context(), userID)

	summaries := make([]ProfileSummary, 0, len(profiles))
	for _, p := range profiles {
		isActive := activeID != nil && *activeID == p.ID
		// Only driver profiles require documents; owner profiles are always "docs-ok"
		profileHasDocs := true
		if p.Role == models.RoleDriver {
			profileHasDocs = hasDocs
		}
		summaries = append(summaries, ProfileSummary{
			ID:               p.ID,
			Role:             p.Role,
			OnboardingStatus: p.OnboardingStatus,
			HasRequiredDocs:  profileHasDocs,
			IsActive:         isActive,
			CreatedAt:        p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}

	var activeRole *models.Role
	if activeID != nil {
		for _, p := range profiles {
			if p.ID == *activeID {
				r := p.Role
				activeRole = &r
				break
			}
		}
	}

	WriteJSON(w, http.StatusOK, ListProfilesResponse{
		Profiles:        summaries,
		ActiveProfileID: activeID,
		ActiveRole:      activeRole,
	})
}

// CreateMyProfile is an idempotent create — if the profile already exists for
// (user, role) it returns the existing row. Creating a profile does NOT make
// it active; the client must call SetActiveProfile for that.
func (h *UserHandler) CreateMyProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var req CreateProfileRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if req.Role != models.RoleDriver && req.Role != models.RoleCarOwner {
		WriteError(w, http.StatusBadRequest, models.NewAPIError(models.ErrCodeInvalidRole, "Role must be 'driver' or 'car_owner'"))
		return
	}

	profile, err := h.profileRepo.Create(r.Context(), userID, req.Role, models.OnboardingRoleSelected)
	if err != nil {
		h.logger.Error("create profile: failed", "error", err, "user_id", userID, "role", req.Role)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	hasDocs := true
	if profile.Role == models.RoleDriver {
		hasDocs, _ = h.docRepo.HasRequiredDocuments(r.Context(), userID)
	}
	activeID, _ := h.userRepo.GetActiveProfileID(r.Context(), userID)

	WriteJSON(w, http.StatusOK, ProfileSummary{
		ID:               profile.ID,
		Role:             profile.Role,
		OnboardingStatus: profile.OnboardingStatus,
		HasRequiredDocs:  hasDocs,
		IsActive:         activeID != nil && *activeID == profile.ID,
		CreatedAt:        profile.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// SetActiveProfile switches which role the user is acting as. Rules:
//   - target role must be driver or car_owner
//   - if target profile does not exist, it is created
//   - switching TO driver requires all driver documents to exist — if missing,
//     we return 409 DRIVER_DOCS_REQUIRED with details.missing_types, and the
//     iOS client is expected to run the DocumentUpload flow and retry.
//   - switching TO car_owner has no gating
//
// On success we mint a fresh access + refresh token pair so that the role
// embedded in the JWT reflects the new active profile immediately.
func (h *UserHandler) SetActiveProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}

	var req SwitchProfileRequest
	if err := DecodeJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil {
		h.logger.Error("switch profile: failed to get user", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Resolve the target role from either the explicit role field or profile_id.
	var targetRole models.Role
	switch {
	case req.ProfileID != nil:
		p, err := h.profileRepo.GetByID(r.Context(), *req.ProfileID)
		if err != nil {
			h.logger.Error("switch profile: lookup by id failed", "error", err)
			WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if p == nil || p.UserID != userID {
			WriteError(w, http.StatusNotFound, models.NewAPIError(models.ErrCodeProfileNotFound, "Profile not found"))
			return
		}
		targetRole = p.Role
	case req.Role != nil:
		targetRole = *req.Role
	default:
		WriteError(w, http.StatusBadRequest, models.NewValidationError("role or profile_id is required"))
		return
	}

	if targetRole != models.RoleDriver && targetRole != models.RoleCarOwner {
		WriteError(w, http.StatusBadRequest, models.NewAPIError(models.ErrCodeInvalidRole, "Role must be 'driver' or 'car_owner'"))
		return
	}

	// Doc gate: entering driver mode requires all driver documents.
	if targetRole == models.RoleDriver {
		missing, err := h.missingDriverDocs(r.Context(), userID)
		if err != nil {
			h.logger.Error("switch profile: doc check failed", "error", err)
			WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		if len(missing) > 0 {
			missingStrs := make([]string, 0, len(missing))
			for _, m := range missing {
				missingStrs = append(missingStrs, string(m))
			}
			apiErr := models.NewAPIError(models.ErrCodeDriverDocsRequired, "Driver documents required before switching to driver mode").
				WithDetails(map[string]interface{}{
					"missing_types": missingStrs,
				})
			WriteError(w, http.StatusConflict, apiErr)
			return
		}
	}

	// Upsert the target profile (idempotent; creates lazily on first switch).
	targetProfile, err := h.profileRepo.Create(r.Context(), userID, targetRole, models.OnboardingRoleSelected)
	if err != nil {
		h.logger.Error("switch profile: upsert failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Activate + mirror role on users (JWT is minted off users.Role below).
	if err := h.userRepo.SetActiveProfile(r.Context(), userID, targetProfile.ID, targetProfile.Role); err != nil {
		h.logger.Error("switch profile: activate failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	user.Role = targetProfile.Role

	// Issue fresh tokens so the new role is reflected immediately.
	accessToken, expiresAt, err := h.jwtSvc.GenerateAccessToken(user)
	if err != nil {
		h.logger.Error("switch profile: access token mint failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	refreshToken, refreshHash, refreshExpires, err := h.jwtSvc.GenerateRefreshToken()
	if err != nil {
		h.logger.Error("switch profile: refresh token mint failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if err := h.tokenRepo.CreateRefreshToken(r.Context(), &models.RefreshToken{
		UserID:    user.ID,
		TokenHash: refreshHash,
		ExpiresAt: refreshExpires,
	}); err != nil {
		h.logger.Error("switch profile: refresh token persist failed", "error", err)
		WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	hasDocs := true
	if targetProfile.Role == models.RoleDriver {
		hasDocs, _ = h.docRepo.HasRequiredDocuments(r.Context(), userID)
	}

	WriteJSON(w, http.StatusOK, SwitchProfileResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    models.NewRFC3339Time(expiresAt),
		User: UserProfile{
			ID:               user.ID,
			Email:            user.Email,
			Role:             user.Role,
			FirstName:        user.FirstName,
			LastName:         user.LastName,
			Phone:            user.Phone,
			IsEmailVerified:  user.IsEmailVerified,
			OnboardingStatus: user.OnboardingStatus,
			ProfilePhotoURL:  user.ProfilePhotoURL,
		},
		ActiveProfile: ProfileSummary{
			ID:               targetProfile.ID,
			Role:             targetProfile.Role,
			OnboardingStatus: targetProfile.OnboardingStatus,
			HasRequiredDocs:  hasDocs,
			IsActive:         true,
			CreatedAt:        targetProfile.CreatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

func (h *UserHandler) missingDriverDocs(ctx context.Context, userID uuid.UUID) ([]models.DocumentType, error) {
	required := []models.DocumentType{models.DocumentDriversLicense, models.DocumentRegistration}
	var missing []models.DocumentType
	for _, t := range required {
		doc, err := h.docRepo.GetByUserIDAndType(ctx, userID, t)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			missing = append(missing, t)
		}
	}
	return missing, nil
}

// Helper functions

func isValidDocumentMimeType(mimeType string) bool {
	validTypes := []string{"image/jpeg", "image/png", "image/jpg", "application/pdf"}
	for _, t := range validTypes {
		if strings.EqualFold(mimeType, t) {
			return true
		}
	}
	return false
}

func isValidImageMimeType(mimeType string) bool {
	validTypes := []string{"image/jpeg", "image/png", "image/jpg"}
	for _, t := range validTypes {
		if strings.EqualFold(mimeType, t) {
			return true
		}
	}
	return false
}
