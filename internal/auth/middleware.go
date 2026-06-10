package auth

import (
	"context"
	"errors"
	"net/http"

	"spaceempire/back/internal/domain"
)

// SessionCookieName is the name of the secure HttpOnly cookie that carries
// the session token. Centralised so handlers and middleware agree.
const SessionCookieName = "session"

type ctxKey int

const playerIDKey ctxKey = 0

// ContextWithPlayerID stores the authenticated player ID on the context.
// Tests and downstream handlers use PlayerIDFromContext to read it back.
func ContextWithPlayerID(ctx context.Context, id domain.PlayerID) context.Context {
	return context.WithValue(ctx, playerIDKey, id)
}

// PlayerIDFromContext returns the player ID stored by RequireAuth. The
// second return is false when the context was not enriched (no auth).
func PlayerIDFromContext(ctx context.Context) (domain.PlayerID, bool) {
	v, ok := ctx.Value(playerIDKey).(domain.PlayerID)
	return v, ok
}

// RequireAuth wraps an http.Handler so that it only runs when a valid
// session cookie is present. The wrapped handler receives a context
// enriched with the authenticated PlayerID. Otherwise it responds 401.
func (s *Server) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		player, err := s.svc.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, ErrNotAuthenticated) {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		ctx := ContextWithPlayerID(r.Context(), player.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
