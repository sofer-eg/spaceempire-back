package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// handleActivateShip switches the player's active ship (phase 10.14a). It
// validates ownership against the worker snapshot, persists active_ship_id, and
// publishes a PlayerHandoffEvent so the WS follows the ship into its sector
// (a same-sector switch is a no-op on the WS side — the SPA picks the new ship
// up from refreshPlayer). The fleet/EVA flows build on this single switch.
func (s *Server) handleActivateShip(w http.ResponseWriter, r *http.Request) {
	if s.activeShipWriter == nil {
		writeError(w, http.StatusServiceUnavailable, "active-ship switching unavailable")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid ship id")
		return
	}
	shipID := domain.ShipID(id)
	playerID, _ := auth.PlayerIDFromContext(r.Context())

	sec, ok := s.router.LookupShipSector(shipID)
	if !ok {
		writeError(w, http.StatusNotFound, "ship not found")
		return
	}
	// Ownership: the ship must exist in the snapshot and belong to the caller.
	owned := false
	for _, ship := range s.router.Snapshot(sec).Ships {
		if ship.ID == shipID {
			if ship.PlayerID != playerID {
				writeError(w, http.StatusForbidden, "ship belongs to another player")
				return
			}
			owned = true
			break
		}
	}
	if !owned {
		// Raced with a despawn between LookupShipSector and Snapshot.
		writeError(w, http.StatusNotFound, "ship not found")
		return
	}

	if err := s.activeShipWriter.SetActiveShip(r.Context(), playerID, shipID); err != nil {
		s.logger.Error("activate ship: persist", "err", err, "player", int64(playerID), "ship", int64(shipID))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if s.handoffPublisher != nil {
		payload, err := json.Marshal(sector.PlayerHandoffEvent{
			PlayerID:     playerID,
			ShipID:       shipID,
			TargetSector: sec,
		})
		if err != nil {
			s.logger.Warn("activate ship: marshal handoff", "err", err)
		} else if err := s.handoffPublisher.Publish(r.Context(), sector.PlayerHandoffTopic(playerID), payload); err != nil {
			// Non-fatal: active_ship_id is already persisted. The client follows
			// on the next reconnect even if the live move did not land.
			s.logger.Warn("activate ship: publish handoff", "err", err, "player", int64(playerID))
		}
	}

	writeJSON(w, http.StatusOK, dto.ActivateResponse{OK: true})
}
