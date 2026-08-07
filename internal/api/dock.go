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

func (s *Server) handleDock(w http.ResponseWriter, r *http.Request) {
	var req dto.DockRequest
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
	err := s.router.Send(currentSector, sector.DockCommand{
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
		case errors.Is(res.Err, sector.ErrAlreadyDocked):
			writeError(w, http.StatusConflict, "корабль уже пристыкован")
		case errors.Is(res.Err, sector.ErrTargetSectorMismatch):
			writeError(w, http.StatusBadRequest, "цель в другом секторе")
		case errors.Is(res.Err, sector.ErrDockOutOfRange):
			writeError(w, http.StatusBadRequest, "слишком далеко для стыковки")
		case errors.Is(res.Err, sector.ErrTargetNotFound):
			writeError(w, http.StatusNotFound, "цель стыковки не найдена")
		case errors.Is(res.Err, sector.ErrInvalidDockTarget):
			writeError(w, http.StatusBadRequest, "к этому объекту нельзя пристыковаться")
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.DockResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}

func (s *Server) handleUndock(w http.ResponseWriter, r *http.Request) {
	var req dto.UndockRequest
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
	err := s.router.Send(currentSector, sector.UndockCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
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
		case errors.Is(res.Err, sector.ErrNotDocked):
			writeError(w, http.StatusConflict, "корабль не пристыкован")
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.UndockResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
