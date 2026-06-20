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

// handleTransport teleports cargo between two of the player's ships in the same
// sector (phase 10.3.18, up_transporter). It routes a TransportCargoCommand to
// the receiver's sector and maps the worker's validation errors to HTTP. The
// equipment gate, ownership, proximity and energy checks live in the worker.
func (s *Server) handleTransport(w http.ResponseWriter, r *http.Request) {
	var req dto.TransportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.SourceShipID <= 0 || req.GoodsType <= 0 || req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	if req.ShipID == req.SourceShipID {
		writeError(w, http.StatusBadRequest, "source and destination must differ")
		return
	}

	playerID, _ := auth.PlayerIDFromContext(r.Context())

	currentSector, ok := s.router.LookupShipSector(domain.ShipID(req.ShipID))
	if !ok {
		writeError(w, http.StatusNotFound, "ship not found")
		return
	}

	reply := make(chan sector.CmdResult, 1)
	err := s.router.Send(currentSector, sector.TransportCargoCommand{
		PlayerID:     playerID,
		ShipID:       domain.ShipID(req.ShipID),
		SourceShipID: domain.ShipID(req.SourceShipID),
		GoodsType:    domain.GoodsTypeID(req.GoodsType),
		Quantity:     req.Quantity,
		Reply:        reply,
	})
	if errors.Is(err, sector.ErrInboxFull) {
		writeError(w, http.StatusServiceUnavailable, "sector busy")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.AckTimeout)
	defer cancel()
	select {
	case res := <-reply:
		switch {
		case errors.Is(res.Err, sector.ErrShipNotFound):
			writeError(w, http.StatusNotFound, "ship not found in this sector")
		case errors.Is(res.Err, sector.ErrForbidden):
			writeError(w, http.StatusForbidden, "ship belongs to another player")
		case errors.Is(res.Err, sector.ErrEquipmentRequired):
			writeError(w, http.StatusUnprocessableEntity, "ship has no transporter")
		case errors.Is(res.Err, sector.ErrTransporterOutOfRange):
			writeError(w, http.StatusUnprocessableEntity, "source ship out of transporter range")
		case errors.Is(res.Err, sector.ErrNotEnoughEnergy):
			writeError(w, http.StatusUnprocessableEntity, "not enough energy for the teleport")
		case res.Err != nil:
			writeError(w, http.StatusInternalServerError, res.Err.Error())
		default:
			writeJSON(w, http.StatusOK, dto.TransportResponse{OK: true})
		}
	case <-ctx.Done():
		writeError(w, http.StatusGatewayTimeout, "command timeout")
	}
}
