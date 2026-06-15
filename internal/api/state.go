package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
)

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	snap := s.router.Snapshot(domain.SectorID(s.cfg.SectorID))
	ships := dto.ShipsFromDomain(snap.Ships, s.hullCategoryOf)
	dto.MarkNPC(ships, int64(s.npcPlayerID))
	out := dto.Snapshot{
		Type:     "snapshot",
		SectorID: int64(snap.SectorID),
		Tick:     snap.Tick,
		Ships:    ships,
	}
	if !snap.Statics.IsEmpty() {
		statics := dto.StaticsFromDomain(snap.Statics)
		out.Statics = &statics
	}
	if len(snap.Asteroids) > 0 {
		out.Asteroids = dto.AsteroidsFromDomain(snap.Asteroids)
	}
	writeJSON(w, http.StatusOK, out)
}
