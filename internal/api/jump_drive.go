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
	err := s.router.Send(currentSector, sector.JumpDriveCommand{
		PlayerID:       playerID,
		ShipID:         domain.ShipID(req.ShipID),
		TargetSectorID: domain.SectorID(req.TargetSectorID),
		Reply:          reply,
	})
	if errors.Is(err, sector.ErrInboxFull) {
		writeErrorCode(w, http.StatusServiceUnavailable, CodeSectorBusy, "сектор занят")
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
			writeErrorCode(w, http.StatusConflict, CodeShipDocked, "корабль пристыкован")
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeErrorCode(w, http.StatusUnprocessableEntity, CodeJumpDriveRequired, "на корабле нет прыжкового двигателя (up_jump_drive)")
		case errors.Is(res.Err, sector.ErrShieldRequired):
			writeErrorCode(w, http.StatusUnprocessableEntity, CodeShieldRequired, "генератор щита повреждён или отсутствует")
		case errors.Is(res.Err, sector.ErrJumpOnCooldown):
			writeError(w, http.StatusTooManyRequests, "прыжковый двигатель ещё не готов")
		case errors.Is(res.Err, sector.ErrJumpForbiddenSector):
			writeErrorCode(w, http.StatusBadRequest, CodeJumpForbiddenSector, "прыжок из этого сектора запрещён")
		case errors.Is(res.Err, sector.ErrJumpBlockedByAntijump):
			writeErrorCode(w, http.StatusConflict, CodeJumpBlockedAntijump, "прыжок глушат гипер-помехи")
		case errors.Is(res.Err, sector.ErrInvalidSector):
			writeErrorCode(w, http.StatusBadRequest, CodeInvalidSector, "недопустимый сектор назначения")
		case errors.Is(res.Err, sector.ErrHandoffUnavailable):
			writeError(w, http.StatusServiceUnavailable, "передача сектора недоступна")
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.JumpDriveResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
