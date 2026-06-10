package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
)

// handleFleet lists every ship the authenticated player owns across all
// sectors (phase 10.14a). The SPA's fleet panel renders the list and offers
// "make active" (activate) per ship; the snapshot Ship shape carries enough
// (name/class/sector/docked/isSpacesuit) to label each row.
func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if s.fleet == nil {
		writeError(w, http.StatusServiceUnavailable, "fleet listing unavailable")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())
	ships := s.fleet.ShipsByPlayer(playerID)
	writeJSON(w, http.StatusOK, dto.FleetResponse{
		Ships: dto.ShipsFromDomain(ships, s.hullCategoryOf),
	})
}
