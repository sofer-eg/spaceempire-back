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

// DroneGoodsType is the goods_type id consumed per launched drone
// (seeded by migration 0018, see drones.md §2).
const DroneGoodsType domain.GoodsTypeID = 51

// DroneCargo is the slice of cargo.Service the drone handlers need.
// Declared here per ISP so handler tests can stub it without the full
// *cargo.Service surface.
type DroneCargo interface {
	Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	Refund(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// handleLaunchDrone is the phase 4.4 entry point for launching N combat
// drones from the player's ship at a target. Same orchestration shape as
// launch-missile: debit cargo up front, send the worker command, refund
// on rejection. Because a launch can spawn fewer drones than requested
// (a mid-loop DB failure), the worker reports how many it actually
// spawned and the handler refunds the shortfall (Count - Spawned).
func (s *Server) handleLaunchDrone(w http.ResponseWriter, r *http.Request) {
	if s.droneCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "drones not available")
		return
	}

	var req dto.LaunchDroneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.TargetRef.ID <= 0 || req.Count <= 0 {
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

	// Debit all requested drones up front.
	if err := s.droneCargo.Consume(r.Context(), shipRef, DroneGoodsType, int64(req.Count)); err != nil {
		switch {
		case errors.Is(err, cargo.ErrInsufficientQuantity):
			writeError(w, http.StatusBadRequest, "not enough drones in cargo")
		case errors.Is(err, cargo.ErrGoodsTypeNotFound):
			writeError(w, http.StatusInternalServerError, "drone goods type missing")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.LaunchDroneResult, 1)
	err := s.router.Send(sectorID, sector.LaunchDroneCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Target:   target,
		Count:    req.Count,
		Reply:    reply,
	})
	if err != nil {
		s.refundDrones(r.Context(), shipRef, req.Count)
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
			s.refundDrones(r.Context(), shipRef, req.Count)
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "ship not found")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "ship belongs to another player")
			case errors.Is(res.Err, sector.ErrShipDocked):
				writeError(w, http.StatusBadRequest, "ship is docked")
			case errors.Is(res.Err, sector.ErrEquipmentRequired):
				writeError(w, http.StatusUnprocessableEntity, "ship has no drone control module")
			case errors.Is(res.Err, sector.ErrDroneCapReached):
				writeError(w, http.StatusUnprocessableEntity, "drone control capacity reached")
			case errors.Is(res.Err, sector.ErrInvalidAttackTarget):
				writeError(w, http.StatusBadRequest, "invalid drone target")
			default:
				writeError(w, http.StatusInternalServerError, res.Err.Error())
			}
			return
		}
		if shortfall := req.Count - res.Spawned; shortfall > 0 {
			s.refundDrones(r.Context(), shipRef, shortfall)
		}
		writeJSON(w, http.StatusOK, dto.LaunchDroneResponse{
			OK:      true,
			Spawned: res.Spawned,
		})
	case <-ctx.Done():
		s.refundDrones(r.Context(), shipRef, req.Count)
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// refundDrones reverses (part of) the Consume done at the start of the
// handler. Errors are logged — the HTTP response has already been chosen.
func (s *Server) refundDrones(ctx context.Context, owner domain.EntityRef, qty int) {
	if s.droneCargo == nil || qty <= 0 {
		return
	}
	if err := s.droneCargo.Refund(ctx, owner, DroneGoodsType, int64(qty)); err != nil {
		s.logger.Error("drone refund failed", "err", err, "ship", owner.ID, "qty", qty)
	}
}
