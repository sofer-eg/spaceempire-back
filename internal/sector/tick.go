package sector

import (
	"sort"

	"spaceempire/back/internal/domain"
)

func applyMovement(s *sectorState, dt float64) {
	for id, ship := range s.ships {
		if ship.Docked != nil {
			continue
		}
		if moveShip(ship, dt) {
			s.markDirty(id)
		}
	}
}

// snapshotShips returns a sorted-by-ID slice of value-type ships derived
// from src, with deep-copied Target/FinalTarget/Docked so the snapshot can
// be safely mutated by consumers.
func snapshotShips(src map[domain.ShipID]*domain.Ship) []domain.Ship {
	ships := make([]domain.Ship, 0, len(src))
	for _, ship := range src {
		cp := *ship
		cp.Target = cloneVec2(ship.Target)
		cp.FinalTarget = cloneCourse(ship.FinalTarget)
		cp.Docked = cloneEntityRef(ship.Docked)
		cp.AttackTarget = cloneEntityRef(ship.AttackTarget)
		cp.CurrentTargetRef = cloneEntityRef(ship.CurrentTargetRef)
		ships = append(ships, cp)
	}
	sort.Slice(ships, func(i, j int) bool {
		return ships[i].ID < ships[j].ID
	})
	return ships
}
