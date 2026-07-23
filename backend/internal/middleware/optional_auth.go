package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
)

// OptionalAuth authenticates a request when it carries a bearer token and
// passes it through anonymously when it carries none. It exists for
// mixed-audience read endpoints (today: the public listings surface), where
// anonymous callers receive a redacted payload and authenticated callers the
// full one.
//
// Semantics, deliberately:
//   - No Authorization header → anonymous: the handler sees no user in the
//     context and must serve the redacted shape.
//   - Malformed, invalid, or expired token → 401, exactly like
//     AuthMiddleware. A signed-in client with a stale token must be told to
//     refresh (its retry machinery handles that) rather than silently
//     falling back to the anonymous view.
//   - Valid token but blocked account → 403 ACCOUNT_BLOCKED. An admin block
//     must not keep working on elevated surfaces; the iOS client force
//     signs out on this code, after which the request comes back anonymous.
//   - Valid token → the same context keys AuthMiddleware sets, so handlers
//     and downstream middleware read the user identically on both paths.
func OptionalAuth(jwtService *auth.JWTService, blockList *auth.BlockChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				next.ServeHTTP(w, r)
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
				return
			}

			claims, err := jwtService.ValidateAccessToken(parts[1])
			if err != nil {
				if apiErr := models.GetAPIError(err); apiErr != nil {
					httputil.WriteError(w, http.StatusUnauthorized, apiErr)
				} else {
					httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
				}
				return
			}

			if blockList != nil && blockList.IsBlocked(r.Context(), claims.UserID) {
				httputil.WriteError(w, http.StatusForbidden, models.ErrAccountBlocked)
				return
			}

			ctx := r.Context()
			ctx = context.WithValue(ctx, httputil.UserIDKey, claims.UserID)
			ctx = context.WithValue(ctx, httputil.EmailKey, claims.Email)
			ctx = context.WithValue(ctx, httputil.RoleKey, claims.Role)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
