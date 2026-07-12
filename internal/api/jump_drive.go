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

// handleJumpDrive is the TASK-100.3.7 entry point for the seamless (gateless)
// jump: it routes a JumpDriveCommand to the worker owning the ship's current
// sector and waits for the ack. All the mechanics (module gate, shield drain,
// cooldown, forbidden-sector, relocation) live in the worker; the handler only
// maps its sentinel errors to HTTP status codes. Mirrors handleJump.
func (s *Server) handleJumpDrive(w http.ResponseWriter, r *http.Request) {
	var req dto.JumpDriveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	currentSector, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeError(w, http.StatusNotFound, "ship not found")
		return
	}

	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(currentSector, sector.JumpDriveCommand{
		PlayerID:       playerID,
		ShipID:         domain.ShipID(req.ShipID),
		TargetSectorID: domain.SectorID(req.TargetSectorID),
		Reply:          reply,
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
			writeError(w, http.StatusConflict, "ship is docked")
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeError(w, http.StatusUnprocessableEntity, "ship has no jump drive")
		case errors.Is(res.Err, sector.ErrShieldRequired):
			writeError(w, http.StatusUnprocessableEntity, "shield generator damaged or missing")
		case errors.Is(res.Err, sector.ErrJumpOnCooldown):
			writeError(w, http.StatusTooManyRequests, "jump drive not ready")
		case errors.Is(res.Err, sector.ErrJumpForbiddenSector):
			writeError(w, http.StatusBadRequest, "jump blocked in this sector")
		case errors.Is(res.Err, sector.ErrInvalidSector):
			writeError(w, http.StatusBadRequest, "invalid target sector")
		case errors.Is(res.Err, sector.ErrHandoffUnavailable):
			writeError(w, http.StatusServiceUnavailable, "handoff unavailable")
		case res.Err != nil:
			writeError(w, http.StatusInternalServerError, res.Err.Error())
		default:
			writeJSON(w, http.StatusOK, dto.JumpDriveResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
