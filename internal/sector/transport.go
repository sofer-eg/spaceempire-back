package sector

import (
	"context"
	"errors"

	"spaceempire/back/internal/domain"
)

// transporterModuleType is the ct_updates key of the cargo-teleport module.
const transporterModuleType = "up_transporter"

// ErrTransporterOutOfRange is reported by TransportCargoCommand when the source
// ship is farther than cfg.TransporterRange from the up_transporter ship.
var ErrTransporterOutOfRange = errors.New("sector: transporter source out of range")

// TransportCargoCommand teleports cargo between two ships the player owns in the
// same sector, with no docking (phase 10.3.18, X-Tension teleport device). The
// receiving ship (ShipID) must carry an up_transporter module; the source
// (SourceShipID) must be the player's own ship within cfg.TransporterRange. The
// teleport spends cfg.TransporterEnergyCost "action" energy from the receiver.
//
// Design notes (decisions for the open forks in the task):
//   - Direction: source ship -> transporter ship (consolidate to your fitted
//     ship). The receiver carries the module and pays the energy.
//   - Mass/volume: bounded by the destination's free cargo space — the haul
//     moves min(available, Quantity, room) (cargo.Service.Move semantics), so an
//     over-large request simply moves what fits rather than failing.
//   - Cooldown: none. The per-use "action" energy cost is the throttle
//     (Simplicity-First — a separate cooldown timer is not warranted yet).
type TransportCargoCommand struct {
	PlayerID     domain.PlayerID
	ShipID       domain.ShipID // the up_transporter ship (receives the cargo)
	SourceShipID domain.ShipID // the player's own ship the cargo is pulled from
	GoodsType    domain.GoodsTypeID
	Quantity     int64
	Reply        chan<- CmdResult
}

func (c TransportCargoCommand) apply(w *Worker, s *sectorState) {
	replyOnce(c.Reply, CmdResult{Err: w.transportCargo(s, c)})
}

// transportCargo validates the teleport against RAM (ownership, fit, proximity,
// energy) and, on success, performs the cross-hold cargo move via the shared
// haul executor and debits the receiver's energy. Both ships must be in this
// sector — the worker only holds its own sector's ships, so a source elsewhere
// reads as not-found (a teleport is line-of-sight within a sector).
func (w *Worker) transportCargo(s *sectorState, c TransportCargoCommand) error {
	dest, ok := s.ships[c.ShipID]
	if !ok {
		return ErrShipNotFound
	}
	if dest.PlayerID != c.PlayerID {
		return ErrForbidden
	}
	if shipEquipmentLevel(dest, transporterModuleType) < 1 {
		return ErrEquipmentRequired
	}
	if c.SourceShipID == c.ShipID {
		return ErrForbidden // a ship cannot teleport cargo to itself
	}
	src, ok := s.ships[c.SourceShipID]
	if !ok {
		return ErrShipNotFound
	}
	if src.PlayerID != c.PlayerID {
		return ErrForbidden
	}
	if dest.Pos.Sub(src.Pos).Length() > w.cfg.TransporterRange {
		return ErrTransporterOutOfRange
	}
	if dest.Energy < w.cfg.TransporterEnergyCost {
		return ErrNotEnoughEnergy
	}
	if w.traderLogistics == nil {
		return nil // no cargo executor wired (pure unit test) — validated no-op
	}
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(c.SourceShipID)}
	destRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(c.ShipID)}
	if err := w.traderLogistics.Haul(context.Background(), srcRef, destRef, c.GoodsType, c.Quantity); err != nil {
		return err
	}
	if w.cfg.TransporterEnergyCost > 0 {
		dest.Energy -= w.cfg.TransporterEnergyCost
	}
	s.markDirty(c.ShipID)
	s.markDirty(c.SourceShipID)
	return nil
}
