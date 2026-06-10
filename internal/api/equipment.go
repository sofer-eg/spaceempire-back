package api

import (
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
)

// EquipmentCatalog is the slice of *balance.Equipments the equipment endpoint
// needs. Declared per ISP so handler tests can stub it.
type EquipmentCatalog interface {
	AllEquipment() []balance.Equipment
}

// handleEquipment returns the static ship-equipment catalog (ct_updates) used
// by the (future) outfitting screen on the shipyard/dock. Read-only, open (no
// auth) — reference data baked into the binary (phase 8.16).
func (s *Server) handleEquipment(w http.ResponseWriter, _ *http.Request) {
	if s.equipment == nil {
		writeError(w, http.StatusServiceUnavailable, "equipment catalog not available")
		return
	}
	all := s.equipment.AllEquipment()
	items := make([]dto.Equipment, 0, len(all))
	for _, e := range all {
		items = append(items, dto.Equipment{
			ID:            int64(e.ID),
			Type:          e.Type,
			Description:   e.Description,
			MaxLevel:      e.MaxLevel,
			Race:          e.Race,
			ShipClass:     e.ShipClass,
			Price:         e.Price,
			PricePerLevel: e.PricePerLevel,
			IsBase:        e.IsBase,
			Position:      e.Position,
			Dependance:    e.Dependance,
			EnergyUseType: e.EnergyUseType,
			EnergyUsage:   e.EnergyUsage,
		})
	}
	writeJSON(w, http.StatusOK, dto.EquipmentListResponse{Items: items})
}
