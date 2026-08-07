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
		s.writeExternalDockResult(w, res.Err)
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}

func (s *Server) writeExternalDockResult(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sector.ErrShipNotFound):
		writeError(w, http.StatusNotFound, "корабль не найден")
	case errors.Is(err, sector.ErrForbidden):
		writeError(w, http.StatusForbidden, "чужой корабль")
	case errors.Is(err, sector.ErrAlreadyDocked):
		writeError(w, http.StatusConflict, "корабль уже пристыкован")
	case errors.Is(err, sector.ErrEquipmentRequired):
		writeError(w, http.StatusBadRequest, "нужен модуль внешней стыковки (up_exdocking)")
	case errors.Is(err, sector.ErrTargetNotFound):
		writeError(w, http.StatusNotFound, "цель стыковки не найдена")
	case errors.Is(err, sector.ErrDockSelf):
		writeError(w, http.StatusBadRequest, "нельзя пристыковаться к самому себе")
	case errors.Is(err, sector.ErrTargetSectorMismatch):
		writeError(w, http.StatusBadRequest, "цель в другом секторе")
	case errors.Is(err, sector.ErrDockOutOfRange):
		writeError(w, http.StatusBadRequest, "слишком далеко для стыковки")
	case errors.Is(err, sector.ErrDockHostile):
		writeError(w, http.StatusForbidden, "корабль-носитель враждебен")
	case err != nil:
		s.writeInternalError(w, err)
	default:
		writeJSON(w, http.StatusOK, dto.ExternalDockResponse{OK: true})
	}
}
