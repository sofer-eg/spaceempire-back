package observ

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuth wraps h behind HTTP basic auth. When user is empty the gate is
// disabled (dev) and h is returned unwrapped — production sets DebugUser/
// DebugPass to protect /metrics and /debug/*. Comparison is constant-time.
func BasicAuth(user, pass string, h http.Handler) http.Handler {
	if user == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="spaceempire-debug"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
