package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// DroneGoodsType is the goods_type id consumed per launched drone: «Боевой дрон»
// (space 290), from the legacy schema's own mapping ct_drones.cargo_id (class 1 →
// 21) — the same table torpedos (23/24), the satellite (26) and the jammer (27)
// already come from. Until TASK-167 it was 51, an English-named duplicate
// migration 0018 minted with space 2; no station sold it, so a spent magazine was
// unrefillable. See drones.md §2.
const DroneGoodsType = domain.DroneGoodsType

// handleLaunchDrone launches a salvo of combat drones from the player's ship at
// a target (phase 4.4). Same orchestration shape as launch-missile: the handler
// owns no cargo, it only routes the command (carrying the goods id) and maps the
// outcome.
//
// The salvo's size AND its debit live inside the worker's apply, through
// sector.Ordnance (TASK-147, TASK-176): the worker clamps to what up_drone_control
// still allows, the transaction clamps again to what the hold carries, and exactly
// that many units are charged in the same transaction as the INSERTs. So Spawned can
// no longer come back short of what was paid for, and the shortfall refund this
// handler used to do is gone with it.
//
// The request carries no count — see dto.LaunchDroneRequest.
func (s *Server) handleLaunchDrone(w http.ResponseWriter, r *http.Request) {
	var req dto.LaunchDroneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 {
		writeError(w, http.StatusBadRequest, "некорректные поля запроса")
		return
	}
	targetKind := domain.EntityKind(req.TargetRef.Kind)
	if targetKind != domain.EntityKindShip {
		writeError(w, http.StatusBadRequest, "недопустимый тип цели")
		return
	}
	if req.TargetRef.ID == req.ShipID {
		writeError(w, http.StatusBadRequest, "нельзя выбрать целью свой корабль")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	target := domain.EntityRef{Kind: targetKind, ID: req.TargetRef.ID}

	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.LaunchDroneResult, 1)
	// No Count: zero means "the whole salvo up_drone_control allows" (TASK-176).
	err := s.router.Send(sectorID, sector.LaunchDroneCommand{
		PlayerID:  playerID,
		ShipID:    domain.ShipID(req.ShipID),
		Target:    target,
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
			case errors.Is(res.Err, sector.ErrShipDocked):
				writeError(w, http.StatusBadRequest, "корабль пристыкован")
			case errors.Is(res.Err, sector.ErrEquipmentRequired):
				writeError(w, http.StatusUnprocessableEntity, "на корабле нет модуля управления дронами")
			case errors.Is(res.Err, sector.ErrDroneCapReached):
				writeError(w, http.StatusUnprocessableEntity, "достигнут предел управляемых дронов")
			case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
				writeError(w, http.StatusBadRequest, "недопустимая цель для дронов")
			case errors.Is(res.Err, cargo.ErrInsufficientQuantity):
				writeError(w, http.StatusBadRequest, "в трюме не хватает дронов")
			case errors.Is(res.Err, cargo.ErrGoodsTypeNotFound):
				writeError(w, http.StatusInternalServerError, "в каталоге товаров нет дрона")
			case errors.Is(res.Err, sector.ErrOrdnanceUnavailable):
				writeError(w, http.StatusServiceUnavailable, "запуск недоступен: ошибка конфигурации сервера")
			default:
				s.writeInternalError(w, res.Err)
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.LaunchDroneResponse{
			OK:      true,
			Spawned: res.Spawned,
		})
	case <-ctx.Done():
		// No compensation to run: the salvo's debit and its INSERTs commit together
		// inside the worker, so ammunition and drones agree either way. 504 means
		// "outcome unknown".
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
