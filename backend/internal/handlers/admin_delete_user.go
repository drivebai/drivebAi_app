package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// DeleteUser — DELETE /api/v1/admin/users/{id} (batch item 3).
//
// SOFT delete: anonymize-in-place, never row removal. A hard DELETE is
// RESTRICT-blocked for any user with purchase history and, where it isn't,
// cascades away counterparties' chats, payments (the only local copy of
// Stripe payment-intent ids), and refund audit trails. The tombstone frees
// the email and phone so the person can register fresh, blocks the account
// at every auth surface, and keeps transaction history attributed to
// "Deleted User".
//
// Guards: an admin cannot delete themselves, and admin accounts cannot be
// deleted at all (an admin tombstone would strand support history and the
// resolved-by references admins hold).
func (h *AdminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid id"))
		return
	}

	callerID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	if callerID == id {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("CANNOT_DELETE_SELF", "you can't delete your own account"))
		return
	}

	target, err := h.adminRepo.GetUserDetail(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.NewAPIError("NOT_FOUND", "user not found"))
		return
	}
	if target.Role == string(models.RoleAdmin) {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("CANNOT_DELETE_ADMIN", "admin accounts can't be deleted"))
		return
	}
	if target.DeletedAt != nil {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_DELETED", "this account is already deleted"))
		return
	}

	if err := h.adminRepo.SoftDeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, models.ErrUserNotFound) {
			httputil.WriteError(w, http.StatusConflict, models.NewAPIError("ALREADY_DELETED", "this account is already deleted"))
			return
		}
		h.logger.Error("admin delete user", "error", err, "user_id", id)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Make the deletion bite NOW — same sequence as BlockUser: revoke every
	// refresh token, cut live sockets, drop the block-cache entry so this
	// instance enforces immediately.
	if h.tokenRepo != nil {
		if err := h.tokenRepo.RevokeAllForUser(r.Context(), id); err != nil {
			h.logger.Error("admin delete user: revoke refresh tokens", "error", err, "user_id", id)
		}
	}
	if h.wsHub != nil {
		h.wsHub.DisconnectUser(id)
	}
	if h.blockList != nil {
		h.blockList.Invalidate(id)
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
