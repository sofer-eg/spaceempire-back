package sector

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
)

// Hangar slot numbers, matching balance.ShipClass.HangerShipType /
// the original StarWind ships.hanger_ship_type (1 = capital, 2 = small).
const (
	hangarSlotCapital = 1
	hangarSlotSmall   = 2
)

// dockToShip resolves the host ship and runs the ship-to-ship dock (phase
// 10.3.24). A missing host yields ErrTargetNotFound.
func (w *Worker) dockToShip(s *sectorState, ship *domain.Ship, target domain.EntityRef) error {
	host, err := lookupShip(s, target)
	if err != nil {
		return err
	}
	return w.executeDockToShip(s, ship, host)
}

// lookupShip resolves an EntityRef into the live ship of the same sector, or
// ErrTargetNotFound when no such ship is present.
func lookupShip(s *sectorState, ref domain.EntityRef) (*domain.Ship, error) {
	host, ok := s.ships[domain.ShipID(ref.ID)]
	if !ok {
		return nil, ErrTargetNotFound
	}
	return host, nil
}

// executeDockToShip validates the ship-to-ship dock invariants (already
// docked, self, sector, range, open/owner, hostility, hangar capacity) and, on
// success, parks the ship in the host's hangar via applyShipDock. On any error
// the ship is left untouched. Port of the SP Docking op=2 target_type=5 branch
// (sql/db.sql:11138-11420).
func (w *Worker) executeDockToShip(s *sectorState, ship, host *domain.Ship) error {
	if ship.Docked != nil {
		return ErrAlreadyDocked
	}
	if host.ID == ship.ID {
		return ErrDockSelf
	}
	if host.SectorID != ship.SectorID {
		return ErrTargetSectorMismatch
	}
	if ship.Pos.Sub(host.Pos).Length() > w.cfg.DockRange {
		return ErrDockOutOfRange
	}
	if !host.IsOpen && host.PlayerID != ship.PlayerID {
		return ErrDockNotOpen
	}
	if w.relations != nil &&
		w.relations.IsHostile(domain.PlayerRef(ship.PlayerID), domain.PlayerRef(host.PlayerID)) {
		return ErrDockHostile
	}
	if err := w.checkHangarCapacity(s, ship, host); err != nil {
		return err
	}
	return w.applyShipDock(s, ship, host)
}

// applyShipDock mutates the ship into its docked-to-host state and persists
// immediately (docking is a critical event, like executeDock). The ship is
// snapped to the host's position and carried along from then on by
// carryDockedShips.
func (w *Worker) applyShipDock(s *sectorState, ship, host *domain.Ship) error {
	ref := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(host.ID)}
	prev := *ship
	ship.Docked = &ref
	ship.Pos = host.Pos
	ship.Vel = domain.Vec2{}
	ship.Target = nil
	ship.FinalTarget = nil
	ship.CurrentTargetRef = nil
	ship.MiningTarget = nil

	if w.repo != nil {
		if err := w.repo.Save(context.Background(), *ship); err != nil {
			*ship = prev
			return fmt.Errorf("save ship-docked ship: %w", err)
		}
	}
	s.markDirty(ship.ID)
	return nil
}

// checkHangarCapacity ports the SP hangar-capacity gate: the docking ship
// occupies a slot (capital/small) in the host's hangar; the host must have a
// hangar of that type with room for the ship's footprint after the ships
// already docked to it. Without a hangar resolver (w.hangers == nil)
// ship-to-ship docking is disabled.
func (w *Worker) checkHangarCapacity(s *sectorState, ship, host *domain.Ship) error {
	if w.hangers == nil {
		return ErrNoHangar
	}
	footprint := w.hangers.HangerOf(ship.ShipClassID)
	slot := footprint.ShipType
	if slot == 0 {
		return ErrNoHangar
	}
	capacity := hangarCapacityForSlot(w.hangers.HangerOf(host.ShipClassID), slot)
	if capacity == 0 {
		return ErrNoHangar
	}
	if capacity-(footprint.ShipSpace+w.usedHangarSpace(s, host.ID, slot)) < 0 {
		return ErrHangarFull
	}
	return nil
}

// hangarCapacityForSlot returns the host hangar capacity for the given slot.
func hangarCapacityForSlot(h domain.Hanger, slot int) int {
	if slot == hangarSlotSmall {
		return h.Small
	}
	return h.Capital
}

// usedHangarSpace sums the footprint of ships already docked in host's hangar
// of the given slot type.
func (w *Worker) usedHangarSpace(s *sectorState, host domain.ShipID, slot int) int {
	used := 0
	for _, other := range s.ships {
		if other.Docked == nil || other.Docked.Kind != domain.EntityKindShip {
			continue
		}
		if other.Docked.ID != int64(host) {
			continue
		}
		if w.hangers.HangerOf(other.ShipClassID).ShipType == slot {
			used += w.hangers.HangerOf(other.ShipClassID).ShipSpace
		}
	}
	return used
}
