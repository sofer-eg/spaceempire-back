package api

import (
	"context"
	"errors"
	"net/http"

	"encoding/json"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// SatelliteGoodsType is the goods_type id consumed by one navigation-satellite
// install (phase 10.15). "Навигационный спутник" in configs/balance.yaml.
const SatelliteGoodsType domain.GoodsTypeID = 26

// SatelliteCargo is the slice of cargo.Service the install handler needs.
// Declared here per ISP so handler tests can stub it without the full
// *cargo.Service surface.
type SatelliteCargo interface {
	Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	Refund(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// handleInstallSatellite deploys one navigation satellite from the player's
// ship (phase 10.15). Same orchestration as launch-missile: cargo lives in
// Postgres, the sector worker lives in RAM, so we
//  1. parse + validate,
//  2. atomically Consume one satellite from the ship's cargo,
//  3. send InstallSatelliteCommand to the worker and wait for ack,
//  4. on worker rejection — Refund the cargo and propagate the error.
func (s *Server) handleInstallSatellite(w http.ResponseWriter, r *http.Request) {
	if s.satelliteCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "satellites not available")
		return
	}

	var req dto.InstallSatelliteRequest
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

	// Step 2: debit one satellite up front. If the player has none we stop here.
	if err := s.satelliteCargo.Consume(r.Context(), shipRef, SatelliteGoodsType, 1); err != nil {
		switch {
		case errors.Is(err, cargo.ErrInsufficientQuantity):
			writeError(w, http.StatusBadRequest, "no satellite in cargo")
		case errors.Is(err, cargo.ErrGoodsTypeNotFound):
			writeError(w, http.StatusInternalServerError, "satellite goods type missing")
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

	reply := make(chan sector.InstallSatelliteResult, 1)
	err := s.router.Send(sectorID, sector.InstallSatelliteCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Reply:    reply,
	})
	if err != nil {
		s.refundSatellite(r.Context(), shipRef)
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
			s.refundSatellite(r.Context(), shipRef)
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
		writeJSON(w, http.StatusOK, dto.InstallSatelliteResponse{
			OK:          true,
			SatelliteID: int64(res.SatelliteID),
		})
	case <-ctx.Done():
		// Best-effort refund — the worker may still apply the command later,
		// but we cannot tell from here. A duplicate refund beats a silent cargo
		// loss; the player retries.
		s.refundSatellite(r.Context(), shipRef)
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// refundSatellite reverses the Consume done at the start of the handler.
// Errors are logged because the HTTP response has already been chosen.
func (s *Server) refundSatellite(ctx context.Context, owner domain.EntityRef) {
	if s.satelliteCargo == nil {
		return
	}
	if err := s.satelliteCargo.Refund(ctx, owner, SatelliteGoodsType, 1); err != nil {
		s.logger.Error("satellite refund failed", "err", err, "ship", owner.ID)
	}
}
