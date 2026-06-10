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

// MissileCargo is the slice of cargo.Service the launch handler needs.
// Declared here per ISP so handler tests can stub it without dragging in
// the full *cargo.Service surface.
type MissileCargo interface {
	Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	Refund(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// handleLaunchMissile is the phase 4.3 entry point for firing one
// missile from the player's ship at a target. The handler is the
// orchestrator between two non-cooperating substrates: cargo lives in
// Postgres (so the launch debit is a real DB transaction), and the
// sector worker lives in RAM (so the actual missile lifecycle is
// in-memory). To keep them mostly consistent we:
//  1. parse + validate the request,
//  2. atomically Consume one missile from the ship's cargo,
//  3. send LaunchMissileCommand to the sector worker and wait for ack,
//  4. on worker rejection — Refund the cargo and propagate the error.
//
// A crash between (2) and (3) leaves the cargo decremented without a
// missile in flight. That is acceptable for phase 4.3: the player
// pays the same as the original SP (which had the same race surface in
// `FireMissileAt`) and the magnitude is one missile.
func (s *Server) handleLaunchMissile(w http.ResponseWriter, r *http.Request) {
	if s.missileCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "missiles not available")
		return
	}

	var req dto.LaunchMissileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	targetKind := domain.EntityKind(req.TargetRef.Kind)
	if targetKind != domain.EntityKindShip {
		writeError(w, http.StatusBadRequest, "invalid target kind")
		return
	}
	if req.TargetRef.ID == req.ShipID {
		writeError(w, http.StatusBadRequest, "cannot target self")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: req.ShipID}
	target := domain.EntityRef{Kind: targetKind, ID: req.TargetRef.ID}

	// Step 2: debit one missile up front. If the player has none we stop
	// here — no need to bother the worker.
	if err := s.missileCargo.Consume(r.Context(), shipRef, MissileGoodsType, 1); err != nil {
		switch {
		case errors.Is(err, cargo.ErrInsufficientQuantity):
			writeError(w, http.StatusBadRequest, "no missile in cargo")
		case errors.Is(err, cargo.ErrGoodsTypeNotFound):
			writeError(w, http.StatusInternalServerError, "missile goods type missing")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Step 3: route to the sector that currently owns the ship; fall back
	// to the configured default sector for callers that bypassed the
	// router (legacy tests).
	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.LaunchMissileResult, 1)
	err := s.router.Send(sectorID, sector.LaunchMissileCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Target:   target,
		Reply:    reply,
	})
	if err != nil {
		s.refundMissile(r.Context(), shipRef)
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
			s.refundMissile(r.Context(), shipRef)
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "ship not found")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "ship belongs to another player")
			case errors.Is(res.Err, sector.ErrShipDocked):
				writeError(w, http.StatusBadRequest, "ship is docked")
			case errors.Is(res.Err, sector.ErrEquipmentRequired):
				writeError(w, http.StatusUnprocessableEntity, "ship has no missile launcher")
			case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
				writeError(w, http.StatusBadRequest, "invalid missile target")
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
		// Best-effort refund — the worker may still apply the command
		// later, but we cannot tell from here. A duplicate refund is
		// preferable to a silent cargo loss; the player will fire again.
		s.refundMissile(r.Context(), shipRef)
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// refundMissile reverses the Consume done at the start of the handler.
// Errors are logged because there is no caller-level recovery path —
// the HTTP response has already been chosen.
func (s *Server) refundMissile(ctx context.Context, owner domain.EntityRef) {
	if s.missileCargo == nil {
		return
	}
	if err := s.missileCargo.Refund(ctx, owner, MissileGoodsType, 1); err != nil {
		s.logger.Error("missile refund failed",
			"err", err, "ship", owner.ID)
	}
}
