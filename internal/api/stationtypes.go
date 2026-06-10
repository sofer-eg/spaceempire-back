package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
)

// StationTypeCatalog is the slice of *balance.StationTypes the station-type
// endpoint needs. Declared per ISP so handler tests can stub it.
type StationTypeCatalog interface {
	AllStationTypes() []balance.StationType
}

// handleStationTypes returns the static station-type catalog (station_types)
// used by the SPA to show a docked station's human-readable type name.
// Read-only, open (no auth) — reference data baked into the binary.
func (s *Server) handleStationTypes(w http.ResponseWriter, _ *http.Request) {
	if s.stationTypes == nil {
		writeError(w, http.StatusServiceUnavailable, "station type catalog not available")
		return
	}
	all := s.stationTypes.AllStationTypes()
	items := make([]dto.StationType, 0, len(all))
	for _, t := range all {
		items = append(items, dto.StationType{
			ID:        t.ID,
			Name:      t.Name,
			Race:      t.RaceID,
			Kind:      int(t.Kind),
			KindLabel: t.Kind.Label(),
			Sellable:  t.Sellable,
		})
	}
	writeJSON(w, http.StatusOK, dto.StationTypeListResponse{Items: items})
}
