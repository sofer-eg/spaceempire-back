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

// handleRecallDrones returns as many of the player's live drones to the hold as it
// can take, one drone cargo unit each, and reports how many stayed out (TASK-156).
// Since TASK-152 the handler owns no cargo: the deletes and the credit commit
// together inside the worker's sector.Ordnance, so the handler only routes the
// command (carrying the goods id) and maps the outcome — the same shape
// launch-drone took in TASK-147.
func (s *Server) handleRecallDrones(w http.ResponseWriter, r *http.Request) {
	var req dto.RecallDronesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	if req.ShipID <= 0 {
		writeError(w, http.StatusBadRequest, "некорректные поля запроса")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.RecallDronesResult, 1)
	err := s.router.Send(sectorID, sector.RecallDronesCommand{
		PlayerID:  playerID,
		ShipID:    domain.ShipID(req.ShipID),
		GoodsType: DroneGoodsType,
		Reply:     reply,
	})
	if err != nil {
		if errors.Is(err, sector.ErrInboxFull) {
			writeError(w, http.StatusServiceUnavailable, "сектор занят")
			return
		}
		s.writeInternalError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AckTimeout)
	defer cancel()

	select {
	case res := <-reply:
		if res.Err != nil {
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "корабль не найден")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "чужой корабль")
			case errors.Is(res.Err, sector.ErrOrdnanceUnavailable):
				writeError(w, http.StatusServiceUnavailable, "возврат дронов недоступен: ошибка конфигурации сервера")
			default:
				s.writeInternalError(w, res.Err)
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.RecallDronesResponse{
			OK:       true,
			Recalled: res.Recalled,
			Left:     res.Left,
		})
	case <-ctx.Done():
		// No compensation to run: the drone DELETEs and the cargo credit commit
		// together inside the worker, so drones and hold agree either way. 504
		// means "outcome unknown".
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
