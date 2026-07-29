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

// hackActionEnergyCost resolves the "action" energy a station hack spends (phase
// 10.3.9.3) from the up_hack catalog row. A nil catalog or a row with no
// energy_usage yields 0, which disables the worker's energy gate. Mirrors
// launchActionEnergyCost.
func hackActionEnergyCost(cat EquipmentCatalog) int {
	if cat == nil {
		return 0
	}
	for _, e := range cat.AllEquipment() {
		if e.Type == "up_hack" {
			return e.EnergyUsage
		}
	}
	return 0
}

// handleHack is the entry point for raiding a trade station with up_hack (SP
// UseHack, TASK-100.3.9.3). It routes a HackStationCommand to the sector that
// owns the hacker ship and maps the worker's gate errors to HTTP: missing
// module / out of range / not enough energy / too little goods → 422, a
// non-trade or missing target → 400, another player's ship → 403.
func (s *Server) handleHack(w http.ResponseWriter, r *http.Request) {
	var req dto.HackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())

	currentSector, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeError(w, http.StatusNotFound, "ship not found")
		return
	}

	reply := make(chan sector.HackResult, 1)
	err := s.router.Send(currentSector, sector.HackStationCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Target: domain.EntityRef{
			Kind: domain.EntityKind(req.TargetRef.Kind),
			ID:   req.TargetRef.ID,
		},
		EnergyCost: s.hackEnergyCost,
		Reply:      reply,
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
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeError(w, http.StatusUnprocessableEntity, "ship has no hack module")
		case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
			writeError(w, http.StatusBadRequest, "invalid hack target")
		case errors.Is(res.Err, sector.ErrHackOutOfRange):
			writeError(w, http.StatusUnprocessableEntity, "trade station out of range")
		case errors.Is(res.Err, sector.ErrNotEnoughEnergy):
			writeError(w, http.StatusUnprocessableEntity, "not enough energy to hack")
		case errors.Is(res.Err, sector.ErrHackTooLittleGoods):
			writeError(w, http.StatusUnprocessableEntity, "station has too little goods to hack")
		case writeIfTransient(w, res.Err, "hack could not be recorded, try again"):
		case res.Err != nil:
			writeError(w, http.StatusInternalServerError, res.Err.Error())
		default:
			writeJSON(w, http.StatusOK, dto.HackResponse{OK: true, Robbed: res.Robbed})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
