package sector

import "spaceempire/back/internal/domain"

// PathRouter is the minimal routing surface the autopilot needs. It is
// declared here (consumer side) per ISP — *world.PathRouter satisfies it
// implicitly. Tests can pass a stub instead of standing up a full topology.
type PathRouter interface {
	NextSector(from, to domain.SectorID) (domain.SectorID, bool)
	GateBetween(a, b domain.SectorID) *domain.Gate
}

// approachStopFraction is the share of Config.DockRange the autopilot
// uses as the "park here" radius when Course.Approach is set. Half the
// dock range leaves a forgiving inner buffer: once the ship coasts to
// rest at this radius, the player's manual /api/cmd/dock click still
// finds the ship inside Config.DockRange even if the controller jitters
// a unit or two.
const approachStopFraction = 0.5

// resolveAutopilot walks every ship in the sector and, when FinalTarget is
// set, computes the next waypoint:
//
//   - same sector + no Approach: Target = FinalTarget.Pos.
//   - same sector + Approach set: aim for the park spot one DockRange/2
//     short of the target, and once the ship is inside that radius drop
//     Target so applyMovement clamps Vel to zero. The ship is now
//     "parked" — Course.Approach stays so the SPA can highlight the
//     pending dock until the player issues DockCommand or moves.
//   - different sector: Target = exit position of the gate on the next hop.
//   - unreachable: drop FinalTarget — the ship comes to rest where it is.
//
// Called from the worker's tick before applyMovement. Mutations are routed
// through the sector state's dirty set so the changes are eventually
// persisted.
func resolveAutopilot(s *sectorState, router PathRouter, dockRange float64) {
	if router == nil {
		return
	}
	for id, ship := range s.ships {
		if ship.Docked != nil {
			continue
		}
		if ship.FinalTarget == nil {
			continue
		}
		if ship.SectorID == ship.FinalTarget.Sector {
			if ship.FinalTarget.Approach != nil {
				resolveApproach(ship, s, id, dockRange)
				continue
			}
			setWaypoint(ship, ship.FinalTarget.Pos, s, id)
			continue
		}
		next, ok := router.NextSector(ship.SectorID, ship.FinalTarget.Sector)
		if !ok {
			ship.FinalTarget = nil
			ship.Target = nil
			s.markDirty(id)
			continue
		}
		gate := router.GateBetween(ship.SectorID, next)
		if gate == nil {
			// Router said the hop is reachable but no gate connects them —
			// data inconsistency. Drop the autopilot rather than spin.
			ship.FinalTarget = nil
			ship.Target = nil
			s.markDirty(id)
			continue
		}
		exitPos := gateExitOn(gate, ship.SectorID)
		setWaypoint(ship, exitPos, s, id)
	}
}

// resolveApproach computes the autopilot Target for an approach-style
// Course (Approach != nil). The ship aims for a point dockRange/2 short
// of FinalTarget.Pos along the ship→target line; once inside that
// radius it parks. The Course is not cleared — DockCommand or
// MoveCommand from the player is the only way out.
func resolveApproach(ship *domain.Ship, s *sectorState, id domain.ShipID, dockRange float64) {
	stopRadius := dockRange * approachStopFraction
	delta := ship.FinalTarget.Pos.Sub(ship.Pos)
	dist := delta.Length()
	if dist <= stopRadius {
		// Park: clear Target so applyMovement zeroes Vel. Keep Approach so
		// the SPA UI can still light the dock affordance.
		if ship.Target != nil || !ship.Vel.IsZero() {
			ship.Target = nil
			ship.Vel = domain.Vec2{}
			s.markDirty(id)
		}
		return
	}
	dir := delta.Scale(1 / dist)
	parkSpot := ship.FinalTarget.Pos.Sub(dir.Scale(stopRadius))
	setWaypoint(ship, parkSpot, s, id)
}

// setWaypoint updates ship.Target only when the new value differs from the
// current one, avoiding unnecessary dirty markings on autopilot ticks where
// the waypoint is unchanged.
func setWaypoint(ship *domain.Ship, pos domain.Vec2, s *sectorState, id domain.ShipID) {
	if ship.Target != nil && *ship.Target == pos {
		return
	}
	cp := pos
	ship.Target = &cp
	s.markDirty(id)
}

// gateExitOn returns the exit position on `sector`'s side of the gate. The
// caller has already verified that `sector` is one of the gate's endpoints
// (via router.NextSector), so the default branch is unreachable in
// production — it falls back to PosA so the function remains total.
func gateExitOn(g *domain.Gate, sector domain.SectorID) domain.Vec2 {
	if g.SectorB == sector {
		return g.PosB
	}
	return g.PosA
}
