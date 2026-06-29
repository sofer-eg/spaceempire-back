package sector

import (
	"context"
	"log/slog"
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

type Snapshot struct {
	SectorID         domain.SectorID
	Tick             uint64
	Ships            []domain.Ship
	Statics          domain.SectorStatics
	LastTickDuration time.Duration

	// HandoffsOut[targetSector] = number of ships this sector has handed
	// off to targetSector since worker start. HandoffsIn[sourceSector] is
	// the reverse. Maps may be nil when no handoffs have happened yet.
	HandoffsOut map[domain.SectorID]uint64
	HandoffsIn  map[domain.SectorID]uint64

	// ProductionCycles counts station factory cycles completed in this
	// sector since worker start. Exposed via Stats() for the future
	// Prometheus exporter (phase 7.1).
	ProductionCycles uint64

	// LaserEffects holds the one-frame laser shots that fired in this
	// tick. Empty between ticks. Subscribers render them in the same
	// frame the ships' updated HP/Shield arrive. Phase 4.2.
	LaserEffects []combat.LaserBeam

	// Missiles is the live in-flight set at the end of this tick. New
	// patches deliver a missiles diff (added/updated/removed) against
	// the previous tick's set. Phase 4.3.
	Missiles []domain.Missile

	// MissileImpacts holds the one-frame missile events (hit / expire)
	// that fired in this tick. Empty between ticks.
	MissileImpacts []MissileImpact

	// Drones is the live combat-drone set at the end of this tick. Patches
	// deliver a drone diff (added/updated/removed) against the previous
	// tick's set, same shape as Missiles. Phase 4.4.
	Drones []domain.Drone

	// DroneImpacts holds the one-frame drone events (shot fired / death /
	// expire) that fired in this tick. Empty between ticks.
	DroneImpacts []DroneImpact

	// Containers is the live loot-container set at the end of this tick.
	// Patches deliver an added/removed delta against the previous tick's
	// set (containers are immutable, so no "updated"). Phase 4.6.
	Containers []domain.Container

	// Asteroids is the live minable ore-body set at the end of this tick.
	// Patches deliver an added/updated/removed delta against the previous
	// tick's set: Pos/OreType are fixed, only Mass changes (mining) so the
	// "updated" bucket carries mass changes, "removed" carries depletion.
	// Phase 10.3.6.
	Asteroids []domain.Asteroid

	// Destructibles is the live combat state (HP/Shield) of every static
	// object in the sector. Patches deliver an updated/removed delta against
	// the previous tick. Phase 6.2b.
	Destructibles []domain.DestructibleStatic
}

// persistDirty is the periodic-snapshot step of the per-sector tick loop. It
// runs at most once every cfg.SnapshotInterval and writes all currently-dirty
// ships in a single BatchUpdate. The dirty set is cleared only on a
// successful write — on error the next tick will retry.
func (w *Worker) persistDirty(ctx context.Context, s *sectorState) {
	if w.repo == nil {
		return
	}
	if w.clock.Now().Sub(s.lastSnapshot) < w.cfg.SnapshotInterval {
		return
	}
	if len(s.dirty) == 0 {
		s.lastSnapshot = w.clock.Now()
		return
	}
	ships := s.collectDirty()
	if err := w.repo.BatchUpdate(ctx, ships); err != nil {
		w.logger.ErrorContext(ctx, "snapshot batch update failed",
			"err", err, "sector", int64(s.sectorID), "dirty_count", len(ships))
		return
	}
	s.dirty = make(map[domain.ShipID]bool)
	s.lastSnapshot = w.clock.Now()
}

// persistDirtyDrones is the drone counterpart of persistDirty: it writes
// the mutable fields of every dirty drone in a single BatchUpdate. It
// piggybacks on the ship snapshot cadence (lastSnapshot, advanced by
// persistDirty) rather than keeping a second timer — both run in the same
// tick, so persistDirty has already gated/advanced lastSnapshot by the
// time this is called. Guard: only flush when the dirty set is non-empty.
func (w *Worker) persistDirtyDrones(ctx context.Context, s *sectorState) {
	if w.droneRepo == nil || len(s.dronesDirty) == 0 {
		return
	}
	if w.clock.Now().Sub(s.lastDroneSnapshot) < w.cfg.SnapshotInterval {
		return
	}
	ds := s.collectDirtyDrones()
	if err := w.droneRepo.BatchUpdate(ctx, ds); err != nil {
		w.logger.ErrorContext(ctx, "drone snapshot batch update failed",
			"err", err, "sector", int64(s.sectorID), "dirty_count", len(ds))
		return
	}
	s.dronesDirty = make(map[domain.DroneID]bool)
	s.lastDroneSnapshot = w.clock.Now()
}

// persistDirtyTorpedos is the torpedo counterpart of persistDirtyDrones: it
// writes the mutable fields of every dirty torpedo in a single BatchUpdate on
// the snapshot cadence (ЧТЗ NFR-002). No-op when persistence is disabled or
// nothing is dirty.
func (w *Worker) persistDirtyTorpedos(ctx context.Context, s *sectorState) {
	if w.torpedoRepo == nil || len(s.torpedosDirty) == 0 {
		return
	}
	if w.clock.Now().Sub(s.lastTorpedoSnapshot) < w.cfg.SnapshotInterval {
		return
	}
	ts := s.collectDirtyTorpedos()
	if err := w.torpedoRepo.BatchUpdate(ctx, ts); err != nil {
		w.logger.ErrorContext(ctx, "torpedo snapshot batch update failed",
			"err", err, "sector", int64(s.sectorID), "dirty_count", len(ts))
		return
	}
	s.torpedosDirty = make(map[domain.TorpedoID]bool)
	s.lastTorpedoSnapshot = w.clock.Now()
}

// persistAsteroids is the asteroid counterpart of persistDirtyDrones: it
// writes the remaining mass of every dirty asteroid in a single BatchUpdate
// on the snapshot cadence. Depleted asteroids are deleted immediately by the
// Mine handler, so this only carries the mass of asteroids still being mined.
// No-op when persistence is disabled or nothing is dirty.
func (w *Worker) persistAsteroids(ctx context.Context, s *sectorState) {
	if w.asteroidRepo == nil || len(s.asteroidsDirty) == 0 {
		return
	}
	if w.clock.Now().Sub(s.lastAsteroidSnapshot) < w.cfg.SnapshotInterval {
		return
	}
	as := s.collectDirtyAsteroids()
	if err := w.asteroidRepo.BatchUpdate(ctx, as); err != nil {
		w.logger.ErrorContext(ctx, "asteroid snapshot batch update failed",
			"err", err, "sector", int64(s.sectorID), "dirty_count", len(as))
		return
	}
	s.asteroidsDirty = make(map[domain.AsteroidID]bool)
	s.lastAsteroidSnapshot = w.clock.Now()
}

// broadcastPatches applies the AOI filter and sends each subscriber the diff
// vs its last delivery. The per-tick spatial grid is built once and reused
// across subscribers; the subscriber's Center is refreshed to track its own
// ship in the sector (origin fallback when no such ship exists, e.g. an
// observer client without an authenticated player). Sends are non-blocking
// — a slow consumer drops the patch instead of stalling the tick loop, and
// its lastSent is left untouched so the next patch will catch it up.
// aoiParams bundles the per-tick radar knobs broadcastPatches needs (phase
// 10.20), so the signature stays small as layers add fields.
type aoiParams struct {
	fallbackRadius  float64   // cfg.AOIRadius, used when a ship has no class radar
	bigMult         float64   // cfg.RadarBigMultiplier — big-object radar = small × this
	stealthDetect   float64   // cfg.StealthDetectRange — close-detect a cloaked ship
	relations       Relations // ship-vs-ship hostility oracle for stealth/ally checks
	satelliteReveal float64   // cfg.SatelliteRevealRadius — radius while a satellite reveals the sector (10.15)
}

func broadcastPatches(logger *slog.Logger, s *sectorState, cellSize float64, ap aoiParams) {
	if len(s.subs) == 0 {
		return
	}
	grid := buildGrid(s.ships, cellSize)
	// Sector radar reveal (phase 10.20 L5): a live navigation satellite lights
	// up the whole sector for its owner and the owner's allies. hasSatellites is
	// the cheap once-per-tick gate; the per-subscriber owner/ally test
	// (satelliteRevealsFor) runs below only when at least one is present.
	hasSatellites := s.satellitesPresent()
	// Static-combat deltas (HP/Shield + destruction) are sector-global (rare
	// events): compute once and attach to every subscriber's patch.
	staticUpdates := s.collectDirtyDestructibles()
	var staticsRemoved []domain.EntityRef
	if len(s.staticsRemoved) > 0 {
		staticsRemoved = append(staticsRemoved, s.staticsRemoved...)
	}
	for _, sub := range s.subs {
		// Personal radar (phase 10.20 L1): center + small-radar radius track the
		// player's own ship in this sector. Ships without a class radar (legacy/
		// spacesuit, RadarRange<=0) and observers fall back to cfg.AOIRadius.
		if ship := playerShip(s.ships, sub.PlayerID); ship != nil {
			sub.Center = ship.Pos
			sub.Radius = radarOrFallback(ship.RadarRange, ap.fallbackRadius)
		} else {
			sub.Center = domain.Vec2{}
			sub.Radius = ap.fallbackRadius
		}
		// A live navigation satellite reveals the whole sector to its owner and
		// allies (phase 10.20 L5): widen the AOI radius so ships and big-radar
		// statics across the sector become visible, but only for a subscriber who
		// owns a satellite here or is allied to an owner. Stealth (hideStealthed)
		// still applies on the boosted window.
		if hasSatellites && ap.satelliteReveal > sub.Radius &&
			s.satelliteRevealsFor(sub.PlayerID, ap.relations) {
			sub.Radius = ap.satelliteReveal
		}
		visible := grid.queryIDs(sub.Center, sub.Radius)
		hideStealthed(visible, s.ships, sub, ap.relations, ap.stealthDetect)
		curr := shipsMapSubset(s.ships, visible)
		patch := buildPatch(sub.lastSent, curr, s.tick)
		patch.LaserEffects = filterLaserEffectsForAOI(s.laserEffects, sub.Center, sub.Radius)
		currMissiles := missilesInRadius(s.missiles, sub.Center, sub.Radius)
		mAdded, mUpdated, mRemoved := diffMissiles(sub.lastSentMissile, currMissiles)
		patch.MissilesAdded = mAdded
		patch.MissilesUpdated = mUpdated
		patch.MissilesRemoved = mRemoved
		patch.MissileImpacts = filterMissileImpactsForAOI(s.missileImpacts, sub.Center, sub.Radius)
		currDrones := dronesInRadius(s.drones, sub.Center, sub.Radius)
		dAdded, dUpdated, dRemoved := diffDrones(sub.lastSentDrone, currDrones)
		patch.DronesAdded = dAdded
		patch.DronesUpdated = dUpdated
		patch.DronesRemoved = dRemoved
		patch.DroneImpacts = filterDroneImpactsForAOI(s.droneImpacts, sub.Center, sub.Radius)
		currTorpedos := torpedosInRadius(s.torpedos, sub.Center, sub.Radius)
		tAdded, tUpdated, tRemoved := diffTorpedos(sub.lastSentTorpedo, currTorpedos)
		patch.TorpedosAdded = tAdded
		patch.TorpedosUpdated = tUpdated
		patch.TorpedosRemoved = tRemoved
		patch.TorpedoImpacts = filterTorpedoImpactsForAOI(s.torpedoImpacts, sub.Center, sub.Radius)
		currContainers := containersInRadius(s.containers, sub.Center, sub.Radius)
		cAdded, cRemoved := diffContainers(sub.lastSentContainer, currContainers)
		patch.ContainersAdded = cAdded
		patch.ContainersRemoved = cRemoved
		currAsteroids := asteroidsInRadius(s.asteroids, sub.Center, sub.Radius)
		aAdded, aUpdated, aRemoved := diffAsteroids(sub.lastSentAsteroid, currAsteroids)
		patch.AsteroidsAdded = aAdded
		patch.AsteroidsUpdated = aUpdated
		patch.AsteroidsRemoved = aRemoved
		patch.StaticsUpdated = staticUpdates
		// Big-object radar (phase 10.20 L2): large statics are visible within
		// RadarRange × bigMult. Diff this window against what the subscriber
		// already has: newly-in-range statics ship in StaticsAdded (full
		// objects), ones that left are appended to StaticsRemoved (alongside the
		// sector-global destruction list — a fresh per-sub copy so subs don't
		// share a slice).
		currStatics := s.staticRefsInRadius(sub.Center, sub.Radius*ap.bigMult)
		addedRefs := diffStaticRefs(sub.lastSentStatics, currStatics)
		subStaticsRemoved := append([]domain.EntityRef(nil), staticsRemoved...)
		subStaticsRemoved = append(subStaticsRemoved, diffStaticRefs(currStatics, sub.lastSentStatics)...)
		if len(addedRefs) > 0 {
			patch.StaticsAdded = s.collectStaticsByRefs(addedRefs)
		}
		patch.StaticsRemoved = subStaticsRemoved
		patch.TimeScale = s.timeScale
		// A dilated sector forces a send even when the AOI view is otherwise
		// empty, so the client's indicator stays current (1.0 sectors skip).
		if patch.IsEmpty() && s.timeScale >= 1.0 {
			continue
		}
		select {
		case sub.patchOut <- patch:
			sub.lastSent = curr
			sub.lastSentMissile = currMissiles
			sub.lastSentDrone = currDrones
			sub.lastSentTorpedo = currTorpedos
			sub.lastSentContainer = currContainers
			sub.lastSentAsteroid = currAsteroids
			sub.lastSentStatics = currStatics
		default:
			logger.Warn("ws patch dropped (slow consumer)",
				"sector", int64(s.sectorID), "player", int64(sub.PlayerID))
		}
	}
}

// missilesInRadius returns the subset of live missiles whose Pos lies
// within radius of center. radius==0 disables the filter (returns all).
// Output is a value-type map, deep-safe because Missile contains no
// pointer fields.
func missilesInRadius(src map[domain.MissileID]*domain.Missile, center domain.Vec2, radius float64) map[domain.MissileID]domain.Missile {
	if len(src) == 0 {
		return nil
	}
	out := make(map[domain.MissileID]domain.Missile, len(src))
	if radius <= 0 {
		for id, m := range src {
			out[id] = *m
		}
		return out
	}
	r2 := radius * radius
	for id, m := range src {
		dx := m.Pos.X - center.X
		dy := m.Pos.Y - center.Y
		if dx*dx+dy*dy <= r2 {
			out[id] = *m
		}
	}
	return out
}

// diffMissiles produces the per-tick missile delta vs the subscriber's
// previously-seen set. Missile values are pure data (no pointer fields)
// so comparison is a plain `==`.
func diffMissiles(prev, curr map[domain.MissileID]domain.Missile) (added, updated []domain.Missile, removed []domain.MissileID) {
	for id, m := range curr {
		pv, existed := prev[id]
		switch {
		case !existed:
			added = append(added, m)
		case pv != m:
			updated = append(updated, m)
		}
	}
	for id := range prev {
		if _, still := curr[id]; !still {
			removed = append(removed, id)
		}
	}
	return added, updated, removed
}

// filterMissileImpactsForAOI keeps only impacts whose Pos is inside the
// subscriber's AOI window. radius==0 disables the filter.
func filterMissileImpactsForAOI(imps []MissileImpact, center domain.Vec2, radius float64) []MissileImpact {
	if len(imps) == 0 {
		return nil
	}
	if radius <= 0 {
		out := make([]MissileImpact, len(imps))
		copy(out, imps)
		return out
	}
	r2 := radius * radius
	var out []MissileImpact
	for _, imp := range imps {
		if pointInRadius2(imp.Pos, center, r2) {
			out = append(out, imp)
		}
	}
	return out
}

// filterLaserEffectsForAOI keeps only the beams whose start or end
// point is inside the subscriber's AOI window. radius==0 disables the
// filter and returns the full slice (rare — only zero-radius
// subscribers, mostly tests). Returns nil when nothing is in range so
// IsEmpty() works as before.
func filterLaserEffectsForAOI(beams []combat.LaserBeam, center domain.Vec2, radius float64) []combat.LaserBeam {
	if len(beams) == 0 {
		return nil
	}
	if radius <= 0 {
		out := make([]combat.LaserBeam, len(beams))
		copy(out, beams)
		return out
	}
	r2 := radius * radius
	var out []combat.LaserBeam
	for _, b := range beams {
		if pointInRadius2(b.From, center, r2) || pointInRadius2(b.To, center, r2) {
			out = append(out, b)
		}
	}
	return out
}

func pointInRadius2(p, center domain.Vec2, r2 float64) bool {
	dx := p.X - center.X
	dy := p.Y - center.Y
	return dx*dx+dy*dy <= r2
}

// playerShip returns the first ship owned by playerID in the sector, or nil
// when the player has none here (observer, just-jumped, unauthenticated). The
// caller centers the AOI window on it and reads its RadarRange (phase 10.20).
func playerShip(ships map[domain.ShipID]*domain.Ship, playerID domain.PlayerID) *domain.Ship {
	for _, s := range ships {
		if s.PlayerID == playerID {
			return s
		}
	}
	return nil
}

// hideStealthed drops cloaked ships (phase 10.20 L4) from a subscriber's
// visible set. A ship with up_hide fitted (IsHidden) and no live AttackTarget
// is hidden from other players, except: the owner (own ships always visible),
// allies (relations Friend), and hostiles within stealthDetect of the
// subscriber's centre (close detection). A cloaked ship that is firing
// (AttackTarget set) is surfaced. So is a cloaked ship whose energy ran dry
// (Energy<=0): up_hide is an "always" energy consumer, and an unpowered cloak
// fails (phase 10.3.1). Deleting from the map mid-range is safe in Go.
func hideStealthed(visible map[domain.ShipID]struct{}, ships map[domain.ShipID]*domain.Ship, sub *Subscription, relations Relations, stealthDetect float64) {
	d2 := stealthDetect * stealthDetect
	for id := range visible {
		ship := ships[id]
		if ship == nil || !ship.IsHidden || ship.AttackTarget != nil || ship.MissileJustFired || ship.Energy <= 0 {
			continue
		}
		if ship.PlayerID == sub.PlayerID {
			continue
		}
		dx := ship.Pos.X - sub.Center.X
		dy := ship.Pos.Y - sub.Center.Y
		if dx*dx+dy*dy <= d2 {
			continue
		}
		if relations != nil &&
			relations.Get(domain.PlayerRef(ship.PlayerID), domain.PlayerRef(sub.PlayerID)) == domain.RelationFriend {
			continue
		}
		delete(visible, id)
	}
}

// diffStaticRefs returns the refs present in curr but not in prev (phase 10.20
// L2). Called both directions: prev→curr gives newly-added, curr→prev (args
// swapped) gives removed.
func diffStaticRefs(prev, curr map[domain.EntityRef]struct{}) []domain.EntityRef {
	var out []domain.EntityRef
	for ref := range curr {
		if _, had := prev[ref]; !had {
			out = append(out, ref)
		}
	}
	return out
}

// radarOrFallback returns r when it is a positive personal-radar radius,
// otherwise fallback (cfg.AOIRadius) — keeping the pre-10.20 flat behaviour for
// ships without a class radar.
func radarOrFallback(r, fallback float64) float64 {
	if r > 0 {
		return r
	}
	return fallback
}
