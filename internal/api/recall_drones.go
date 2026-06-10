package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// handleRecallDrones removes every live drone owned by the player's ship
// and returns one drone cargo unit per recalled drone. The worker is the
// source of truth for how many are still alive, so cargo is credited only
// after the worker replies with the recalled count.
func (s *Server) handleRecallDrones(w http.ResponseWriter, r *http.Request) {
	if s.droneCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "drones not available")
		return
	}

	var req dto.RecallDronesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: req.ShipID}

	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.RecallDronesResult, 1)
	err := s.router.Send(sectorID, sector.RecallDronesCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Reply:    reply,
	})
	if err != nil {
		if errors.Is(err, sector.ErrInboxFull) {
			writeError(w, http.StatusServiceUnavailable, "sector busy")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AckTimeout)
	defer cancel()

	select {
	case res := <-reply:
		if res.Err != nil {
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "ship not found")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "ship belongs to another player")
			default:
				writeError(w, http.StatusInternalServerError, res.Err.Error())
			}
			return
		}
		// Credit one drone unit per recalled drone. A failure here would
		// lose the player a refund; logged inside refundDrones.
		s.refundDrones(r.Context(), shipRef, res.Recalled)
		writeJSON(w, http.StatusOK, dto.RecallDronesResponse{
			OK:       true,
			Recalled: res.Recalled,
		})
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
