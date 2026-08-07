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

// handleAttack arms the laser tick: sets AttackTarget on the given
// ship. PlayerID is enforced via session — only the ship's owner can
// fire. The kind set is the worker's (another ship, or a shoot-downable
// projectile since TASK-112); anything else comes back as
// ErrInvalidAttackTarget → 400.
func (s *Server) handleAttack(w http.ResponseWriter, r *http.Request) {
	var req dto.AttackRequest
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
	err := s.router.Send(currentSector, sector.AttackCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Target: domain.EntityRef{
			Kind: domain.EntityKind(req.TargetRef.Kind),
			ID:   req.TargetRef.ID,
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
		case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
			writeError(w, http.StatusBadRequest, "недопустимая цель для атаки")
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.AttackResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}

// handleCeaseFire clears AttackTarget. Idempotent — clearing an empty
// target is success.
func (s *Server) handleCeaseFire(w http.ResponseWriter, r *http.Request) {
	var req dto.CeaseFireRequest
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
	err := s.router.Send(currentSector, sector.CeaseFireCommand{
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
		case res.Err != nil:
			s.writeInternalError(w, r, res.Err)
		default:
			writeJSON(w, http.StatusOK, dto.CeaseFireResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "таймаут команды")
	}
}
