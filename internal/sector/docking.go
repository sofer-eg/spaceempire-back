package sector

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
)

var (
	// ErrAlreadyDocked is returned when DockCommand fires on a ship that
	// already has Docked != nil. The current dock must be released first.
	ErrAlreadyDocked = errors.New("sector: ship already docked")
	// ErrNotDocked is returned when UndockCommand fires on a ship that is
	// free in space (Docked == nil).
	ErrNotDocked = errors.New("sector: ship not docked")
	// ErrTargetSectorMismatch is returned when DockCommand references a
	// static that lives in a different sector than the ship.
	ErrTargetSectorMismatch = errors.New("sector: target in different sector")
	// ErrDockOutOfRange is returned when the ship is further than
	// Config.DockRange from the target's position.
	ErrDockOutOfRange = errors.New("sector: ship out of dock range")
	// ErrTargetNotFound is returned when DockCommand references a static
	// (kind+id) that the sector worker does not know about.
	ErrTargetNotFound = errors.New("sector: dock target not found")
	// ErrInvalidDockTarget is returned when the EntityKind is not one of
	// the four static dockable kinds (station / shipyard / trade station /
	// pirbase).
	ErrInvalidDockTarget = errors.New("sector: invalid dock target kind")
)

// DockCommand is the player-issued request to dock the given ship to the
// referenced static. The worker validates ownership, the static's existence,
// and Config.DockRange before mutating state. Manual dock works regardless
// of Ship.AutoPilotModule — that flag only gates the tick-driven auto-dock.
type DockCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	Reply    chan<- CmdResult
}

func (c DockCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}
	target, err := lookupStatic(s.statics, c.Target)
	if err != nil {
		res.Err = err
		replyOnce(c.Reply, res)
		return
	}
	res.Err = executeDock(w, s, ship, target, w.cfg.DockRange)
	replyOnce(c.Reply, res)
}

// UndockCommand is the player-issued request to release the ship from its
// current dock. The ship stays at the static's position (no automatic
// nudge "outward" in MVP); the player is expected to issue a course right
// after — and a fresh DockCommand would no-op if the player never moved.
type UndockCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Reply    chan<- CmdResult
}

func (c UndockCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked == nil:
		res.Err = ErrNotDocked
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}
	res.Err = executeUndock(w, s, ship)
	replyOnce(c.Reply, res)
}

// executeDock validates dock-time invariants (already-docked, sector match,
// proximity), mutates the ship to its docked state, persists immediately,
// and marks dirty for the next snapshot. On any error the ship is left
// untouched. Callers (DockCommand, tick-driven auto-dock) reuse this so the
// state transition lives in exactly one place.
func executeDock(w *Worker, s *sectorState, ship *domain.Ship, target domain.DockableObject, dockRange float64) error {
	if ship.Docked != nil {
		return ErrAlreadyDocked
	}
	if target.ObjectSector() != ship.SectorID {
		return ErrTargetSectorMismatch
	}
	if ship.Pos.Sub(target.ObjectPos()).Length() > dockRange {
		return ErrDockOutOfRange
	}

	ref := target.ObjectID()
	prev := *ship
	ship.Docked = &ref
	ship.Pos = target.ObjectPos()
	ship.Vel = domain.Vec2{}
	ship.Target = nil
	ship.FinalTarget = nil
	ship.CurrentTargetRef = nil

	if w.repo != nil {
		if err := w.repo.Save(context.Background(), *ship); err != nil {
			*ship = prev
			return fmt.Errorf("save docked ship: %w", err)
		}
	}
	s.markDirty(ship.ID)
	return nil
}

// executeUndock flips Docked to nil and clears velocity/targets. Caller has
// already verified Docked != nil. Persisted immediately so a crash between
// undock and the next periodic snapshot does not strand the player on a
// destroyed station.
func executeUndock(w *Worker, s *sectorState, ship *domain.Ship) error {
	prev := *ship
	ship.Docked = nil
	ship.Vel = domain.Vec2{}
	ship.Target = nil
	ship.FinalTarget = nil
	ship.CurrentTargetRef = nil

	if w.repo != nil {
		if err := w.repo.Save(context.Background(), *ship); err != nil {
			*ship = prev
			return fmt.Errorf("save undocked ship: %w", err)
		}
	}
	s.markDirty(ship.ID)
	return nil
}

// lookupStatic resolves an EntityRef into the matching SectorStatics entry.
// Only the four static dockable kinds are valid; anything else returns
// ErrInvalidDockTarget. Missing kind+id returns ErrTargetNotFound.
func lookupStatic(s domain.SectorStatics, ref domain.EntityRef) (domain.DockableObject, error) {
	switch ref.Kind {
	case domain.EntityKindStation:
		for i := range s.Stations {
			if int64(s.Stations[i].ID) == ref.ID {
				return s.Stations[i], nil
			}
		}
	case domain.EntityKindShipyard:
		for i := range s.Shipyards {
			if int64(s.Shipyards[i].ID) == ref.ID {
				return s.Shipyards[i], nil
			}
		}
	case domain.EntityKindTradeStation:
		for i := range s.TradeStations {
			if int64(s.TradeStations[i].ID) == ref.ID {
				return s.TradeStations[i], nil
			}
		}
	case domain.EntityKindPirbase:
		for i := range s.Pirbases {
			if int64(s.Pirbases[i].ID) == ref.ID {
				return s.Pirbases[i], nil
			}
		}
	default:
		return nil, ErrInvalidDockTarget
	}
	return nil, ErrTargetNotFound
}
