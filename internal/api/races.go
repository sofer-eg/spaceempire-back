package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/reference/race"
)

// handleRaces returns the static race reference (id, name, state, colour) the
// SPA uses to colour objects/sectors and label owners. Read-only, open (no
// auth) — pure reference data baked into the binary (phase 8.13).
func (s *Server) handleRaces(w http.ResponseWriter, _ *http.Request) {
	all := race.All()
	items := make([]dto.Race, 0, len(all))
	for _, d := range all {
		items = append(items, dto.Race{
			ID:        int(d.ID),
			Name:      d.Name,
			StateName: d.StateName,
			Color:     d.Color,
		})
	}
	writeJSON(w, http.StatusOK, dto.RaceListResponse{Items: items})
}
