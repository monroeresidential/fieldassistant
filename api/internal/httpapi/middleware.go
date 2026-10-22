package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
)

type claimsKey struct{}

// requireSession authenticates /v1 requests other than publicPaths. The
// Authorization header must carry a valid access token whose session still
// exists and is unexpired; otherwise the request gets 401 unauthorized. The
// verified claims are put in the request context for claimsFrom.
func requireSession(ss *auth.Sessions, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if publicPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}
			claims, err := ss.Verify(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
				return
			}
			if err := ss.Lookup(r.Context(), claims.SessionID); err != nil {
				if errors.Is(err, auth.ErrSessionRevoked) {
					writeError(w, http.StatusUnauthorized, "unauthorized", "session revoked")
					return
				}
				logger.Error("session lookup", "path", r.URL.Path, "error", err)
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
		})
	}
}

// claimsFrom returns the claims requireSession stored in ctx. It returns
// zero Claims on public routes, which never carry them.
func claimsFrom(ctx context.Context) auth.Claims {
	c, _ := ctx.Value(claimsKey{}).(auth.Claims)
	return c
}
