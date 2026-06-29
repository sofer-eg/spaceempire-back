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
// sector worker (sub-task TASK-100.3.5.4); here the class only picks the cargo
// row to debit.
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

// TorpedoCargo is the slice of cargo.Service the torpedo launch handler needs.
// Declared here per ISP so handler tests can stub it without dragging in the
// full *cargo.Service surface.
type TorpedoCargo interface {
	Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	Refund(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// handleLaunchTorpedo fires one torpedo from the player's ship at a target
// (ЧТЗ doc-1 §5.2). Same orchestration as launch-missile: the ammunition lives
// in Postgres (so the debit is a real DB transaction) and the torpedo lifecycle
// lives in the sector worker's RAM. The handler:
//  1. parses + validates the request and resolves the class's goods type,
//  2. atomically Consumes one unit of that goods type from the ship's cargo,
//  3. sends LaunchTorpedoCommand to the sector worker and waits for the ack,
//  4. on worker rejection (4xx/422) — Refunds the ammunition and propagates
//     the error.
//
// A crash between (2) and (3) leaves the cargo decremented without a torpedo in
// flight — the same race surface as launch-missile, acceptable at this scale.
func (s *Server) handleLaunchTorpedo(w http.ResponseWriter, r *http.Request) {
	if s.torpedoCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "torpedoes not available")
		return
	}

	var req dto.LaunchTorpedoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	goodsType, ok := torpedoGoodsType(req.Class)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid torpedo class")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: req.ShipID}
	target := domain.EntityRef{Kind: domain.EntityKind(req.TargetRef.Kind), ID: req.TargetRef.ID}

	// Step 2: debit one torpedo of the chosen class up front. If the player has
	// none we stop here — no need to bother the worker.
	if err := s.torpedoCargo.Consume(r.Context(), shipRef, goodsType, 1); err != nil {
		switch {
		case errors.Is(err, cargo.ErrInsufficientQuantity):
			writeError(w, http.StatusBadRequest, "no torpedo in cargo")
		case errors.Is(err, cargo.ErrGoodsTypeNotFound):
			writeError(w, http.StatusInternalServerError, "torpedo goods type missing")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Step 3: route to the sector that currently owns the ship; fall back to
	// the configured default sector for callers that bypassed the router.
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
		EnergyCost: s.torpedoEnergyCost,
		Reply:      reply,
	})
	if err != nil {
		s.refundTorpedo(r.Context(), shipRef, goodsType)
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
			s.refundTorpedo(r.Context(), shipRef, goodsType)
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "ship not found")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "ship belongs to another player")
			case errors.Is(res.Err, sector.ErrShipDocked):
				writeError(w, http.StatusBadRequest, "ship is docked")
			case errors.Is(res.Err, sector.ErrEquipmentRequired):
				writeError(w, http.StatusUnprocessableEntity, "ship has no torpedo launcher")
			case errors.Is(res.Err, sector.ErrNotEnoughEnergy):
				writeError(w, http.StatusUnprocessableEntity, "not enough energy to launch")
			case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
				writeError(w, http.StatusBadRequest, "invalid torpedo target")
			default:
				writeError(w, http.StatusInternalServerError, res.Err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, dto.LaunchTorpedoResponse{
			OK:        true,
			TorpedoID: int64(res.TorpedoID),
		})
	case <-ctx.Done():
		// Best-effort refund — the worker may still apply the command later,
		// but a duplicate refund is preferable to a silent cargo loss.
		s.refundTorpedo(r.Context(), shipRef, goodsType)
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// refundTorpedo reverses the Consume done at the start of the handler. Errors
// are logged because there is no caller-level recovery path — the HTTP response
// has already been chosen.
func (s *Server) refundTorpedo(ctx context.Context, owner domain.EntityRef, goodsType domain.GoodsTypeID) {
	if s.torpedoCargo == nil {
		return
	}
	if err := s.torpedoCargo.Refund(ctx, owner, goodsType, 1); err != nil {
		s.logger.Error("torpedo refund failed", "err", err, "ship", owner.ID)
	}
}
