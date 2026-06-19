package sector

import (
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// Patch is the per-tick delta a Subscription receives over its channel. It
// is designed to map 1:1 onto the WS wire contract from phase 1.4 and the
// AOI contract in phase 2.6.
//
// Empty patches (every ship/missile slice empty and no effects/impacts)
// are not sent — the worker filters them out.
type Patch struct {
	Tick    uint64
	Added   []domain.Ship
	Updated []domain.Ship
	Removed []domain.ShipID

	LaserEffects []combat.LaserBeam

	// Missile delta against the subscriber's previous frame within AOI.
	// MissilesAdded carries the full Missile (the SPA does not have a
	// prior record); MissilesUpdated only carries missiles whose Pos /
	// Vel / Direction changed; MissilesRemoved is the id-only list of
	// missiles that disappeared (hit / expired / left AOI). Phase 4.3.
	MissilesAdded   []domain.Missile
	MissilesUpdated []domain.Missile
	MissilesRemoved []domain.MissileID
	MissileImpacts  []MissileImpact

	// Drone delta against the subscriber's previous frame within AOI,
	// same shape as the missile delta. Phase 4.4.
	DronesAdded   []domain.Drone
	DronesUpdated []domain.Drone
	DronesRemoved []domain.DroneID
	DroneImpacts  []DroneImpact

	// Container delta against the subscriber's previous frame within AOI.
	// Containers are immutable once created, so there is no "updated"
	// bucket — only added (full Container) and removed (id-only). Phase 4.6.
	ContainersAdded   []domain.Container
	ContainersRemoved []domain.ContainerID

	// Asteroid delta against the subscriber's previous frame within AOI.
	// Asteroids are static (Pos/OreType fixed), so AsteroidsAdded carries the
	// full Asteroid (the SPA has no prior record), AsteroidsUpdated carries
	// bodies whose Mass changed this tick (mining), and AsteroidsRemoved is the
	// id-only list of asteroids that disappeared (depleted or left AOI). Phase
	// 10.3.6.
	AsteroidsAdded   []domain.Asteroid
	AsteroidsUpdated []domain.Asteroid
	AsteroidsRemoved []domain.AsteroidID

	// Static-combat delta (phase 6.2b): statics whose HP/Shield changed this
	// tick (StaticsUpdated) and statics destroyed this tick (StaticsRemoved,
	// ref-only). Statics ship once via StaticsMessage, so these patch their
	// live combat state on the client. Static-combat events are rare, so the
	// delta is sector-global (no AOI / per-subscriber diff).
	StaticsUpdated []domain.DestructibleStatic
	StaticsRemoved []domain.EntityRef

	// StaticsAdded carries the full static objects that just entered the
	// subscriber's big-radar window (phase 10.20 L2): a SectorStatics subset the
	// client merges into its statics map. Statics that left the window are added
	// to StaticsRemoved (the client's removeStaticsByRefs handles both distance
	// exits and destruction). Per-subscriber, diffed against lastSentStatics.
	StaticsAdded domain.SectorStatics

	// TimeScale is the sector's current time-dilation factor (phase 7.2):
	// 1.0 = real time, < 1.0 = slowed under overload. Sent on every patch so
	// the SPA can show a "time dilation" indicator. Not part of IsEmpty — a
	// dilated sector forces a send even when the AOI view is otherwise empty
	// (see broadcastPatches).
	TimeScale float64
}

// IsEmpty reports whether the patch contains no changes.
func (p Patch) IsEmpty() bool {
	return len(p.Added) == 0 && len(p.Updated) == 0 && len(p.Removed) == 0 &&
		len(p.LaserEffects) == 0 &&
		len(p.MissilesAdded) == 0 && len(p.MissilesUpdated) == 0 &&
		len(p.MissilesRemoved) == 0 && len(p.MissileImpacts) == 0 &&
		len(p.DronesAdded) == 0 && len(p.DronesUpdated) == 0 &&
		len(p.DronesRemoved) == 0 && len(p.DroneImpacts) == 0 &&
		len(p.ContainersAdded) == 0 && len(p.ContainersRemoved) == 0 &&
		len(p.AsteroidsAdded) == 0 && len(p.AsteroidsUpdated) == 0 &&
		len(p.AsteroidsRemoved) == 0 &&
		len(p.StaticsUpdated) == 0 && len(p.StaticsRemoved) == 0 &&
		p.StaticsAdded.IsEmpty()
}

// buildPatch diffs prev → curr. Ships present only in curr go to Added,
// ships present in both with any observable field changed go to Updated,
// and ships present only in prev go to Removed. Both maps are read-only
// for the caller after this call.
func buildPatch(prev, curr map[domain.ShipID]domain.Ship, tick uint64) Patch {
	p := Patch{Tick: tick}
	for id, c := range curr {
		pv, existed := prev[id]
		if !existed {
			p.Added = append(p.Added, c)
			continue
		}
		if !shipEqual(pv, c) {
			p.Updated = append(p.Updated, c)
		}
	}
	for id := range prev {
		if _, stillThere := curr[id]; !stillThere {
			p.Removed = append(p.Removed, id)
		}
	}
	return p
}

// shipEqual compares the fields the broadcaster considers observable.
// MaxSpeed/HP/Shield/Vel/Facing/Energy are included so the snapshot
// still drives the client in later phases (combat, etc.); add fields
// here as they become visible. Acceleration/TurnRate are class
// characteristics fixed at Create — never change between ticks in the
// current model.
func shipEqual(a, b domain.Ship) bool {
	if a.ID != b.ID || a.PlayerID != b.PlayerID || a.SectorID != b.SectorID {
		return false
	}
	if a.Pos != b.Pos || a.Vel != b.Vel || a.Direction != b.Direction {
		return false
	}
	if a.HP != b.HP || a.Shield != b.Shield || a.MaxSpeed != b.MaxSpeed {
		return false
	}
	if a.Energy != b.Energy {
		return false
	}
	if !entityRefPtrEqual(a.Docked, b.Docked) {
		return false
	}
	if !entityRefPtrEqual(a.CurrentTargetRef, b.CurrentTargetRef) {
		return false
	}
	if !entityRefPtrEqual(a.AttackTarget, b.AttackTarget) {
		return false
	}
	if !asteroidIDPtrEqual(a.MiningTarget, b.MiningTarget) {
		return false
	}
	switch {
	case a.Target == nil && b.Target == nil:
		return true
	case a.Target == nil || b.Target == nil:
		return false
	default:
		return *a.Target == *b.Target
	}
}

// entityRefPtrEqual reports value equality of two *EntityRef pointers,
// treating two nils as equal.
func entityRefPtrEqual(a, b *domain.EntityRef) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// asteroidIDPtrEqual reports value equality of two *AsteroidID pointers,
// treating two nils as equal. Used so a mining-state change (phase 10.3.21)
// is detected even when nothing else about the ship moved.
func asteroidIDPtrEqual(a, b *domain.AsteroidID) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// shipsMapSubset builds a value-type snapshot restricted to the given IDs.
// Target and FinalTarget are deep-copied so patches handed to subscribers
// never alias live state. Used by the AOI broadcaster to materialise only
// the ships a subscriber can currently see.
func shipsMapSubset(src map[domain.ShipID]*domain.Ship, ids map[domain.ShipID]struct{}) map[domain.ShipID]domain.Ship {
	out := make(map[domain.ShipID]domain.Ship, len(ids))
	for id := range ids {
		s, ok := src[id]
		if !ok {
			continue
		}
		cp := *s
		cp.Target = cloneVec2(s.Target)
		cp.FinalTarget = cloneCourse(s.FinalTarget)
		cp.Docked = cloneEntityRef(s.Docked)
		cp.AttackTarget = cloneEntityRef(s.AttackTarget)
		cp.CurrentTargetRef = cloneEntityRef(s.CurrentTargetRef)
		cp.MiningTarget = cloneAsteroidID(s.MiningTarget)
		out[id] = cp
	}
	return out
}
