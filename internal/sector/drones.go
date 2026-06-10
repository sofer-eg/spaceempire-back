package sector

import (
	"context"
	"math"
	"sort"
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// droneAcquireFactor sets the auto-acquire detection radius as a multiple of
// the drone's weapon reach (6.2a): a drone notices a hostile a bit beyond
// firing range and closes in. Reuses droneSpec.FireRange so it scales with
// the spec.
const droneAcquireFactor = 4.0

// droneSpec is the spec the sector tick passes into combat.TickDrone.
// Phase 4.4 has exactly one drone class (see drones.md §6), so the spec
// is a package-level value. When the balance catalog arrives this will
// move into per-drone state.
var droneSpec = combat.DefaultDroneSpec()

// sortDrones orders a slice of value-type Drone by ID in place so every
// Snapshot the worker hands out is deterministic for diffing and tests.
func sortDrones(ds []domain.Drone) {
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
}

// liveDroneCount returns how many drones the given ship currently has in
// flight. Every drone in s.drones is alive (the tick removes dead/expired/
// recalled ones immediately), so a plain count suffices. Used by the
// up_drone_control cap in LaunchDroneCommand (phase 10.14b).
func (s *sectorState) liveDroneCount(ship domain.ShipID) int {
	n := 0
	for _, d := range s.drones {
		if d.OwnerShipID == ship {
			n++
		}
	}
	return n
}

// DroneImpact is a per-tick drone event broadcast in the Snapshot. A
// shot-fired event carries Damage (and Killed if the target died); a
// death/expire event sets Expired=true. The SPA renders both as a brief
// flash at Pos, same as MissileImpact.
type DroneImpact struct {
	DroneID     domain.DroneID
	OwnerShipID domain.ShipID
	Target      domain.EntityRef
	Pos         domain.Vec2
	Damage      int
	Killed      bool
	Expired     bool
}

// tickDrones advances every drone in the sector for dt seconds. For each
// drone it resolves the owner and target, steers the drone (toward the
// target while alive, otherwise back toward the owner), fires when in
// range, and self-destructs the drone on TTL expiry or owner loss —
// writing the removal to the DB immediately (drones are persistent).
//
// Targeting is intentionally narrow in phase 4.4: a drone shoots exactly
// the ship it was launched at, with no hostility check or auto-acquire —
// that arrives with relations in 6.2 (see drones.md §4).
func (w *Worker) tickDrones(ctx context.Context, s *sectorState, dt float64, now time.Time) {
	for id, d := range s.drones {
		owner, ownerOK := s.ships[d.OwnerShipID]
		if !ownerOK || owner.HP <= 0 {
			// Owner gone — the drone self-destructs (SP Orders=8).
			w.removeDrone(ctx, s, id, DroneImpact{
				DroneID:     id,
				OwnerShipID: d.OwnerShipID,
				Target:      d.Target,
				Pos:         d.Pos,
				Expired:     true,
			})
			continue
		}

		var targetShip *domain.Ship
		targetAlive := false
		if d.Target.Kind == domain.EntityKindShip {
			if ts, ok := s.ships[domain.ShipID(d.Target.ID)]; ok && ts.HP > 0 {
				targetShip = ts
				targetAlive = true
			}
		}

		// Auto-acquire (6.2a, SP Orders=4): with no live explicit target,
		// lock onto the nearest hostile ship in detection range. The acquired
		// ship is the active target only for this tick (d.Target is left as
		// the launch target).
		activeTarget := d.Target
		if !targetAlive {
			if acq := w.acquireDroneTarget(s, d); acq != nil {
				targetShip = acq
				targetAlive = true
				activeTarget = domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(acq.ID)}
			}
		}

		dest := owner.Pos
		if targetAlive {
			dest = targetShip.Pos
		}

		if combat.TickDrone(d, dest, droneSpec, dt, now) == combat.DroneExpired {
			w.removeDrone(ctx, s, id, DroneImpact{
				DroneID:     id,
				OwnerShipID: d.OwnerShipID,
				Target:      d.Target,
				Pos:         d.Pos,
				Expired:     true,
			})
			continue
		}
		s.markDroneDirty(id)

		if targetAlive && combat.DroneCanFire(d, targetShip.Pos, droneSpec) {
			res := combat.ApplyDamage(targetShip, d.Damage)
			// Attribute the kill for bounty payout (6.3) to the drone owner.
			targetShip.LastAttacker = d.PlayerID
			s.addDroneImpact(DroneImpact{
				DroneID:     id,
				OwnerShipID: d.OwnerShipID,
				Target:      activeTarget,
				Pos:         d.Pos,
				Damage:      res.ShieldAbsorbed + res.HPAbsorbed,
				Killed:      res.Killed,
			})
			s.markDirty(targetShip.ID)
		}
	}
}

