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

// Torpedo ammunition goods types (migration 0042). A class-2 launch burns one
// "Огненная Буря" (gt23); a class-3 launch burns one "Святая Торпеда" (gt24).
// The torpedo object's balance profile is selected from the class inside the
// sector worker; here the class only picks the cargo row to debit.
const (
	TorpedoFirestormGoodsType domain.GoodsTypeID = 23 // gt23, class 2
	TorpedoHolyGoodsType      domain.GoodsTypeID = 24 // gt24, class 3
)

// torpedoGoodsType maps a launch class to the goods type its ammunition is
// stored as. Only classes 2 and 3 exist; any other value is rejected by the
// handler with 400.
func torpedoGoodsType(class int) (domain.GoodsTypeID, bool) {
	switch class {
	case 2:
		return TorpedoFirestormGoodsType, true
	case 3:
		return TorpedoHolyGoodsType, true
	}
	return 0, false
}

// torpedoLaunchEnergyCost resolves the "action" energy a torpedo launch spends
// (phase 10.3.1) from the up_torpedo_launcher catalog row, mirroring
// launchActionEnergyCost for up_launcher. energy_usage is uniform across the
// per-class launcher rows, so the first match is representative. A nil catalog
// or a launcher with no energy_usage yields 0, which disables the worker's
// energy gate.
func torpedoLaunchEnergyCost(cat EquipmentCatalog) int {
	if cat == nil {
		return 0
	}
	for _, e := range cat.AllEquipment() {
		if e.Type == "up_torpedo_launcher" {
			return e.EnergyUsage
		}
	}
	return 0
}

// handleLaunchTorpedo fires one torpedo from the player's ship at a target
// (ЧТЗ doc-1 §5.2). The handler is a pure orchestrator — it owns no cargo:
//  1. parse + validate and resolve the class's goods type,
//  2. send LaunchTorpedoCommand (carrying that goods id) to the worker,
//  3. wait for ack and map the outcome.
//
// The ammunition debit lives inside the worker's apply, in the same transaction
// as the torpedo row (TASK-147). That is what makes a lost ack safe: before, the
// handler consumed up front and refunded on timeout while the worker still
// applied the command — the torpedo flew and the ammunition came back.
func (s *Server) handleLaunchTorpedo(w http.ResponseWriter, r *http.Request) {
	var req dto.LaunchTorpedoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 {
		writeError(w, http.StatusBadRequest, "некорректные поля запроса")
		return
	}
	goodsType, ok := torpedoGoodsType(req.Class)
	if !ok {
		writeError(w, http.StatusBadRequest, "недопустимый класс торпеды")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	target := domain.EntityRef{Kind: domain.EntityKind(req.TargetRef.Kind), ID: req.TargetRef.ID}

	// Route to the sector that currently owns the ship; fall back to the
	// configured default sector for callers that bypassed the router.
	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.LaunchTorpedoResult, 1)
	err := s.router.Send(sectorID, sector.LaunchTorpedoCommand{
		PlayerID:   playerID,
		ShipID:     domain.ShipID(req.ShipID),
		Target:     target,
		Class:      req.Class,
		GoodsType:  goodsType,
		EnergyCost: s.torpedoEnergyCost,
		Reply:      reply,
	})
	if err != nil {
		if errors.Is(err, sector.ErrInboxFull) {
			writeError(w, http.StatusServiceUnavailable, "сектор занят")
			return
		}
		s.writeInternalError(w, r, err)
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
				writeError(w, http.StatusUnprocessableEntity, "на корабле нет торпедного аппарата")
			case errors.Is(res.Err, sector.ErrNotEnoughEnergy):
				writeError(w, http.StatusUnprocessableEntity, "не хватает энергии для запуска")
			case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
				writeError(w, http.StatusBadRequest, "недопустимая цель для торпеды")
			case errors.Is(res.Err, cargo.ErrInsufficientQuantity):
				writeError(w, http.StatusBadRequest, "в трюме нет торпед")
			case errors.Is(res.Err, cargo.ErrGoodsTypeNotFound):
				writeError(w, http.StatusInternalServerError, "в каталоге товаров нет торпеды")
			case errors.Is(res.Err, sector.ErrOrdnanceUnavailable):
				writeError(w, http.StatusServiceUnavailable, "запуск недоступен: ошибка конфигурации сервера")
			default:
				s.writeInternalError(w, r, res.Err)
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.LaunchTorpedoResponse{
			OK:        true,
			TorpedoID: int64(res.TorpedoID),
		})
	case <-ctx.Done():
		// No compensation to run: the debit and the torpedo row commit together
		// inside the worker, so ammunition and torpedo agree either way. 504 means
		// "outcome unknown".
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
