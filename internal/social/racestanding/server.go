package racestanding

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
)

// Middleware is the auth gate applied to the standings route.
type Middleware = func(http.Handler) http.Handler

// Server exposes the per-player race-standing read endpoint over a Service.
type Server struct {
	svc    *Service
	logger *slog.Logger
}

// NewServer constructs a Server. A nil logger falls back to slog.Default.
func NewServer(svc *Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{svc: svc, logger: logger}
}

// reportedRaces are the races whose police track per-player standing (the main
// races 1-5). The endpoint always returns a row per race (0 when none stored)
// so the SPA renders the full reputation panel.
var reportedRaces = []domain.RaceID{1, 2, 3, 4, 5}

// RegisterRoutes mounts the standings endpoint behind the auth middleware.
//
//	GET /api/my/race-standings
func (s *Server) RegisterRoutes(mux *http.ServeMux, authMW Middleware) {
	mux.Handle("GET /api/my/race-standings", authMW(http.HandlerFunc(s.handleMyStandings)))
}

// standingDTO is the JSON shape of one race standing.
type standingDTO struct {
	Race     int  `json:"race"`
	Standing int  `json:"standing"`
	Wanted   bool `json:"wanted"`
}

type standingsResponse struct {
	Items           []standingDTO `json:"items"`
	WantedThreshold int           `json:"wantedThreshold"`
}

func (s *Server) handleMyStandings(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	snap := s.svc.SnapshotForPlayer(player)
	out := make([]standingDTO, 0, len(reportedRaces))
	for _, race := range reportedRaces {
		v := snap[race]
		out = append(out, standingDTO{Race: int(race), Standing: v, Wanted: v <= s.svc.WantedThreshold()})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(standingsResponse{Items: out, WantedThreshold: s.svc.WantedThreshold()})
}
