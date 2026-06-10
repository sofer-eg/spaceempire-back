package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// shipReplacer is the slice of *shipSpawner the shipyard handler needs (ISP).
type shipReplacer interface {
	SpawnStarterAt(ctx context.Context, player domain.PlayerID, sectorID domain.SectorID, pos domain.Vec2) error
}

// shipyardLocator is the slice of *sector.Pool the handler needs.
type shipyardLocator interface {
	LookupPrimaryShipByPlayer(player domain.PlayerID) (domain.ShipID, domain.SectorID, bool)
	Snapshot(sectorID domain.SectorID) sector.Snapshot
	Send(sectorID domain.SectorID, cmd sector.Command) error
	// ShipsByPlayer lists the player's ships across sectors (10.14a trade-in
	// "last ship" guard).
	ShipsByPlayer(player domain.PlayerID) []domain.Ship
}

// shipyardServer serves the spacesuit → new-ship recovery at a shipyard (phase
// 10.2): a player whose ship is a spacesuit docked at a shipyard exchanges it
// for a fresh starter ship at the same spot. Free for now (ship-purchase
// pricing is a follow-up).
type shipyardServer struct {
	pool    shipyardLocator
	spawner shipReplacer
	logger  *slog.Logger
}

func newShipyardServer(pool shipyardLocator, spawner shipReplacer, logger *slog.Logger) *shipyardServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &shipyardServer{pool: pool, spawner: spawner, logger: logger}
}

// RegisterRoutes mounts the shipyard endpoint behind the auth middleware.
//
//	POST /api/shipyard/{id}/get-ship
func (s *shipyardServer) RegisterRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler) {
	mux.Handle("POST /api/shipyard/{id}/get-ship", authMW(http.HandlerFunc(s.handleGetShip)))
}

func (s *shipyardServer) handleGetShip(w http.ResponseWriter, r *http.Request) {
	player, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, `{"error":"invalid shipyard id"}`, http.StatusBadRequest)
		return
	}

	shipID, sectorID, ok := s.pool.LookupPrimaryShipByPlayer(player)
	if !ok {
		http.Error(w, `{"error":"no active ship"}`, http.StatusConflict)
		return
	}
	var suit *domain.Ship
	snap := s.pool.Snapshot(sectorID)
	for i := range snap.Ships {
		if snap.Ships[i].ID == shipID {
			suit = &snap.Ships[i]
			break
		}
	}
	switch {
	case suit == nil:
		http.Error(w, `{"error":"ship not found"}`, http.StatusConflict)
		return
	case !suit.IsSpacesuit:
		http.Error(w, `{"error":"available only in a spacesuit"}`, http.StatusConflict)
		return
	case suit.Docked == nil || suit.Docked.Kind != domain.EntityKindShipyard || suit.Docked.ID != id:
		http.Error(w, `{"error":"dock at this shipyard first"}`, http.StatusConflict)
		return
	}

	// New ship at the suit's spot (the shipyard, same sector — the player's WS
	// stays put), then remove the suit.
	if err := s.spawner.SpawnStarterAt(r.Context(), player, sectorID, suit.Pos); err != nil {
		s.logger.Error("shipyard: spawn starter", "err", err, "player", int64(player), "shipyard", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sectorID, sector.RemoveShipCommand{ShipID: shipID, Reply: reply}); err != nil {
		s.logger.Error("shipyard: remove suit", "err", err, "player", int64(player), "ship", int64(shipID))
	} else {
		select {
		case <-reply:
		case <-time.After(time.Second):
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
