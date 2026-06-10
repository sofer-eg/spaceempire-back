package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// ShipClassCatalog is the slice of *balance.ShipClasses the ship-class
// endpoint needs. Declared per ISP so handler tests can stub it.
type ShipClassCatalog interface {
	AllShipClasses() []balance.ShipClass
}

// buildHullCategoryIndex precomputes the ship-class id → hull-category map
// (phase 10.13) from the catalog so the per-tick snapshot path is a plain map
// lookup instead of scanning the class list. Returns nil when the catalog is
// absent (tests / minimal deployments) — hullCategoryOf then yields "".
func buildHullCategoryIndex(catalog ShipClassCatalog) map[domain.ShipClassID]string {
	if catalog == nil {
		return nil
	}
	classes := catalog.AllShipClasses()
	out := make(map[domain.ShipClassID]string, len(classes))
	for _, c := range classes {
		out[c.ID] = string(c.Category())
	}
	return out
}

// hullCategoryOf resolves a ship's class id to its hull-shape category for the
// WS/state snapshot DTO (phase 10.13). 0 / unknown id / nil index → "" so the
// client falls back to its maxSpeed heuristic. Safe on a nil map.
func (s *Server) hullCategoryOf(id domain.ShipClassID) string {
	return s.hullCategories[id]
}

// handleShipClasses returns the static ship-class catalog (ct_ship_classes)
// used by the SPA to label ships and, later, the shipyard buy screen.
// Read-only, open (no auth) — reference data.
func (s *Server) handleShipClasses(w http.ResponseWriter, _ *http.Request) {
	if s.shipClasses == nil {
		writeError(w, http.StatusServiceUnavailable, "ship class catalog not available")
		return
	}
	all := s.shipClasses.AllShipClasses()
	items := make([]dto.ShipClass, 0, len(all))
	for _, c := range all {
		cat := c.Category()
		items = append(items, dto.ShipClass{
			ID:            int64(c.ID),
			Race:          c.Race,
			Type:          c.Type,
			Class:         c.Class,
			Category:      string(cat),
			CategoryLabel: balance.CategoryLabel(cat),
			Name:          c.Name,
			Speed:         c.Speed,
			Acceleration:  c.Acceleration,
			Laser:         c.Laser,
			Shield:        c.Shield,
			Hull:          c.Hull,
			CargoBay:      c.CargoBay,
			BasePrice:     c.BasePrice,
			PilotCabin:    c.PilotCabin,
		})
	}
	writeJSON(w, http.StatusOK, dto.ShipClassListResponse{Items: items})
}
