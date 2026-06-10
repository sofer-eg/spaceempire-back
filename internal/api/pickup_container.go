package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/containers"
	"spaceempire/back/internal/sector"
)

// handlePickupContainer is the phase 4.6 entry point for scooping a loot
// container into the player's ship. The cargo move + container delete is
// transactional in the sector worker's container repo; the handler only
// routes the command and maps the result. No up-front cargo accounting
// (unlike launch-drone) — the worker owns the whole transfer.
func (s *Server) handlePickupContainer(w http.ResponseWriter, r *http.Request) {
	var req dto.PickupContainerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.ContainerID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(sectorID, sector.PickupContainerCommand{
		PlayerID:    playerID,
		ShipID:      domain.ShipID(req.ShipID),
		ContainerID: domain.ContainerID(req.ContainerID),
		Reply:       reply,
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
			writePickupError(w, res.Err)
			return
		}
		writeJSON(w, http.StatusOK, dto.PickupContainerResponse{OK: true})
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

func writePickupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sector.ErrShipNotFound):
		writeError(w, http.StatusNotFound, "ship not found")
	case errors.Is(err, sector.ErrForbidden):
		writeError(w, http.StatusForbidden, "ship belongs to another player")
	case errors.Is(err, sector.ErrContainerNotFound):
		writeError(w, http.StatusNotFound, "container not found")
	case errors.Is(err, sector.ErrContainerOutOfRange):
		writeError(w, http.StatusBadRequest, "container out of range")
	case errors.Is(err, containers.ErrNoSpace):
		writeError(w, http.StatusConflict, "not enough cargo space")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
