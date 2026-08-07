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

func (s *Server) handleSetCourse(w http.ResponseWriter, r *http.Request) {
	if s.pathRouter == nil {
		writeError(w, http.StatusServiceUnavailable, "автопилот недоступен")
		return
	}
	var req dto.SetCourseRequest
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

	hops, ok := s.pathRouter.Hops(currentSector, domain.SectorID(req.SectorID))
	if !ok {
		writeError(w, http.StatusBadRequest, "маршрут до цели не найден")
		return
	}

	course := domain.Course{
		Sector: domain.SectorID(req.SectorID),
		Pos:    domain.Vec2{X: req.X, Y: req.Y},
	}
	if req.Approach != nil {
		course.Approach = &domain.EntityRef{
			Kind: domain.EntityKind(req.Approach.Kind),
			ID:   req.Approach.ID,
		}
	}
	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(currentSector, sector.SetCourseCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Course:   &course,
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
		case errors.Is(res.Err, sector.ErrShipDocked):
			writeError(w, http.StatusConflict, "корабль пристыкован")
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeError(w, http.StatusUnprocessableEntity, "нужен модуль автопилота")
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.SetCourseResponse{Hops: hops})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
