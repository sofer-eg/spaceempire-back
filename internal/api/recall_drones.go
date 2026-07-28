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

// DroneCargo is the slice of cargo.Service the recall handler needs: crediting
// recalled drones back to the ship's hold. Declared here per ISP so handler
// tests can stub it without the full *cargo.Service surface. The launch side owns
// no cargo — its debit lives in the worker's sector.Ordnance (TASK-147) — so
// Refund is the only operation left on the HTTP layer.
type DroneCargo interface {
	Refund(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// handleRecallDrones removes every live drone owned by the player's ship
// and returns one drone cargo unit per recalled drone. The worker is the
// source of truth for how many are still alive, so cargo is credited only
// after the worker replies with the recalled count.
func (s *Server) handleRecallDrones(w http.ResponseWriter, r *http.Request) {
	if s.droneCargo == nil {
		writeError(w, http.StatusServiceUnavailable, "drones not available")
		return
	}

	var req dto.RecallDronesRequest
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

	sectorID := domain.SectorID(s.cfg.SectorID)
	if sid, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID)); ok {
		sectorID = sid
	}

	reply := make(chan sector.RecallDronesResult, 1)
	err := s.router.Send(sectorID, sector.RecallDronesCommand{
		PlayerID: playerID,
		ShipID:   domain.ShipID(req.ShipID),
		Reply:    reply,
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
			default:
				writeError(w, http.StatusInternalServerError, res.Err.Error())
			}
			return
		}
		// Credit one drone unit per recalled drone. A failure here would
		// lose the player a refund; logged inside refundDrones.
		s.refundDrones(r.Context(), shipRef, res.Recalled)
		writeJSON(w, http.StatusOK, dto.RecallDronesResponse{
			OK:       true,
			Recalled: res.Recalled,
		})
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}

// refundDrones credits qty drone units back to the ship's hold. Errors are
// logged — the HTTP response has already been chosen.
func (s *Server) refundDrones(ctx context.Context, owner domain.EntityRef, qty int) {
	if s.droneCargo == nil || qty <= 0 {
		return
	}
	if err := s.droneCargo.Refund(ctx, owner, DroneGoodsType, int64(qty)); err != nil {
		s.logger.Error("drone refund failed", "err", err, "ship", owner.ID, "qty", qty)
	}
}
