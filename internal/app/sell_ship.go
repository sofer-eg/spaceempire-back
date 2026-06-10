package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// Trade-in guard sentinels (phase 10.14a). Returned by sellDecision and mapped
// to 409 by the handler.
var (
	errSellLastShip   = errors.New("sell: cannot sell the only ship")
	errSellActiveShip = errors.New("sell: cannot sell the active ship")
)

type sellShipRequest struct {
	ShipID int64 `json:"shipID"`
}

type sellShipResponse struct {
	OK   bool  `json:"ok"`
	Cash int64 `json:"cash"`
}

// sellDecision validates the trade-in guards and computes the credit. shipCount
// is how many ships the player owns; isActive reports whether the target is the
// ship the player currently controls. Pure so the guards are unit-testable
// without a DB.
func sellDecision(basePrice int64, ratio float64, shipCount int, isActive bool) (int64, error) {
	if shipCount <= 1 {
		return 0, errSellLastShip
	}
	if isActive {
		return 0, errSellActiveShip
	}
	credit := int64(float64(basePrice) * ratio)
	if credit < 0 {
		credit = 0
	}
	return credit, nil
}

// handleSellShip sells a player's ship docked at this shipyard for a fraction
// of its class base price (phase 10.14a). Guards: the ship must be owned and
// docked here, must not be the only ship, and must not be the active one
// (switch active first). Credit + row delete run in one transaction; the worker
// RAM copy is then evicted.
func (s *outfitServer) handleSellShip(w http.ResponseWriter, r *http.Request) {
	player, shipyardID, ok := s.authAndShipyard(w, r)
	if !ok {
		return
	}
	var req sellShipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}

	target, ok := s.ownedDockedShip(w, player, shipyardID, domain.ShipID(req.ShipID))
	if !ok {
		return
	}
	cls, ok := s.classes.GetShipClass(target.ShipClassID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "у корабля нет класса")
		return
	}

	fleet := s.pool.ShipsByPlayer(player)
	isActive := s.isActiveShip(r.Context(), player, target.ID)
	credit, err := sellDecision(cls.BasePrice, s.cfg.TradeInRatio, len(fleet), isActive)
	switch {
	case errors.Is(err, errSellLastShip):
		writeJSONError(w, http.StatusConflict, "нельзя продать единственный корабль")
		return
	case errors.Is(err, errSellActiveShip):
		writeJSONError(w, http.StatusConflict, "нельзя продать активный корабль — сначала сделайте активным другой")
		return
	}

	var newCash int64
	err = s.tx.Do(r.Context(), func(ctx context.Context, txx pgx.Tx) error {
		cash, e := s.players.WithExecutor(txx).AdjustCash(ctx, player, credit)
		if e != nil {
			return e
		}
		newCash = cash
		return s.ships.WithExecutor(txx).Delete(ctx, target.ID)
	})
	if err != nil {
		s.logger.Error("sell-ship: tx", "err", err, "player", int64(player), "ship", req.ShipID)
		writeJSONError(w, http.StatusInternalServerError, "внутренняя ошибка")
		return
	}

	// Evict from the worker's RAM. RemoveShipCommand also issues repo.Delete —
	// a harmless no-op now that the row is already gone.
	if _, sectorID, found := s.pool.LookupPrimaryShipByPlayer(player); found {
		s.mirrorRemoveShip(sectorID, target.ID)
	}
	writeJSON(w, sellShipResponse{OK: true, Cash: newCash})
}

// isActiveShip reports whether shipID is the ship the player currently
// controls: the explicit active_ship_id when set, else the lowest-id ship.
func (s *outfitServer) isActiveShip(ctx context.Context, player domain.PlayerID, shipID domain.ShipID) bool {
	if id, ok, err := s.players.ActiveShip(ctx, player); err == nil && ok {
		return id == shipID
	}
	if id, _, ok := s.pool.LookupPrimaryShipByPlayer(player); ok {
		return id == shipID
	}
	return false
}

// mirrorRemoveShip drops the sold ship from the worker's RAM state.
func (s *outfitServer) mirrorRemoveShip(sectorID domain.SectorID, shipID domain.ShipID) {
	reply := make(chan sector.CmdResult, 1)
	if err := s.pool.Send(sectorID, sector.RemoveShipCommand{ShipID: shipID, Reply: reply}); err != nil {
		s.logger.Error("sell-ship: mirror remove", "err", err, "ship", int64(shipID), "sector", int64(sectorID))
		return
	}
	select {
	case <-reply:
	case <-time.After(s.cfg.AckTimeout):
	}
}
