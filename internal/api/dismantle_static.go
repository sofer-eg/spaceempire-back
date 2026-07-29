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

// handleDismantleStatic takes one of the player's deployed objects back into the
// ship's hold (TASK-146). Like the install handlers it owns no cargo: the goods
// credit and the row delete commit together inside the worker's StaticInstaller,
// so a lost ack cannot remove an object without paying for it.
//
// The handler owns the goods catalog, so it resolves which good one unit of the
// target is worth and rejects any other kind before the command is sent — the
// sector package only knows the id it was handed.
func (s *Server) handleDismantleStatic(w http.ResponseWriter, r *http.Request) {
	var req dto.DismantleStaticRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.Target.ID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}

	kind := domain.EntityKind(req.Target.Kind)
	gtype, ok := dismantleGoodsType(kind)
	if !ok {
		writeError(w, http.StatusBadRequest, "object cannot be dismantled")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(sectorID, sector.DismantleStaticCommand{
		PlayerID:  playerID,
		ShipID:    domain.ShipID(req.ShipID),
		Target:    domain.EntityRef{Kind: kind, ID: req.Target.ID},
		GoodsType: gtype,
		Reply:     reply,
	})
	if err != nil {
		if errors.Is(err, sector.ErrInboxFull) {
			writeError(w, http.StatusServiceUnavailable, "sector busy")
			return
		}
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
		case errors.Is(res.Err, sector.ErrDeployedNotFound):
			writeError(w, http.StatusNotFound, "object not found")
		case errors.Is(res.Err, sector.ErrForbidden):
			writeError(w, http.StatusForbidden, "object belongs to another player")
		case errors.Is(res.Err, sector.ErrShipDocked):
			writeError(w, http.StatusBadRequest, "ship is docked")
		case errors.Is(res.Err, sector.ErrNotDismantlable):
			writeError(w, http.StatusBadRequest, "object cannot be dismantled")
		case errors.Is(res.Err, sector.ErrDeployedOutOfRange):
			writeError(w, http.StatusUnprocessableEntity, "object out of range")
		case errors.Is(res.Err, cargo.ErrNoSpace):
			writeError(w, http.StatusUnprocessableEntity, "no room in cargo")
		case errors.Is(res.Err, cargo.ErrGoodsTypeNotFound):
			writeError(w, http.StatusInternalServerError, "goods type missing")
		case errors.Is(res.Err, sector.ErrInstallerUnavailable):
			// Misconfiguration, not a player error: without the transactional
			// installer the worker refuses to remove an object it cannot pay for.
			writeError(w, http.StatusServiceUnavailable, "dismantle unavailable: server misconfigured")
		case res.Err != nil:
			writeError(w, http.StatusInternalServerError, res.Err.Error())
		default:
			writeJSON(w, http.StatusOK, dto.DismantleStaticResponse{OK: true})
		}
	case <-ctx.Done():
		// No compensation to run: the credit and the delete commit together inside
		// the worker, so hold and object agree either way. 504 means "outcome
		// unknown" — the player checks the radar and their hold.
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// dismantleGoodsType maps a deployed object's kind to the goods id one unit of it
// is worth. ok is false for anything that is not player-deployed equipment.
func dismantleGoodsType(k domain.EntityKind) (domain.GoodsTypeID, bool) {
	switch k {
	case domain.EntityKindJammer:
		return JammerGoodsType, true
	case domain.EntityKindSatellite:
		return SatelliteGoodsType, true
	default:
		return 0, false
	}
}
