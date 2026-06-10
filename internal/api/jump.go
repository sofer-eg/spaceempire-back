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

func (s *Server) handleJump(w http.ResponseWriter, r *http.Request) {
	var req dto.JumpRequest
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
	err := s.router.Send(currentSector, sector.JumpCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		GateID:   domain.GateID(req.GateID),
		Reply:    reply,
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
		switch {
		case errors.Is(res.Err, sector.ErrShipNotFound):
			writeError(w, http.StatusNotFound, "ship not found")
		case errors.Is(res.Err, sector.ErrForbidden):
			writeError(w, http.StatusForbidden, "ship belongs to another player")
		case errors.Is(res.Err, sector.ErrInvalidGate):
			writeError(w, http.StatusBadRequest, "invalid gate for current sector")
		case errors.Is(res.Err, sector.ErrGateOutOfRange):
			writeError(w, http.StatusBadRequest, "out of gate range")
		case errors.Is(res.Err, sector.ErrHandoffUnavailable):
			writeError(w, http.StatusServiceUnavailable, "handoff unavailable")
		case res.Err != nil:
			writeError(w, http.StatusInternalServerError, res.Err.Error())
		default:
			writeJSON(w, http.StatusOK, dto.JumpResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
