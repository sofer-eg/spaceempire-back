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

// captureActionEnergyCost resolves the "action" energy a ship capture spends (phase
// 10.3.9.4) from the up_capture catalog row. A nil catalog or a row with no
// energy_usage yields 0, which disables the worker's energy gate. Mirrors
// hackActionEnergyCost.
func captureActionEnergyCost(cat EquipmentCatalog) int {
	if cat == nil {
		return 0
	}
	for _, e := range cat.AllEquipment() {
		if e.Type == "up_capture" {
			return e.EnergyUsage
		}
	}
	return 0
}

// handleCapture is the entry point for seizing a hostile ship with up_capture (SP
// DoCapture, TASK-100.3.9.4). It routes a CaptureShipCommand to the sector that
// owns the attacker ship and maps the worker's gate errors to HTTP: missing module
// / out of range / shield up / not enough energy → 422, a non-ship / self / missing
// target → 400, another player's ship → 403, a docked attacker → 400.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var req dto.CaptureRequest
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

	reply := make(chan sector.CaptureResult, 1)
	err := s.router.Send(currentSector, sector.CaptureShipCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Target: domain.EntityRef{
			Kind: domain.EntityKind(req.TargetRef.Kind),
			ID:   req.TargetRef.ID,
		},
		EnergyCost: s.captureEnergyCost,
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
		case errors.Is(res.Err, sector.ErrShipDocked):
			writeError(w, http.StatusBadRequest, "ship is docked")
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeError(w, http.StatusUnprocessableEntity, "ship has no capture module")
		case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
			writeError(w, http.StatusBadRequest, "invalid capture target")
		case errors.Is(res.Err, sector.ErrCaptureOutOfRange):
			writeError(w, http.StatusUnprocessableEntity, "capture target out of range")
		case errors.Is(res.Err, sector.ErrCaptureShielded):
			writeError(w, http.StatusUnprocessableEntity, "capture target shield is up")
		case errors.Is(res.Err, sector.ErrNotEnoughEnergy):
			writeError(w, http.StatusUnprocessableEntity, "not enough energy to capture")
		case writeIfTransient(w, res.Err, "capture could not be recorded, try again"):
			// The roll landed but the ownership transfer could not be persisted, so
			// the worker refused it (TASK-148).
		case res.Err != nil:
			writeError(w, http.StatusInternalServerError, res.Err.Error())
		default:
			writeJSON(w, http.StatusOK, dto.CaptureResponse{OK: true, Captured: res.Captured})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
