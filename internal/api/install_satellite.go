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

// SatelliteGoodsType is the goods_type id consumed by one navigation-satellite
// install (phase 10.15). "Навигационный спутник" in configs/balance.yaml.
const SatelliteGoodsType domain.GoodsTypeID = 26

// handleInstallSatellite deploys one navigation satellite from the player's
// ship (phase 10.15). The handler is a pure orchestrator — it owns no cargo:
//  1. parse + validate,
//  2. send InstallSatelliteCommand (carrying the goods id) to the worker,
//  3. wait for ack and map the outcome.
//
// The goods debit lives inside the worker's apply, in the same transaction as
// the satellite INSERT (TASK-144) — see handleInstallJammer for why the old
// Consume-then-Refund orchestration was unsafe on a lost ack.
func (s *Server) handleInstallSatellite(w http.ResponseWriter, r *http.Request) {
	var req dto.InstallSatelliteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	if req.ShipID <= 0 {
		writeError(w, http.StatusBadRequest, "некорректные поля запроса")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	// Route to the sector that currently owns the ship; fall back to the
	// configured default sector for callers that bypassed the router.
	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.InstallSatelliteResult, 1)
	err := s.router.Send(sectorID, sector.InstallSatelliteCommand{
		PlayerID:  playerID,
		ShipID:    domain.ShipID(req.ShipID),
		GoodsType: SatelliteGoodsType,
		Reply:     reply,
	})
	if err != nil {
		if errors.Is(err, sector.ErrInboxFull) {
			writeErrorCode(w, http.StatusServiceUnavailable, CodeSectorBusy, "сектор занят")
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
				writeErrorCode(w, http.StatusBadRequest, CodeShipDocked, "корабль пристыкован")
			case errors.Is(res.Err, cargo.ErrInsufficientQuantity):
				writeErrorCode(w, http.StatusBadRequest, CodeCargoInsufficient, "в трюме нет спутников")
			case errors.Is(res.Err, cargo.ErrGoodsTypeNotFound):
				writeError(w, http.StatusInternalServerError, "в каталоге товаров нет спутника")
			case errors.Is(res.Err, sector.ErrInstallerUnavailable):
				// Misconfiguration, not a player error — see install_jammer.go.
				writeError(w, http.StatusServiceUnavailable, "установка недоступна: ошибка конфигурации сервера")
			default:
				s.writeInternalError(w, res.Err)
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.InstallSatelliteResponse{
			OK:          true,
			SatelliteID: int64(res.SatelliteID),
		})
	case <-ctx.Done():
		// No compensation to run: the debit and the INSERT commit together
		// inside the worker, so whether the command has already applied or is
		// still queued, goods and satellite agree. 504 means "outcome unknown".
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
