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

// handleMine arms (or stops) sustained ore mining on a player's ship (phase
// 10.3.6). It only sets the intent — the per-tick drilling, energy gate and ore
// deposit run in the sector worker. A zero AsteroidID is a stop request. The
// drill gate (up_drill), range check and asteroid lookup are enforced by the
// worker; this handler routes the command and maps the worker's errors to HTTP.
func (s *Server) handleMine(w http.ResponseWriter, r *http.Request) {
	var req dto.MineRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	if req.ShipID <= 0 {
		writeError(w, http.StatusBadRequest, "некорректные поля запроса")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	currentSector, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeError(w, http.StatusNotFound, "корабль не найден")
		return
	}

	var asteroid *domain.AsteroidID
	if req.AsteroidID > 0 {
		id := domain.AsteroidID(req.AsteroidID)
		asteroid = &id
	}

	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(currentSector, sector.MineCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Asteroid: asteroid,
		Reply:    reply,
	})
	if errors.Is(err, sector.ErrInboxFull) {
		writeError(w, http.StatusServiceUnavailable, "сектор занят")
		return
	}
	if err != nil {
		s.writeInternalError(w, err)
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
		case errors.Is(res.Err, sector.ErrShipDocked):
			writeError(w, http.StatusBadRequest, "корабль пристыкован")
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeError(w, http.StatusUnprocessableEntity, "на корабле нет бура")
		case errors.Is(res.Err, sector.ErrAsteroidNotFound):
			writeError(w, http.StatusNotFound, "астероид не найден")
		case errors.Is(res.Err, sector.ErrAsteroidOutOfRange):
			writeError(w, http.StatusBadRequest, "астероид слишком далеко")
		case res.Err != nil:
			s.writeInternalError(w, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.MineResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
