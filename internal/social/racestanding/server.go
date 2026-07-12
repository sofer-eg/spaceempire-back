package racestanding

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/players"
)

// Middleware is the auth gate applied to the standings route.
type Middleware = func(http.Handler) http.Handler

// reputationReader reads a player's aggregate war/trade rating (ISP slice of
// *playersrepo.Repository). It backs the warRate/tradeRate fields so the
// shipyard can predictively gate module installs on all three reputation axes.
type reputationReader interface {
	GetReputation(ctx context.Context, player domain.PlayerID) (players.Reputation, error)
}

// Server exposes the per-player race-standing read endpoint over a Service.
type Server struct {
	svc        *Service
	reputation reputationReader
	logger     *slog.Logger
}

// NewServer constructs a Server. A nil logger falls back to slog.Default. A nil
// reputation reader is allowed (war/trade then always report 0) so the Server
// can be constructed without a DB in tests.
func NewServer(svc *Service, reputation reputationReader, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{svc: svc, reputation: reputation, logger: logger}
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
	// WarRate / TradeRate are the player's aggregate combat / trade ratings,
	// added so the shipyard can predictively gate installs on those axes too
	// (race gate comes from Items). Always present; default 0.
	WarRate   int `json:"warRate"`
	TradeRate int `json:"tradeRate"`
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
	warRate, tradeRate := s.readReputation(r.Context(), player)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(standingsResponse{
		Items:           out,
		WantedThreshold: s.svc.WantedThreshold(),
		WarRate:         warRate,
		TradeRate:       tradeRate,
	})
}

// readReputation resolves the player's war/trade ratings, defaulting to 0 when
// no reader is wired, the player has no row yet (ErrPlayerNotFound), or a read
// fails — the reputation panel must never 500 over a missing rating.
func (s *Server) readReputation(ctx context.Context, player domain.PlayerID) (war, trade int) {
	if s.reputation == nil {
		return 0, 0
	}
	rep, err := s.reputation.GetReputation(ctx, player)
	switch {
	case err == nil:
		return rep.War, rep.Trade
	case errors.Is(err, players.ErrPlayerNotFound):
		return 0, 0
	default:
		s.logger.Warn("race-standings: read reputation failed", "player", player, "err", err)
		return 0, 0
	}
}
