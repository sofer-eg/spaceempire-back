package sector

import (
	"sort"
	"sync/atomic"
	"time"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// sectorState is the per-sector slice of mutable state a Worker owns. A
// Worker holds many of these in its sectors map; every tick walks them in
// turn. All fields are mutated only from the Worker's tick goroutine — the
// one-writer-per-sector invariant.
type sectorState struct {
	sectorID domain.SectorID

	ships map[domain.ShipID]*domain.Ship
	dirty map[domain.ShipID]bool

	// statics holds the immutable static objects (stations, shipyards,
	// trade stations, pirbases) loaded once at worker startup. In phase
	// 3.1 they don't mutate; later phases (building, destruction) will
	// add dedicated channels and dirty tracking.
	statics domain.SectorStatics

	subs map[uint64]*Subscription

	tick         uint64
	lastSnapshot time.Time
	lastDuration time.Duration
	// timeScale is the sector's time-dilation factor (phase 7.2): 1.0 = real
	// time, < 1.0 = slowed under overload. Mutated only from the tick
	// goroutine; reported via the metrics sink. Defaults to 1.0.
	timeScale float64

	// outbound/inbound count handoffs by counterpart sector. Mutated only
	// from the tick goroutine; published into Snapshot.HandoffsOut /
	// HandoffsIn so Stats() can read consistently from any goroutine.
	outbound map[domain.SectorID]uint64
	inbound  map[domain.SectorID]uint64

	// productionCycles counts completed production cycles since worker
	// start. Mutated only from the tick goroutine, published into
	// Snapshot.ProductionCycles.
	productionCycles uint64

	// laserEffects accumulates one-frame combat effects (laser beams)
	// for the current tick. publishSnapshotFor copies them into the
	// outgoing Snapshot, then clearLaserEffects empties the slice so
	// the next tick starts fresh. Mutated only from the tick goroutine.
	laserEffects []combat.LaserBeam

	// missiles holds the live in-flight missiles for this sector.
	// Reconstructable state — never persisted. nextMissileID is the
	// monotonic id allocator inside this worker; restarts reset it,
	// which is fine because no other code keeps references to dead
	// missile ids.
	missiles      map[domain.MissileID]*domain.Missile
	nextMissileID domain.MissileID
	// missileImpacts accumulates one-frame missile events (hit /
	// expire) for the current tick. Same lifecycle as laserEffects.
	missileImpacts []MissileImpact

	// drones holds the live combat drones for this sector. Unlike
	// missiles, drones are persistent state (see drones.md §3): launch
	// INSERTs, death/recall DELETEs, dronesDirty drives the periodic
	// BatchUpdate. nextDroneID is only used as a fallback id allocator
	// when no DroneRepo is wired (pure unit tests) — with a repo the id
	// is the DB-assigned primary key.
	drones            map[domain.DroneID]*domain.Drone
	dronesDirty       map[domain.DroneID]bool
	nextDroneID       domain.DroneID
	lastDroneSnapshot time.Time
	// droneImpacts accumulates one-frame drone events (shot fired /
	// death / expire) for the current tick. Same lifecycle as
	// laserEffects.
	droneImpacts []DroneImpact

	// torpedos holds the live homing torpedoes for this sector (ЧТЗ doc-1
	// §3 FR-001). Like drones they are persistent state: launch INSERTs,
	// death/detonation/expire DELETEs, torpedosDirty drives the periodic
	// BatchUpdate. nextTorpedoID is only used as a fallback id allocator
	// when no TorpedoRepo is wired (pure unit tests) — with a repo the id
	// is the DB-assigned primary key.
	torpedos            map[domain.TorpedoID]*domain.Torpedo
	torpedosDirty       map[domain.TorpedoID]bool
	nextTorpedoID       domain.TorpedoID
	lastTorpedoSnapshot time.Time
	// torpedoImpacts accumulates one-frame torpedo events (detonation /
	// expire / owner-loss) for the current tick. Same one-frame lifecycle
	// as droneImpacts; surfacing them in the Snapshot/AOI is a later
	// sub-task (TASK-100.3.5.7), so they stay internal for now.
	torpedoImpacts []TorpedoImpact

	// containers holds the live loot containers in this sector (phase
	// 4.6). Persistent (immediate writes via the container repo) but
	// immutable once created — the cargo inside changes only on pickup,
	// which removes the whole container. So no dirty-tracking and no
	// periodic batch: only added/removed deltas reach subscribers.
	containers map[domain.ContainerID]*domain.Container

	// asteroids holds the live minable ore bodies in this sector (phase
	// 5.4). Persistent: mass is mined down in RAM, written by the periodic
	// BatchUpdate (asteroidsDirty drives it, gated by lastAsteroidSnapshot),
	// and the row is deleted immediately when depleted. Pos/OreType are
	// immutable, so only mass is ever batched.
	asteroids            map[domain.AsteroidID]*domain.Asteroid
	asteroidsDirty       map[domain.AsteroidID]bool
	lastAsteroidSnapshot time.Time

	// destructibles holds the mutable combat state (HP/Shield) of every
	// static object in the sector (phase 6.2b), keyed by EntityRef. Built
	// once at cold-start from `statics`; lasers damage them, chargeStatics
	// recharges shields, killStatic removes them. RAM-only this phase — not
	// persisted, so a restart resets statics to full/undestroyed.
	// destructiblesDirty drives the per-tick WS combat delta; staticsRemoved
	// is the one-frame list of statics destroyed this tick.
	destructibles      map[domain.EntityRef]*domain.DestructibleStatic
	destructiblesDirty map[domain.EntityRef]bool
	staticsRemoved     []domain.EntityRef

	// nextSatelliteID is a fallback id allocator for installed navigation
	// satellites (phase 10.15) used only when no SatelliteRepo is wired (pure
	// unit tests). With a repo the id is the DB-assigned primary key.
	nextSatelliteID domain.SatelliteID

	// controllers maps each AI-driven ship in this sector to its NPC
	// controller (phase 5.1). Built once at cold-start from the ai_state
	// rows (buildControllers) and pruned when a controlled ship leaves the
	// sector or dies. nil until buildControllers runs; tickAI/persistAIState
	// tolerate an empty map. State is snapshotted to ai_state every
	// SnapshotInterval, gated by lastAISnapshot.
	controllers    map[domain.ShipID]ai.Controller
	lastAISnapshot time.Time

	// policeScanCooldown maps a player ship to the tick at which it may be
	// scanned again (phase 9.4): after any police scan the ship is exempt for
	// PoliceConfig.CooldownTicks, so police do not re-read the same hold every
	// tick. Mutated only from the tick goroutine.
	policeScanCooldown map[domain.ShipID]uint64

	snap atomic.Pointer[Snapshot]
}

func newSectorState(id domain.SectorID, initial []domain.Ship, initialDrones []domain.Drone, initialTorpedos []domain.Torpedo, initialContainers []domain.Container, initialAsteroids []domain.Asteroid, statics domain.SectorStatics, now time.Time) *sectorState {
	ships := make(map[domain.ShipID]*domain.Ship, len(initial))
	for i := range initial {
		s := initial[i]
		s.Target = cloneVec2(s.Target)
		s.FinalTarget = cloneCourse(s.FinalTarget)
		s.Docked = cloneEntityRef(s.Docked)
		s.AttackTarget = cloneEntityRef(s.AttackTarget)
		s.MiningTarget = cloneAsteroidID(s.MiningTarget)
		// CurrentTargetRef is not persisted; cold-start derives it from
		// FinalTarget.Approach so the SPA highlight survives a worker
		// restart while the autopilot is still parked or approaching.
		// Manual MoveCommand refs are intentionally lost — the player can
		// re-click to restore them.
		s.CurrentTargetRef = cloneEntityRef(s.CurrentTargetRef)
		if s.CurrentTargetRef == nil && s.FinalTarget != nil && s.FinalTarget.Approach != nil {
			s.CurrentTargetRef = cloneEntityRef(s.FinalTarget.Approach)
		}
		// Phase 10.20 L4: derive the cloak flag from the loaded equipment.
		s.IsHidden = cloakEngagedFromEquipment(s.Equipment)
		ships[s.ID] = &s
	}
	drones := make(map[domain.DroneID]*domain.Drone, len(initialDrones))
	var maxDroneID domain.DroneID
	for i := range initialDrones {
		d := initialDrones[i]
		drones[d.ID] = &d
		if d.ID > maxDroneID {
			maxDroneID = d.ID
		}
	}
	torpedos := make(map[domain.TorpedoID]*domain.Torpedo, len(initialTorpedos))
	var maxTorpedoID domain.TorpedoID
	for i := range initialTorpedos {
		tp := initialTorpedos[i]
		torpedos[tp.ID] = &tp
		if tp.ID > maxTorpedoID {
			maxTorpedoID = tp.ID
		}
	}
	containers := make(map[domain.ContainerID]*domain.Container, len(initialContainers))
	for i := range initialContainers {
		c := initialContainers[i]
		containers[c.ID] = &c
	}
	asteroids := make(map[domain.AsteroidID]*domain.Asteroid, len(initialAsteroids))
	for i := range initialAsteroids {
		a := initialAsteroids[i]
		asteroids[a.ID] = &a
	}
	destructibleList := domain.DestructiblesFromStatics(statics)
	destructibles := make(map[domain.EntityRef]*domain.DestructibleStatic, len(destructibleList))
	for i := range destructibleList {
		d := destructibleList[i]
		destructibles[d.Ref] = &d
	}
	st := &sectorState{
		sectorID:             id,
		ships:                ships,
		dirty:                make(map[domain.ShipID]bool),
		statics:              statics,
		subs:                 make(map[uint64]*Subscription),
		outbound:             make(map[domain.SectorID]uint64),
		inbound:              make(map[domain.SectorID]uint64),
		missiles:             make(map[domain.MissileID]*domain.Missile),
		drones:               drones,
		dronesDirty:          make(map[domain.DroneID]bool),
		nextDroneID:          maxDroneID,
		lastDroneSnapshot:    now,
		torpedos:             torpedos,
		torpedosDirty:        make(map[domain.TorpedoID]bool),
		nextTorpedoID:        maxTorpedoID,
		lastTorpedoSnapshot:  now,
		containers:           containers,
		asteroids:            asteroids,
		asteroidsDirty:       make(map[domain.AsteroidID]bool),
		lastAsteroidSnapshot: now,
		destructibles:        destructibles,
		destructiblesDirty:   make(map[domain.EntityRef]bool),
		policeScanCooldown:   make(map[domain.ShipID]uint64),
		lastAISnapshot:       now,
		lastSnapshot:         now,
		timeScale:            1.0,
	}
	publishSnapshotFor(st, 0)
	return st
}

func (s *sectorState) markDirty(id domain.ShipID) {
	s.dirty[id] = true
}

// addLaserEffect records a per-tick laser shot for inclusion in the
// next published Snapshot. Subscribers receive it as a one-frame
// rendering hint.
func (s *sectorState) addLaserEffect(b combat.LaserBeam) {
	s.laserEffects = append(s.laserEffects, b)
}

// clearLaserEffects drops the accumulated per-tick effects after the
// snapshot has been published. The slice is reused (cap preserved) to
// keep steady-state allocations near zero.
func (s *sectorState) clearLaserEffects() {
	s.laserEffects = s.laserEffects[:0]
}

// addMissileImpact records a per-tick missile event (hit / expire) for
// inclusion in the next published Snapshot. Subscribers consume them as
// one-frame rendering hints, same lifecycle as laserEffects.
func (s *sectorState) addMissileImpact(imp MissileImpact) {
	s.missileImpacts = append(s.missileImpacts, imp)
}

// clearMissileImpacts drops the accumulated per-tick missile events
// after the snapshot has been published.
func (s *sectorState) clearMissileImpacts() {
	s.missileImpacts = s.missileImpacts[:0]
}

// allocMissileID returns a fresh monotonic id for a new missile in this
// worker. Called from the tick goroutine only.
func (s *sectorState) allocMissileID() domain.MissileID {
	s.nextMissileID++
	return s.nextMissileID
}

func (s *sectorState) markDroneDirty(id domain.DroneID) {
	s.dronesDirty[id] = true
}

// addDroneImpact records a per-tick drone event (shot fired / death /
// expire) for inclusion in the next published Snapshot. Same one-frame
// lifecycle as laserEffects / missileImpacts.
func (s *sectorState) addDroneImpact(imp DroneImpact) {
	s.droneImpacts = append(s.droneImpacts, imp)
}

// clearDroneImpacts drops the accumulated per-tick drone events after the
// snapshot has been published.
func (s *sectorState) clearDroneImpacts() {
	s.droneImpacts = s.droneImpacts[:0]
}

// allocDroneID returns a fallback monotonic id, used only when no
// DroneRepo is wired (pure unit tests). With a repo the DroneID is the
// DB-assigned primary key returned by Create.
func (s *sectorState) allocDroneID() domain.DroneID {
	s.nextDroneID++
	return s.nextDroneID
}

// collectDirtyDrones returns value copies of every currently-dirty drone.
// Ids removed since being marked dirty are skipped.
func (s *sectorState) collectDirtyDrones() []domain.Drone {
	out := make([]domain.Drone, 0, len(s.dronesDirty))
	for id := range s.dronesDirty {
		d, ok := s.drones[id]
		if !ok {
			continue
		}
		out = append(out, *d)
	}
	return out
}

func (s *sectorState) markTorpedoDirty(id domain.TorpedoID) {
	s.torpedosDirty[id] = true
}

// addTorpedoImpact records a per-tick torpedo event (detonation / expire /
// owner-loss) for the current tick. Same one-frame lifecycle as droneImpacts.
func (s *sectorState) addTorpedoImpact(imp TorpedoImpact) {
	s.torpedoImpacts = append(s.torpedoImpacts, imp)
}

// clearTorpedoImpacts drops the accumulated per-tick torpedo events after the
// tick has consumed them.
func (s *sectorState) clearTorpedoImpacts() {
	s.torpedoImpacts = s.torpedoImpacts[:0]
}

// allocTorpedoID returns a fallback monotonic id, used only when no TorpedoRepo
// is wired (pure unit tests). With a repo the TorpedoID is the DB-assigned
// primary key returned by Create.
func (s *sectorState) allocTorpedoID() domain.TorpedoID {
	s.nextTorpedoID++
	return s.nextTorpedoID
}

// collectDirtyTorpedos returns value copies of every currently-dirty torpedo.
// Ids removed since being marked dirty are skipped.
func (s *sectorState) collectDirtyTorpedos() []domain.Torpedo {
	out := make([]domain.Torpedo, 0, len(s.torpedosDirty))
	for id := range s.torpedosDirty {
		tp, ok := s.torpedos[id]
		if !ok {
			continue
		}
		out = append(out, *tp)
	}
	return out
}

// resolveTargetPos resolves the current position of a homing-weapon target and
// whether it is present and alive. A ship target resolves from s.ships (HP>0);
// any destructible static resolves from s.destructibles (HP>0). Shared by
// torpedoes and missiles (TASK-113 FR-07/FR-08): it gates both the launch (a
// dead/missing target must not spend energy or ammunition) and the per-tick
// homing destination.
func (s *sectorState) resolveTargetPos(ref domain.EntityRef) (domain.Vec2, bool) {
	if ref.Kind == domain.EntityKindShip {
		if ship, ok := s.ships[domain.ShipID(ref.ID)]; ok && ship.HP > 0 {
			return ship.Pos, true
		}
		return domain.Vec2{}, false
	}
	if d, ok := s.destructibles[ref]; ok && d.HP > 0 {
		return d.Pos, true
	}
	return domain.Vec2{}, false
}

func (s *sectorState) markAsteroidDirty(id domain.AsteroidID) {
	s.asteroidsDirty[id] = true
}

// removeAsteroid drops a depleted asteroid from the live set and clears its
// dirty flag (a deleted asteroid must not resurface in the next BatchUpdate).
func (s *sectorState) removeAsteroid(id domain.AsteroidID) {
	delete(s.asteroids, id)
	delete(s.asteroidsDirty, id)
}

// collectDirtyAsteroids returns value copies of every currently-dirty
// asteroid. Ids removed since being marked dirty are skipped.
func (s *sectorState) collectDirtyAsteroids() []domain.Asteroid {
	out := make([]domain.Asteroid, 0, len(s.asteroidsDirty))
	for id := range s.asteroidsDirty {
		a, ok := s.asteroids[id]
		if !ok {
			continue
		}
		out = append(out, *a)
	}
	return out
}

// snapshotAsteroids returns value copies of every live asteroid, sorted by
// id for determinism. Used by the WorldView the AI controllers see.
func (s *sectorState) snapshotAsteroids() []domain.Asteroid {
	out := make([]domain.Asteroid, 0, len(s.asteroids))
	for _, a := range s.asteroids {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *sectorState) markDestructibleDirty(ref domain.EntityRef) {
	s.destructiblesDirty[ref] = true
}

// removeDestructible drops a destroyed static from the live combat set and
// records its ref for the one-frame WS removal delta.
func (s *sectorState) removeDestructible(ref domain.EntityRef) {
	delete(s.destructibles, ref)
	delete(s.destructiblesDirty, ref)
	s.staticsRemoved = append(s.staticsRemoved, ref)
}

// collectDirtyDestructibles returns value copies of every destructible whose
// HP/Shield changed this tick (for the WS combat delta). Refs removed since
// being marked dirty are skipped.
func (s *sectorState) collectDirtyDestructibles() []domain.DestructibleStatic {
	out := make([]domain.DestructibleStatic, 0, len(s.destructiblesDirty))
	for ref := range s.destructiblesDirty {
		d, ok := s.destructibles[ref]
		if !ok {
			continue
		}
		out = append(out, *d)
	}
	return out
}

// snapshotDestructibles returns value copies of every live destructible,
// sorted by (kind, id) for determinism. Published in the Snapshot so
// /api/state and the AI/tests can read static combat state.
func (s *sectorState) snapshotDestructibles() []domain.DestructibleStatic {
	out := make([]domain.DestructibleStatic, 0, len(s.destructibles))
	for _, d := range s.destructibles {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ref.Kind != out[j].Ref.Kind {
			return out[i].Ref.Kind < out[j].Ref.Kind
		}
		return out[i].Ref.ID < out[j].Ref.ID
	})
	return out
}

// clearStaticCombatDeltas drops the per-tick static-combat deltas (the dirty
// set and the one-frame removed list) after the snapshot/patch has been
// published. Called at the end of tickSector.
func (s *sectorState) clearStaticCombatDeltas() {
	s.staticsRemoved = s.staticsRemoved[:0]
	if len(s.destructiblesDirty) > 0 {
		s.destructiblesDirty = make(map[domain.EntityRef]bool)
	}
}

// addContainer registers a freshly-created loot container in the live
// set. Called from the kill handler after RecordKill returns the
// DB-assigned ids.
func (s *sectorState) addContainer(c *domain.Container) {
	s.containers[c.ID] = c
}

// removeContainer drops a container from the live set (pickup / TTL).
func (s *sectorState) removeContainer(id domain.ContainerID) {
	delete(s.containers, id)
}

// allocSatelliteID returns a fallback monotonic id, used only when no
// SatelliteRepo is wired (pure unit tests). With a repo the SatelliteID is the
// DB-assigned primary key returned by Create.
func (s *sectorState) allocSatelliteID() domain.SatelliteID {
	s.nextSatelliteID++
	return s.nextSatelliteID
}

// addSatellite registers a freshly-installed navigation satellite (phase
// 10.15) in both the rendered layout and the live combat set. Called from the
// install command after the repo (when wired) returns the DB-assigned id, so
// the next L2 big-radar diff ships it to clients and lasers can damage it.
func (s *sectorState) addSatellite(sat domain.Satellite) {
	s.statics.Satellites = append(s.statics.Satellites, sat)
	d := domain.DestructibleStatic{
		Ref:            sat.ObjectID(),
		Pos:            sat.Pos,
		OwnerID:        sat.OwnerID,
		HP:             sat.HP,
		Shield:         sat.Shield,
		MaxShield:      sat.MaxShield,
		ShieldRecharge: sat.ShieldRecharge,
	}
	s.destructibles[d.Ref] = &d
}

// satellitesPresent reports whether the sector holds at least one live
// navigation satellite — the cheap once-per-tick gate for the radar reveal.
func (s *sectorState) satellitesPresent() bool {
	for _, sat := range s.statics.Satellites {
		if sat.Built {
			return true
		}
	}
	return false
}

// satelliteRevealsFor reports whether a built navigation satellite in the
// sector reveals it to player p (phase 10.20 L5): the player owns one, or is
// allied (clan/friend, per relations) to an owner. Owner-gated — mirrors the
// own/ally test in hideStealthed; an unowned satellite reveals to nobody.
func (s *sectorState) satelliteRevealsFor(p domain.PlayerID, relations Relations) bool {
	for i := range s.statics.Satellites {
		sat := s.statics.Satellites[i]
		if !sat.Built || sat.OwnerID == nil {
			continue
		}
		if *sat.OwnerID == p {
			return true
		}
		if relations != nil &&
			relations.Get(domain.PlayerRef(*sat.OwnerID), domain.PlayerRef(p)) == domain.RelationFriend {
			return true
		}
	}
	return false
}

func (s *sectorState) recordOutbound(to domain.SectorID) {
	s.outbound[to]++
}

func (s *sectorState) recordInbound(from domain.SectorID) {
	s.inbound[from]++
}

// handoffCopies returns defensive copies of the outbound/inbound maps so
// snapshots can be read without holding any lock on sectorState.
func (s *sectorState) handoffCopies() (out, in map[domain.SectorID]uint64) {
	if len(s.outbound) > 0 {
		out = make(map[domain.SectorID]uint64, len(s.outbound))
		for k, v := range s.outbound {
			out[k] = v
		}
	}
	if len(s.inbound) > 0 {
		in = make(map[domain.SectorID]uint64, len(s.inbound))
		for k, v := range s.inbound {
			in[k] = v
		}
	}
	return out, in
}

// collectDirty returns deep copies of every currently-dirty ship. Ids that
// have been removed since being marked dirty are skipped.
func (s *sectorState) collectDirty() []domain.Ship {
	out := make([]domain.Ship, 0, len(s.dirty))
	for id := range s.dirty {
		ship, ok := s.ships[id]
		if !ok {
			continue
		}
		cp := *ship
		cp.Target = cloneVec2(ship.Target)
		cp.FinalTarget = cloneCourse(ship.FinalTarget)
		cp.Docked = cloneEntityRef(ship.Docked)
		cp.AttackTarget = cloneEntityRef(ship.AttackTarget)
		cp.MiningTarget = cloneAsteroidID(ship.MiningTarget)
		cp.CurrentTargetRef = cloneEntityRef(ship.CurrentTargetRef)
		out = append(out, cp)
	}
	return out
}

func cloneVec2(v *domain.Vec2) *domain.Vec2 {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func cloneCourse(c *domain.Course) *domain.Course {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Approach = cloneEntityRef(c.Approach)
	return &cp
}

func cloneEntityRef(r *domain.EntityRef) *domain.EntityRef {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

// cloneAsteroidID deep-copies a ship's MiningTarget (phase 10.3.6) so a
// snapshot/handoff copy never aliases the worker's live pointer.
func cloneAsteroidID(a *domain.AsteroidID) *domain.AsteroidID {
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

// cloneEquipment returns an independent copy of an installed-equipment slice
// (phase 10.14) so the worker's RAM copy never aliases the command/spawn input.
func cloneEquipment(eq []domain.InstalledEquipment) []domain.InstalledEquipment {
	if len(eq) == 0 {
		return nil
	}
	return append([]domain.InstalledEquipment(nil), eq...)
}

// liveStaticRefs returns the set of refs for every static still alive (phase
// 10.20 L2). A fresh subscription seeds lastSentStatics with this so the first
// per-tick diff trims the welcome's full statics down to the big-radar window.
func (s *sectorState) liveStaticRefs() map[domain.EntityRef]struct{} {
	out := make(map[domain.EntityRef]struct{}, len(s.destructibles))
	for ref := range s.destructibles {
		out[ref] = struct{}{}
	}
	return out
}

// staticRefsInRadius returns the refs of live statics within radius of center
// (phase 10.20 L2 big radar). radius<=0 yields the empty set.
func (s *sectorState) staticRefsInRadius(center domain.Vec2, radius float64) map[domain.EntityRef]struct{} {
	out := make(map[domain.EntityRef]struct{})
	if radius <= 0 {
		return out
	}
	r2 := radius * radius
	for ref, d := range s.destructibles {
		dx := d.Pos.X - center.X
		dy := d.Pos.Y - center.Y
		if dx*dx+dy*dy <= r2 {
			out[ref] = struct{}{}
		}
	}
	return out
}

// collectStaticsByRefs builds a SectorStatics subset holding the full typed
// objects whose ref is in refs (phase 10.20 L2), so a patch can carry the
// statics that just entered the big-radar window for the client to render.
func (s *sectorState) collectStaticsByRefs(refs []domain.EntityRef) domain.SectorStatics {
	set := make(map[domain.EntityRef]struct{}, len(refs))
	for _, r := range refs {
		set[r] = struct{}{}
	}
	var out domain.SectorStatics
	for _, o := range s.statics.Stations {
		if _, ok := set[o.ObjectID()]; ok {
			out.Stations = append(out.Stations, o)
		}
	}
	for _, o := range s.statics.Shipyards {
		if _, ok := set[o.ObjectID()]; ok {
			out.Shipyards = append(out.Shipyards, o)
		}
	}
	for _, o := range s.statics.TradeStations {
		if _, ok := set[o.ObjectID()]; ok {
			out.TradeStations = append(out.TradeStations, o)
		}
	}
	for _, o := range s.statics.Pirbases {
		if _, ok := set[o.ObjectID()]; ok {
			out.Pirbases = append(out.Pirbases, o)
		}
	}
	for _, o := range s.statics.LaserTowers {
		ref := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: int64(o.ID)}
		if _, ok := set[ref]; ok {
			out.LaserTowers = append(out.LaserTowers, o)
		}
	}
	for _, o := range s.statics.Satellites {
		if _, ok := set[o.ObjectID()]; ok {
			out.Satellites = append(out.Satellites, o)
		}
	}
	return out
}

// upHideType is the ct_updates module that cloaks a ship (phase 10.20 L4).
const upHideType = "up_hide"

// cloakEngagedFromEquipment reports whether an up_hide module is fitted —
// the cached value of domain.Ship.IsHidden, refreshed at every RAM entry point.
func cloakEngagedFromEquipment(eq []domain.InstalledEquipment) bool {
	for _, m := range eq {
		if m.Type == upHideType {
			return true
		}
	}
	return false
}
