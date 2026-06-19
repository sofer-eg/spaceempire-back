package sector

import "spaceempire/back/internal/domain"

// carryDockedShips snaps every ship docked to a host ship (Docked.Kind ==
// EntityKindShip) onto the host's current position each tick — the "ride-along"
// for ship-to-ship docking (phase 10.3.24). Static-docked ships need no carry
// (statics do not move). Runs after applyMovement so the host's just-updated
// position is used.
//
// A host that has left the sector (gate jump) or been destroyed is skipped: the
// carried ship stays put with a dangling Docked and the player can undock
// manually. Cross-sector carry and death-release are follow-ups (see
// external_docking.md §5).
func carryDockedShips(s *sectorState) {
	for id, ship := range s.ships {
		if ship.Docked == nil || ship.Docked.Kind != domain.EntityKindShip {
			continue
		}
		host, ok := s.ships[domain.ShipID(ship.Docked.ID)]
		if !ok {
			continue
		}
		if ship.Pos != host.Pos {
			ship.Pos = host.Pos
			s.markDirty(id)
		}
	}
}
