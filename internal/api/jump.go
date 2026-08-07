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
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	currentSector, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeError(w, http.StatusNotFound, "корабль не найден")
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
		writeError(w, http.StatusServiceUnavailable, "сектор занят")
		return
	}
	if err != nil {
		s.writeInternalError(w, r, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AckTimeout)
	defer cancel()

	select {
	case res := <-reply:
		switch {
		case errors.Is(res.Err, sector.ErrShipNotFound):
			writeError(w, http.StatusNotFound, "корабль не найден")
		case errors.Is(res.Err, sector.ErrForbidden):
			writeError(w, http.StatusForbidden, "чужой корабль")
		case errors.Is(res.Err, sector.ErrInvalidGate):
			writeError(w, http.StatusBadRequest, "эти ворота не из текущего сектора")
		case errors.Is(res.Err, sector.ErrGateOutOfRange):
			writeError(w, http.StatusBadRequest, "ворота слишком далеко")
		case errors.Is(res.Err, sector.ErrHandoffUnavailable):
			writeError(w, http.StatusServiceUnavailable, "передача сектора недоступна")
		case writeIfTransient(w, res.Err, "не удалось записать прыжок, попробуйте ещё раз"):
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.JumpResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
