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

func (s *Server) handleExternalDock(w http.ResponseWriter, r *http.Request) {
	var req dto.ExternalDockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	currentSector, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeError(w, http.StatusNotFound, "ship not found")
		return
	}

	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(currentSector, sector.ExternalDockCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Target: domain.EntityRef{
			Kind: domain.EntityKind(req.Target.Kind),
			ID:   req.Target.ID,
		},
		Reply: reply,
	})
	if errors.Is(err, sector.ErrInboxFull) {
		writeError(w, http.StatusServiceUnavailable, "sector busy")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AckTimeout)
	defer cancel()

	select {
	case res := <-reply:
		writeExternalDockResult(w, res.Err)
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

func writeExternalDockResult(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sector.ErrShipNotFound):
		writeError(w, http.StatusNotFound, "ship not found")
	case errors.Is(err, sector.ErrForbidden):
		writeError(w, http.StatusForbidden, "ship belongs to another player")
	case errors.Is(err, sector.ErrAlreadyDocked):
		writeError(w, http.StatusConflict, "already docked")
	case errors.Is(err, sector.ErrEquipmentRequired):
		writeError(w, http.StatusBadRequest, "up_exdocking module required")
	case errors.Is(err, sector.ErrTargetNotFound):
		writeError(w, http.StatusNotFound, "dock target not found")
	case errors.Is(err, sector.ErrDockSelf):
		writeError(w, http.StatusBadRequest, "cannot dock to self")
	case errors.Is(err, sector.ErrTargetSectorMismatch):
		writeError(w, http.StatusBadRequest, "target in different sector")
	case errors.Is(err, sector.ErrDockOutOfRange):
		writeError(w, http.StatusBadRequest, "out of dock range")
	case errors.Is(err, sector.ErrDockHostile):
		writeError(w, http.StatusForbidden, "host ship is hostile")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, dto.ExternalDockResponse{OK: true})
	}
}
