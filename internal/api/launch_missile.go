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

// MissileGoodsType is the goods_type id consumed by every missile launch.
// Mirrors `app.MissileGoodsType` (seeded by migration 0017).
const MissileGoodsType domain.GoodsTypeID = 50

// launchActionEnergyCost resolves the "action" energy a missile launch spends
// (phase 10.3.1) from the up_launcher catalog row. energy_usage is uniform
// across the per-class launcher rows, so the first match is representative. A
// nil catalog or a launcher with no energy_usage yields 0, which disables the
// worker's energy gate.
func launchActionEnergyCost(cat EquipmentCatalog) int {
	if cat == nil {
		return 0
	}
	for _, e := range cat.AllEquipment() {
		if e.Type == "up_launcher" {
			return e.EnergyUsage
		}
	}
	return 0
}

// handleLaunchMissile fires one missile from the player's ship at a target
// (phase 4.3). The handler is a pure orchestrator — it owns no cargo:
//  1. parse + validate,
//  2. send LaunchMissileCommand (carrying the goods id) to the worker,
//  3. wait for ack and map the outcome.
//
// The ammunition debit lives inside the worker's apply, through sector.Ordnance
// (TASK-147). That is what makes a lost ack safe: before, the handler consumed up
// front and refunded on timeout while the worker still applied the command — the
// missile flew and the player got their ammunition back, repeatably.
func (s *Server) handleLaunchMissile(w http.ResponseWriter, r *http.Request) {
	var req dto.LaunchMissileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	// TASK-113 FR-06 / TASK-110 / TASK-111: a missile may strike a ship (not
	// itself; a spacesuit is a ships row, so EVA rides this branch), any
	// destructible static — gates included — or a loot container. The set comes from
	// sector.IsMissileTargetKind, the same predicate the worker enforces; other kinds
	// are rejected here, before the command is built. The self-target guard only
	// applies to ship targets — a static and a ship may share a numeric id
	// (separate id spaces).
	targetKind := domain.EntityKind(req.TargetRef.Kind)
	switch {
	case targetKind == domain.EntityKindShip:
		if req.TargetRef.ID == req.ShipID {
			writeError(w, http.StatusBadRequest, "cannot target self")
			return
		}
	case sector.IsMissileTargetKind(targetKind):
		// ok — the worker resolves the target's liveness/position.
	default:
		writeError(w, http.StatusBadRequest, "invalid target kind")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	target := domain.EntityRef{Kind: targetKind, ID: req.TargetRef.ID}

	// Route to the sector that currently owns the ship; fall back to the
	// configured default sector for callers that bypassed the router (legacy
	// tests).
	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.LaunchMissileResult, 1)
	err := s.router.Send(sectorID, sector.LaunchMissileCommand{
		PlayerID:   playerID,
		ShipID:     domain.ShipID(req.ShipID),
		Target:     target,
		GoodsType:  MissileGoodsType,
		EnergyCost: s.launchEnergyCost,
		Reply:      reply,
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
			case errors.Is(res.Err, sector.ErrEquipmentRequired):
				writeError(w, http.StatusUnprocessableEntity, "ship has no missile launcher")
			case errors.Is(res.Err, sector.ErrNotEnoughEnergy):
				writeError(w, http.StatusUnprocessableEntity, "not enough energy to launch")
			case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
				writeError(w, http.StatusBadRequest, "invalid missile target")
			case errors.Is(res.Err, cargo.ErrInsufficientQuantity):
				writeError(w, http.StatusBadRequest, "no missile in cargo")
			case errors.Is(res.Err, cargo.ErrGoodsTypeNotFound):
				writeError(w, http.StatusInternalServerError, "missile goods type missing")
			case errors.Is(res.Err, sector.ErrOrdnanceUnavailable):
				// Misconfiguration, not a player error: the worker has no
				// transactional ordnance, so it refuses to fire rather than launch
				// for free. 503 — retrying may work after a fix.
				writeError(w, http.StatusServiceUnavailable, "launch unavailable: server misconfigured")
			default:
				writeError(w, http.StatusInternalServerError, res.Err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.LaunchMissileResponse{
			OK:        true,
			MissileID: int64(res.MissileID),
		})
	case <-ctx.Done():
		// No compensation to run: the debit happens inside the worker together
		// with the launch, so whether the command has already applied or is still
		// queued, ammunition and missile agree. 504 means "outcome unknown" — the
		// player checks their hold and retries if nothing was fired.
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
