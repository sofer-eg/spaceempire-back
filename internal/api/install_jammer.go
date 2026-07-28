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

// JammerGoodsType is the goods_type id consumed by one hyper-interference
// generator install (TASK-131). "Генератор гипер-помех" in configs/balance.yaml
// (SP ct_drones class 7 cargo_id 27).
const JammerGoodsType domain.GoodsTypeID = 27

// handleInstallJammer deploys one hyper-interference generator from the
// player's ship (TASK-131). The handler is a pure orchestrator — it owns no
// cargo:
//  1. parse + validate,
//  2. send InstallJammerCommand (carrying the goods id) to the worker,
//  3. wait for ack and map the outcome.
//
// The goods debit lives inside the worker's apply, in the same transaction as
// the generator INSERT (TASK-144). That is what makes a lost ack safe: before,
// the handler consumed up front and refunded on timeout while the worker still
// applied the command, handing out a ≈1.13M cr generator for free.
func (s *Server) handleInstallJammer(w http.ResponseWriter, r *http.Request) {
	var req dto.InstallJammerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	// Route to the sector that currently owns the ship; fall back to the
	// configured default sector for callers that bypassed the router.
	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.InstallJammerResult, 1)
	err := s.router.Send(sectorID, sector.InstallJammerCommand{
		PlayerID:  playerID,
		ShipID:    domain.ShipID(req.ShipID),
		GoodsType: JammerGoodsType,
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
		if res.Err != nil {
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "ship not found")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "ship belongs to another player")
			case errors.Is(res.Err, sector.ErrShipDocked):
				writeError(w, http.StatusBadRequest, "ship is docked")
			case errors.Is(res.Err, cargo.ErrInsufficientQuantity):
				writeError(w, http.StatusBadRequest, "no jammer in cargo")
			case errors.Is(res.Err, cargo.ErrGoodsTypeNotFound):
				writeError(w, http.StatusInternalServerError, "jammer goods type missing")
			case errors.Is(res.Err, sector.ErrInstallerUnavailable):
				// Misconfiguration, not a player error: the worker has no
				// transactional installer, so it refuses to deploy rather than
				// build one for free. 503 — retrying may work after a fix.
				writeError(w, http.StatusServiceUnavailable, "install unavailable: server misconfigured")
			default:
				writeError(w, http.StatusInternalServerError, res.Err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.InstallJammerResponse{
			OK:       true,
			JammerID: int64(res.JammerID),
		})
	case <-ctx.Done():
		// No compensation to run: the debit and the INSERT commit together
		// inside the worker, so whether the command has already applied or is
		// still queued, goods and generator agree. 504 means "outcome unknown" —
		// the player checks their hold and retries if nothing was deployed.
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
