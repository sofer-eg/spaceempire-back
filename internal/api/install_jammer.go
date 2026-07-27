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

// JammerCargo is the slice of cargo.Service the install handler needs.
// Declared here per ISP so handler tests can stub it without the full
// *cargo.Service surface.
type JammerCargo interface {
	Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	Refund(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// handleInstallJammer deploys one hyper-interference generator from the
// player's ship (TASK-131). Same orchestration as install-satellite: cargo
// lives in Postgres, the sector worker lives in RAM, so we
//  1. parse + validate,
//  2. atomically Consume one generator from the ship's cargo,
//  3. send InstallJammerCommand to the worker and wait for ack,
//  4. on worker rejection — Refund the cargo and propagate the error.
func (s *Server) handleInstallJammer(w http.ResponseWriter, r *http.Request) {
	if s.jammerCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "jammers not available")
		return
	}

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
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: req.ShipID}

	// Step 2: debit one generator up front. If the player has none we stop here.
	if err := s.jammerCargo.Consume(r.Context(), shipRef, JammerGoodsType, 1); err != nil {
		switch {
		case errors.Is(err, cargo.ErrInsufficientQuantity):
			writeError(w, http.StatusBadRequest, "no jammer in cargo")
		case errors.Is(err, cargo.ErrGoodsTypeNotFound):
			writeError(w, http.StatusInternalServerError, "jammer goods type missing")
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

	reply := make(chan sector.InstallJammerResult, 1)
	err := s.router.Send(sectorID, sector.InstallJammerCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Reply:    reply,
	})
	if err != nil {
		s.refundJammer(r.Context(), shipRef)
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
			s.refundJammer(r.Context(), shipRef)
			switch {
			case errors.Is(res.Err, sector.ErrShipNotFound):
				writeError(w, http.StatusNotFound, "ship not found")
			case errors.Is(res.Err, sector.ErrForbidden):
				writeError(w, http.StatusForbidden, "ship belongs to another player")
			case errors.Is(res.Err, sector.ErrShipDocked):
				writeError(w, http.StatusBadRequest, "ship is docked")
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
		// Best-effort refund — the worker may still apply the command later,
		// but we cannot tell from here. A duplicate refund beats a silent cargo
		// loss; the player retries.
		s.refundJammer(r.Context(), shipRef)
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// refundJammer reverses the Consume done at the start of the handler.
// Errors are logged because the HTTP response has already been chosen.
func (s *Server) refundJammer(ctx context.Context, owner domain.EntityRef) {
	if s.jammerCargo == nil {
		return
	}
	if err := s.jammerCargo.Refund(ctx, owner, JammerGoodsType, 1); err != nil {
		s.logger.Error("jammer refund failed", "err", err, "ship", owner.ID)
	}
}
