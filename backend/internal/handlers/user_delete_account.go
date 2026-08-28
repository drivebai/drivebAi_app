package handlers

import (
	"net/http"
	"os"
	"strings"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

// Self-service account deletion — DELETE /api/v1/users/me (App Review
// 5.1.1(v): account creation requires in-app account DELETION; deactivation
// is insufficient, and routing the user to support is forbidden).
//
// The deletion itself is AdminRepository.SoftDeleteUser — the exact
// anonymize-in-place path the admin console uses; there is deliberately no
// second deletion implementation. Around it, this handler:
//   - refuses while money is captured or a car is physically out
//     (409 DELETION_BLOCKED with per-item, self-service instructions)
//   - auto-resolves open lease requests that hold no captured money
//     (cancel/decline + unreserve + counterparty notification), cancelling
//     any dangling Stripe intent best-effort
//   - archives the user's listings and permanently deletes their identity
//     documents (rows + files)
//   - revokes every refresh token, cuts live sockets, and invalidates the
//     block cache so the deletion bites immediately
//
// Confirmation: the user types DELETE; accounts with a password also
// re-enter it. Both checks happen before anything is touched.

// accountDeletionDeps bundles the collaborators DeleteAccount needs beyond
// UserHandler's own fields. Wired via SetAccountDeletionDependencies in
// main.go — setter, per the house pattern.
type accountDeletionDeps struct {
	adminRepo    *repository.AdminRepository
	leaseRepo    *repository.LeaseRequestRepository
	carRepo      *repository.CarRepository
	stripe       *stripeService.Service
	wsHub        *ws.Hub
	blockList    *auth.BlockChecker
	notifHandler *NotificationHandler
}

// SetAccountDeletionDependencies wires the self-service deletion
// collaborators.
func (h *UserHandler) SetAccountDeletionDependencies(
	adminRepo *repository.AdminRepository,
	leaseRepo *repository.LeaseRequestRepository,
	carRepo *repository.CarRepository,
	stripe *stripeService.Service,
	wsHub *ws.Hub,
	blockList *auth.BlockChecker,
	notifHandler *NotificationHandler,
) {
	h.deletionDeps = &accountDeletionDeps{
		adminRepo:    adminRepo,
		leaseRepo:    leaseRepo,
		carRepo:      carRepo,
		stripe:       stripe,
		wsHub:        wsHub,
		blockList:    blockList,
		notifHandler: notifHandler,
	}
}

// DeleteAccount — DELETE /api/v1/users/me. Acts on the CALLER only; there
// is no id parameter by design.
func (h *UserHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	deps := h.deletionDeps
	if deps == nil || deps.adminRepo == nil {
		h.logger.Error("account deletion: dependencies not wired")
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil || user == nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrUserNotFound)
		return
	}
	// Admin accounts keep the existing rule from the admin path: no
	// tombstoned admins (support history + resolved-by references).
	if user.Role == models.RoleAdmin {
		httputil.WriteError(w, http.StatusForbidden, models.NewAPIError("CANNOT_DELETE_ADMIN", "admin accounts can't be deleted"))
		return
	}

	// Confirmation FIRST — nothing is inspected or touched until intent is
	// proven. Typed word for everyone; password additionally for accounts
	// that have one (passwordless email-code accounts have no password to
	// re-enter, and the email rail is not a dependency we may add here).
	var body models.DeleteAccountBody
	if err := httputil.DecodeJSON(r, &body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("Invalid request body"))
		return
	}
	if strings.TrimSpace(strings.ToUpper(body.Confirm)) != "DELETE" {
		httputil.WriteError(w, http.StatusBadRequest, models.NewAPIError(models.ErrCodeInvalidDeleteConfirm, "Type DELETE to confirm"))
		return
	}
	if user.PasswordHash != nil && *user.PasswordHash != "" {
		if !auth.CheckPassword(body.Password, *user.PasswordHash) {
			httputil.WriteError(w, http.StatusForbidden, models.NewAPIError(models.ErrCodeInvalidDeleteConfirm, "Incorrect password"))
			return
		}
	}

	// Blockers: captured money or a physically-out car. Every blocker's
	// Detail names its in-app exit — never "contact support".
	blockers, err := h.userRepo.ListAccountDeletionBlockers(r.Context(), userID)
	if err != nil {
		h.logger.Error("account deletion: list blockers", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	if len(blockers) > 0 {
		// The Message carries the full per-item instructions — the iOS
		// error surface renders messages, and Apple's bar is that the user
		// can see and finish every step in-app. Structured Details ride
		// along for richer clients.
		msg := "Finish these first — each can be completed in the app:"
		for _, b := range blockers {
			msg += "\n• " + b.Detail
		}
		httputil.WriteError(w, http.StatusConflict, &models.APIError{
			Code:    models.ErrCodeDeletionBlocked,
			Message: msg,
			Details: map[string]interface{}{"blockers": blockers},
		})
		return
	}

	// Auto-resolve open lease requests (no captured money by definition
	// here). Counterparties are notified; dangling Stripe intents cancelled
	// best-effort so a webhook can never resurrect a cancelled lease.
	closed, err := deps.leaseRepo.CancelOpenLeasesForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("account deletion: cancel open leases", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	for _, c := range closed {
		if c.PaymentIntentID != nil && deps.stripe != nil {
			if cerr := deps.stripe.CancelPaymentIntent(*c.PaymentIntentID); cerr != nil {
				h.logger.Warn("account deletion: cancel stripe intent", "error", cerr, "lease_request_id", c.ID)
			}
		}
		counterparty := c.OwnerID
		if !c.WasDriver {
			counterparty = c.DriverID
		}
		chatID := c.ChatID
		leaseID := c.ID
		if deps.notifHandler != nil {
			go deps.notifHandler.Notify(counterparty, models.NotificationTypeLeaseRequest,
				"Request closed",
				"The other party's account was deleted, so the request on "+carTitleOr(c.CarTitle)+" was closed.",
				&chatID, &leaseID)
		}
	}

	// Their listings leave the marketplace; their identity documents are
	// permanently removed (rows first, then files best-effort).
	if _, err := deps.carRepo.ArchiveCarsForOwner(r.Context(), userID); err != nil {
		h.logger.Error("account deletion: archive cars", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	paths, err := h.docRepo.DeleteAllForUser(r.Context(), userID)
	if err != nil {
		h.logger.Error("account deletion: delete documents", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if rmErr := os.Remove(p); rmErr != nil && !os.IsNotExist(rmErr) {
			h.logger.Warn("account deletion: unlink document file", "error", rmErr)
		}
	}

	// The tombstone — the single shared deletion path.
	if err := deps.adminRepo.SoftDeleteUser(r.Context(), userID); err != nil {
		h.logger.Error("account deletion: soft delete", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}

	// Bite immediately: same sequence as the admin path and BlockUser.
	if h.tokenRepo != nil {
		if err := h.tokenRepo.RevokeAllForUser(r.Context(), userID); err != nil {
			h.logger.Error("account deletion: revoke refresh tokens", "error", err, "user_id", userID)
		}
	}
	if deps.wsHub != nil {
		deps.wsHub.DisconnectUser(userID)
	}
	if deps.blockList != nil {
		deps.blockList.Invalidate(userID)
	}

	h.logger.Info("account self-deleted", "user_id", userID, "open_leases_closed", len(closed), "documents_removed", len(paths))
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