// acquireDroneTarget returns the nearest live hostile ship within the drone's
// detection radius (droneSpec.FireRange × droneAcquireFactor), or nil. Used
// when the drone has no live explicit target. A nil relations oracle (tests
// without 6.2a wiring) returns nil — no auto-acquire.
func (w *Worker) acquireDroneTarget(s *sectorState, d *domain.Drone) *domain.Ship {
	if w.relations == nil {
		return nil
	}
	r2 := (droneSpec.FireRange * droneAcquireFactor) * (droneSpec.FireRange * droneAcquireFactor)
	var best *domain.Ship
	bestSq := math.MaxFloat64
	for _, ship := range s.ships {
		if ship.HP <= 0 || ship.ID == d.OwnerShipID {
			continue
		}
		if !w.relations.IsHostile(domain.PlayerRef(d.PlayerID), domain.PlayerRef(ship.PlayerID)) {
			continue
		}
		diff := ship.Pos.Sub(d.Pos)
		sq := diff.X*diff.X + diff.Y*diff.Y
		if sq > r2 || sq >= bestSq {
			continue
		}
		best, bestSq = ship, sq
	}
	return best
}

// removeDrone deletes a drone from RAM and the DB (immediate), records the
// supplied impact for the WS broadcast, and clears any dirty flag. Used by
// both the TTL/owner-loss path and RecallDronesCommand.
func (w *Worker) removeDrone(ctx context.Context, s *sectorState, id domain.DroneID, imp DroneImpact) {
	s.addDroneImpact(imp)
	delete(s.drones, id)
	delete(s.dronesDirty, id)
	if w.droneRepo != nil {
		if err := w.droneRepo.Delete(ctx, id); err != nil {
			w.logger.ErrorContext(ctx, "drone delete failed",
				"err", err, "drone", int64(id), "sector", int64(s.sectorID))
		}
	}
}

// snapshotDrones returns a sorted-by-ID slice of value-type drones. Drone
// has no pointer fields, so a plain value copy satisfies the
// worker→subscriber isolation contract.
func snapshotDrones(src map[domain.DroneID]*domain.Drone) []domain.Drone {
	if len(src) == 0 {
		return nil
	}
	out := make([]domain.Drone, 0, len(src))
	for _, d := range src {
		out = append(out, *d)
	}
	sortDrones(out)
	return out
}

// dronesInRadius returns the subset of live drones whose Pos lies within
// radius of center. radius<=0 disables the filter. Output is a value-type
// map (Drone has no pointer fields).
func dronesInRadius(src map[domain.DroneID]*domain.Drone, center domain.Vec2, radius float64) map[domain.DroneID]domain.Drone {
	if len(src) == 0 {
		return nil
	}
	out := make(map[domain.DroneID]domain.Drone, len(src))
	if radius <= 0 {
		for id, d := range src {
			out[id] = *d
		}
		return out
	}
	r2 := radius * radius
	for id, d := range src {
		dx := d.Pos.X - center.X
		dy := d.Pos.Y - center.Y
		if dx*dx+dy*dy <= r2 {
			out[id] = *d
		}
	}
	return out
}

// diffDrones produces the per-tick drone delta vs the subscriber's
// previously-seen set. Drone is a comparable value (no pointer fields) so
// "changed" is a plain `!=`.
func diffDrones(prev, curr map[domain.DroneID]domain.Drone) (added, updated []domain.Drone, removed []domain.DroneID) {
	for id, d := range curr {
		pv, existed := prev[id]
		switch {
		case !existed:
			added = append(added, d)
		case pv != d:
			updated = append(updated, d)
		}
	}
	for id := range prev {
		if _, still := curr[id]; !still {
			removed = append(removed, id)
		}
	}
	return added, updated, removed
}

// filterDroneImpactsForAOI keeps only impacts whose Pos is inside the
// subscriber's AOI window. radius<=0 disables the filter.
func filterDroneImpactsForAOI(imps []DroneImpact, center domain.Vec2, radius float64) []DroneImpact {
	if len(imps) == 0 {
		return nil
	}
	if radius <= 0 {
		out := make([]DroneImpact, len(imps))
		copy(out, imps)
		return out
	}
	r2 := radius * radius
	var out []DroneImpact
	for _, imp := range imps {
		if pointInRadius2(imp.Pos, center, r2) {
			out = append(out, imp)
		}
	}
	return out
}
