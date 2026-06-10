package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"spaceempire/back/internal/domain"
)

// SelfReader is the slice of players.Repository the /api/player/me handler
// needs: the wallet balance and the explicit active ship (10.14a). Declared
// per ISP so the auth package does not import internal/persistence/players.
type SelfReader interface {
	GetCash(ctx context.Context, playerID domain.PlayerID) (int64, error)
	// ActiveShip returns the player's selected active ship; ok=false when
	// active_ship_id is NULL (the SPA then falls back to its min-id rule).
	ActiveShip(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool, error)
	// PassengerHost returns the ship the player rides as a passenger (10.23);
	// ok=false when not a passenger. The SPA renders a read-only host HUD.
	PassengerHost(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool, error)
}

// ServerConfig knobs control cookie behavior. SessionTTL controls how long
// the cookie's MaxAge is set to and matches the session row's expires_at.
type ServerConfig struct {
	// CookieSecure marks the cookie as Secure (HTTPS-only). Off in dev.
	CookieSecure bool
	// SessionTTLSeconds is the cookie Max-Age. Must equal Service's
	// SessionTTL so the browser drops the cookie when the session expires.
	SessionTTLSeconds int
}

// Server wires the auth HTTP handlers and the RequireAuth middleware to a
// single Service instance.
type Server struct {
	svc    *Service
	cfg    ServerConfig
	logger *slog.Logger
}

// NewServer constructs a Server. If logger is nil, slog.Default is used.
func NewServer(svc *Service, cfg ServerConfig, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{svc: svc, cfg: cfg, logger: logger}
}

// RegisterRoutes mounts the auth endpoints on the given mux. Mounted paths:
//
//	POST /api/auth/register
//	POST /api/auth/login
//	POST /api/auth/logout
//	GET  /api/auth/me
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
}

// RegisterPlayersList mounts GET /api/players behind RequireAuth so the
// SPA can resolve playerID → login for ships in the world.
func (s *Server) RegisterPlayersList(mux *http.ServeMux) {
	mux.Handle("GET /api/players", s.RequireAuth(http.HandlerFunc(s.handlePlayers)))
}

// RegisterPlayerSelf mounts GET /api/player/me behind RequireAuth. The
// endpoint returns the authenticated player together with their wallet
// balance — used by the SPA's HUD and the station view. Pass the
// players.Repository (or any SelfReader) so the handler can read cash and the
// active ship without auth importing that package.
func (s *Server) RegisterPlayerSelf(mux *http.ServeMux, reader SelfReader) {
	mux.Handle("GET /api/player/me", s.RequireAuth(s.handlePlayerSelf(reader)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
