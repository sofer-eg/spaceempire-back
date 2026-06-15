package sector

import (
	"context"
	"errors"
	"math"
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

var (
	// ErrShipNotFound is reported when a command targets an unknown ship.
	ErrShipNotFound = errors.New("sector: ship not found")
	// ErrForbidden is reported when a player tries to act on a ship that
	// is not theirs. HTTP handlers translate this to 403.
	ErrForbidden = errors.New("sector: forbidden")
	// ErrShipExists is reported when AddShipCommand collides with an
	// already-registered ship id in the worker's RAM state.
	ErrShipExists = errors.New("sector: ship already in sector")
	// ErrSectorNotFound is reported when a Send/Snapshot/Subscribe targets a
	// sector that no worker in the pool owns.
	ErrSectorNotFound = errors.New("sector: sector not owned by any worker")
	// ErrShipDocked is reported when MoveCommand or SetCourseCommand fires
	// on a ship that is currently docked. The player must undock first.
	ErrShipDocked = errors.New("sector: ship is docked")
	// ErrInvalidAttackTarget is reported by AttackCommand when the target
	// reference is not a ship (phase 4.2 only supports ship targets) or
	// points at the attacker itself.
	ErrInvalidAttackTarget = errors.New("sector: invalid attack target")
	// ErrContainerNotFound is reported by PickupContainerCommand when the
	// container id is not in the sector (already picked up / expired).
	ErrContainerNotFound = errors.New("sector: container not found")
	// ErrContainerOutOfRange is reported by PickupContainerCommand when the
	// ship is farther than PickupRange from the container.
	ErrContainerOutOfRange = errors.New("sector: container out of range")
	// ErrEquipmentRequired is reported when a command needs a capability module
	// the ship has not installed: up_launcher for missiles (phase 10.14b),
	// up_drone_control for drones (phase 10.14b), up_autopilot for SetCourseCommand
	// (phase 10.3.11). HTTP maps it to 422.
	ErrEquipmentRequired = errors.New("sector: required equipment not installed")
	// ErrDroneCapReached is reported by LaunchDroneCommand when the ship already
	// flies as many live drones as its up_drone_control level allows (10.14b).
	ErrDroneCapReached = errors.New("sector: drone control capacity reached")
	// ErrNotEnoughEnergy is reported when an "action" energy module cannot fire
	// because the ship's Energy is below the action's cost (phase 10.3.1):
	// launching a missile spends the launcher's energy_usage. HTTP maps it to 422.
	ErrNotEnoughEnergy = errors.New("sector: not enough energy")
)

// shipEquipmentLevel returns the install level of the first module of the given
// type on the ship, or 0 when none is installed. Capability gates (10.14b) read
// presence (level >= 1) or the level itself (e.g. up_drone_control cap).
func shipEquipmentLevel(ship *domain.Ship, typ string) int {
	for _, m := range ship.Equipment {
		if m.Type == typ {
			if m.Level < 1 {
				return 1
			}
			return m.Level
		}
	}
	return 0
}

type CmdResult struct {
	Err error
}

// Command is applied by the worker at the start of a tick. It receives the
// owning Worker (for shared counters and logging) and the sectorState the
// command was routed to.
type Command interface {
	apply(w *Worker, s *sectorState)
}

// MoveCommand sets a ship's target. Ownership is enforced: the command is
// rejected with ErrForbidden when PlayerID does not match the ship's owner.
type MoveCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.Vec2
	// TargetRef, when non-nil, names the entity the player clicked on so the
	// SPA can paint a persistent "current target" highlight while the ship
	// approaches it. Nil means "move to a bare point" (canvas empty click)
	// and clears any previous highlight ref. Does not affect physics.
	TargetRef *domain.EntityRef
	// Reply, when non-nil, receives CmdResult after the command runs.
	// Must be buffered (cap >= 1).
	Reply chan<- CmdResult
}

func (c MoveCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	default:
		if ship.Docked != nil {
			if err := executeUndock(w, s, ship); err != nil {
				res.Err = err
				replyOnce(c.Reply, res)
				return
			}
		}
		target := c.Target
		ship.Target = &target
		ship.CurrentTargetRef = cloneEntityRef(c.TargetRef)
		s.markDirty(c.ShipID)
	}
	replyOnce(c.Reply, res)
}

// SetCourseCommand arms the autopilot on a ship: subsequent ticks will
// resolve FinalTarget into a per-tick waypoint and auto-jump through
// gates. Ownership is enforced just like MoveCommand. A nil Course clears
// the autopilot.
type SetCourseCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Course   *domain.Course
	Reply    chan<- CmdResult
}

func (c SetCourseCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case c.Course != nil && shipEquipmentLevel(ship, "up_autopilot") < 1:
		// Player autopilot is gated on an installed up_autopilot module
		// (phase 10.3.11). NPC ships arm their course directly in ai.go,
		// bypassing this command, so they keep flying without a module.
		// Clearing the course (Course == nil) is always allowed — a ship
		// can stop regardless of its fit.
		res.Err = ErrEquipmentRequired
	default:
		if ship.Docked != nil {
			if err := executeUndock(w, s, ship); err != nil {
				res.Err = err
				replyOnce(c.Reply, res)
				return
			}
		}
		ship.FinalTarget = cloneCourse(c.Course)
		// Drop the current per-tick target so the autopilot recomputes it
		// from FinalTarget on the next resolveAutopilot call. Without this,
		// a leftover MoveCommand target could send the ship sideways for
		// one tick before the autopilot overwrites it.
		ship.Target = nil
		// Mirror the new course's Approach into CurrentTargetRef so the SPA
		// highlights the parked-static even before the ship arrives. A
		// course without Approach clears any prior highlight ref.
		if c.Course != nil && c.Course.Approach != nil {
			ship.CurrentTargetRef = cloneEntityRef(c.Course.Approach)
		} else {
			ship.CurrentTargetRef = nil
		}
		s.markDirty(c.ShipID)
	}
	replyOnce(c.Reply, res)
}

// AddShipCommand registers a fully-formed ship into the worker's RAM
// state. Used at runtime when a new player registers and the spawner
// has already INSERTed the row — the worker mirrors that in memory so
// other players see the ship on the next tick. Pass a non-zero Ship.ID;
// the worker treats ID collisions as ErrShipExists.
//
// For a runtime NPC spawn (phase 9.5 invasion, and the deferred quest-NPC
// spawn from 8.17) set ControllerKind + StateJSON: the worker rebuilds the
// AI controller from the registry and registers it so the ship is AI-driven
// from the same tick. The DB rows (ship + ai_state) are persisted by the
// caller before Send, mirroring the player spawner. ControllerKind=="" keeps
// the player-spawn behaviour (no controller).
type AddShipCommand struct {
	Ship           domain.Ship
	ControllerKind string
	StateJSON      []byte
	Reply          chan<- CmdResult
}

func (c AddShipCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	if _, exists := s.ships[c.Ship.ID]; exists {
		res.Err = ErrShipExists
		replyOnce(c.Reply, res)
		return
	}
	ship := c.Ship
	ship.Target = cloneVec2(ship.Target)
	ship.FinalTarget = cloneCourse(ship.FinalTarget)
	ship.Docked = cloneEntityRef(ship.Docked)
	ship.AttackTarget = cloneEntityRef(ship.AttackTarget)
	ship.CurrentTargetRef = cloneEntityRef(ship.CurrentTargetRef)
	ship.Equipment = cloneEquipment(ship.Equipment)
	ship.PassengerPlayers = clonePlayerIDs(ship.PassengerPlayers) // phase 10.23
	ship.IsHidden = cloakEngagedFromEquipment(ship.Equipment)     // phase 10.20 L4
	s.ships[ship.ID] = &ship
	s.markDirty(ship.ID)
	// NPC spawn (9.5): hydrate the controller so the ship acts this tick.
	// A build error is logged and the ship stays controller-less rather than
	// aborting the add (it is already persisted and in RAM).
	if c.ControllerKind != "" && w.aiRegistry != nil {
		ctrl, err := w.aiRegistry.Build(c.ControllerKind, c.StateJSON)
		if err != nil {
			w.logger.Error("add ship: build controller",
				"err", err, "ship", int64(ship.ID), "kind", c.ControllerKind,
				"sector", int64(s.sectorID))
		} else {
			s.controllers[ship.ID] = ctrl
		}
	}
	replyOnce(c.Reply, res)
}

// RemoveShipCommand is the despawn counterpart of AddShipCommand (phase 8.18):
// it drops a ship from the worker's RAM state and deletes its DB row (ai_state
// cascades). Used to clean up quest NPCs when a quest reaches a terminal state.
// Idempotent — a missing ship replies nil (it may already have been killed).
type RemoveShipCommand struct {
	ShipID domain.ShipID
	Reply  chan<- CmdResult
}

func (c RemoveShipCommand) apply(w *Worker, s *sectorState) {
	if _, ok := s.ships[c.ShipID]; !ok {
		replyOnce(c.Reply, CmdResult{})
		return
	}
	delete(s.ships, c.ShipID)
	delete(s.controllers, c.ShipID)
	delete(s.dirty, c.ShipID)
	delete(s.policeScanCooldown, c.ShipID)
	// The ship was in RAM, so its row exists; a delete error is logged but the
	// RAM removal already stands.
	if w.repo != nil {
		if err := w.repo.Delete(context.Background(), c.ShipID); err != nil {
			w.logger.Error("remove ship: delete row",
				"err", err, "ship", int64(c.ShipID), "sector", int64(s.sectorID))
		}
	}
	replyOnce(c.Reply, CmdResult{})
}

// UpdateShipEquipmentCommand applies a recomputed equipment fit to a ship
// already in RAM (phase 10.14). The outfit service validates the request,
// debits cash and persists the new equipment + folded stats in a transaction,
// then sends this so the worker's authoritative copy matches the DB. Ownership
// is enforced. Current shield/energy are clamped to the (possibly lowered)
// maxima so an uninstall cannot leave a pool above its cap.
type UpdateShipEquipmentCommand struct {
	PlayerID       domain.PlayerID
	ShipID         domain.ShipID
	Equipment      []domain.InstalledEquipment
	MaxSpeed       float64
	Acceleration   float64
	MaxShield      int
	ShieldRecharge int
	MaxEnergy      int
	EnergyRecharge int
	EnergyDelta    int
	LaserDamage    int
	RadarRange     float64
	TurnRate       float64
	Reply          chan<- CmdResult
}

func (c UpdateShipEquipmentCommand) apply(_ *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	default:
		ship.Equipment = cloneEquipment(c.Equipment)
		ship.IsHidden = cloakEngagedFromEquipment(ship.Equipment) // phase 10.20 L4
		ship.MaxSpeed = c.MaxSpeed
		ship.Acceleration = c.Acceleration
		ship.MaxShield = c.MaxShield
		ship.ShieldRecharge = c.ShieldRecharge
		ship.MaxEnergy = c.MaxEnergy
		ship.EnergyRecharge = c.EnergyRecharge
		ship.EnergyDelta = c.EnergyDelta
		ship.LaserDamage = c.LaserDamage
		ship.RadarRange = c.RadarRange
		ship.TurnRate = c.TurnRate
		if ship.Shield > ship.MaxShield {
			ship.Shield = ship.MaxShield
		}
		if ship.Energy > ship.MaxEnergy {
			ship.Energy = ship.MaxEnergy
		}
		s.markDirty(c.ShipID)
	}
	replyOnce(c.Reply, res)
}

// AttackCommand sets a ship's AttackTarget. Phase 4.2 only supports
// EntityKindShip targets; other kinds reply ErrInvalidAttackTarget.
// Self-attack (Target.ID == ShipID) is also rejected. On success the
// worker writes the new AttackTarget immediately via repo.Save so a
// crash between ticks does not lose the player's intent.
type AttackCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	Reply    chan<- CmdResult
}

func (c AttackCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case c.Target.Kind != domain.EntityKindShip:
		res.Err = ErrInvalidAttackTarget
	case domain.ShipID(c.Target.ID) == c.ShipID:
		res.Err = ErrInvalidAttackTarget
	default:
		target := c.Target
		ship.AttackTarget = &target
		s.markDirty(c.ShipID)
		w.immediateSave(ship)
	}
	replyOnce(c.Reply, res)
}

// CeaseFireCommand clears a ship's AttackTarget. Idempotent — a
// ship that is not attacking returns nil error.
type CeaseFireCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Reply    chan<- CmdResult
}

func (c CeaseFireCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	default:
		if ship.AttackTarget != nil {
			ship.AttackTarget = nil
			s.markDirty(c.ShipID)
			w.immediateSave(ship)
		}
	}
	replyOnce(c.Reply, res)
}

// LaunchMissileResult carries the freshly allocated missile id back to
// the HTTP handler so the response can echo it for client-side tracking.
// On error MissileID is zero and Err is non-nil.
type LaunchMissileResult struct {
	Err       error
	MissileID domain.MissileID
}

// LaunchMissileCommand spawns one homing missile from ShipID at Target.
// Ownership is enforced (PlayerID must match the launcher's owner).
// Phase 4.3 only accepts EntityKindShip targets in the same sector;
// other kinds and self-targeting are rejected with ErrInvalidAttackTarget.
// Cargo accounting (1 missile consumed) happens outside the worker —
// the HTTP handler debits cargo before Send, refunds on reply.Err.
type LaunchMissileCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	// Now lets tests inject a deterministic clock; production wiring leaves
	// it zero and the worker substitutes its own clock.Now(). Keeping the
	// resolved time on the command (instead of reading w.clock inside apply)
	// makes the unit test path independent of any clock plumbing.
	Now time.Time
	// EnergyCost is the "action" energy a launch spends (phase 10.3.1), sourced
	// from the launcher's catalog energy_usage by the HTTP handler. The worker
	// rejects the launch with ErrNotEnoughEnergy when Energy < EnergyCost and
	// otherwise debits it. Zero (the test/legacy default) disables the gate.
	EnergyCost int
	Reply      chan<- LaunchMissileResult
}

func (c LaunchMissileCommand) apply(w *Worker, s *sectorState) {
	var res LaunchMissileResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
		replyLaunchMissile(c.Reply, res)
		return
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
		replyLaunchMissile(c.Reply, res)
		return
	case ship.Docked != nil:
		res.Err = ErrShipDocked
		replyLaunchMissile(c.Reply, res)
		return
	case shipEquipmentLevel(ship, "up_launcher") < 1:
		// Phase 10.14b: missiles require an installed launcher (up_launcher),
		// mirroring the original StarWind capability gate.
		res.Err = ErrEquipmentRequired
		replyLaunchMissile(c.Reply, res)
		return
	case c.Target.Kind != domain.EntityKindShip:
		res.Err = ErrInvalidAttackTarget
		replyLaunchMissile(c.Reply, res)
		return
	case domain.ShipID(c.Target.ID) == c.ShipID:
		res.Err = ErrInvalidAttackTarget
		replyLaunchMissile(c.Reply, res)
		return
	}

	target, ok := s.ships[domain.ShipID(c.Target.ID)]
	if !ok || target.HP <= 0 {
		res.Err = ErrInvalidAttackTarget
		replyLaunchMissile(c.Reply, res)
		return
	}

	// Phase 10.3.1: a launch is an "action" energy expense. Reject when the
	// pool cannot cover the launcher's cost; debit it on success so repeated
	// fire drains the ship until it recharges. Cost 0 disables the gate (tests).
	if ship.Energy < c.EnergyCost {
		res.Err = ErrNotEnoughEnergy
		replyLaunchMissile(c.Reply, res)
		return
	}
	if c.EnergyCost > 0 {
		ship.Energy -= c.EnergyCost
		s.markDirty(c.ShipID)
	}

	now := c.Now
	if now.IsZero() {
		now = w.clock.Now()
	}
	id := s.allocMissileID()
	m := combat.LaunchMissile(id, missileSpec, ship, c.Target, target.Pos, now)
	s.missiles[id] = m
	res.MissileID = id
	if ship.IsHidden {
		ship.MissileJustFired = true // reveal for this tick's snapshot (phase 10.20a)
	}
	replyLaunchMissile(c.Reply, res)
}

func replyLaunchMissile(reply chan<- LaunchMissileResult, res LaunchMissileResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}

// LaunchDroneResult reports how many drones were actually spawned. The
// HTTP handler debits Count units up front and refunds (Count - Spawned)
// so a partial DB failure does not silently swallow the player's cargo.
type LaunchDroneResult struct {
	Err     error
	Spawned int
}

// LaunchDroneCommand spawns Count combat drones from ShipID, each launched
// at Target. Ownership is enforced; the target must be a live ship in the
// same sector (phase 4.4: explicitly-assigned target only, see
// drones.md §4). Each drone is INSERTed immediately so it survives a
// restart; the assigned id is the DB primary key (or a fallback counter
// when no DroneRepo is wired). Cargo accounting happens in the handler.
type LaunchDroneCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	Count    int
	// Now lets tests inject a deterministic clock; zero means the worker
	// substitutes its own clock.Now(). Same convention as
	// LaunchMissileCommand.
	Now   time.Time
	Reply chan<- LaunchDroneResult
}

func (c LaunchDroneCommand) apply(w *Worker, s *sectorState) {
	var res LaunchDroneResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	case shipEquipmentLevel(ship, "up_drone_control") < 1:
		// Phase 10.14b: drones require a drone-control module; its level caps
		// how many may fly at once (see cap check below).
		res.Err = ErrEquipmentRequired
	case c.Target.Kind != domain.EntityKindShip:
		res.Err = ErrInvalidAttackTarget
	case domain.ShipID(c.Target.ID) == c.ShipID:
		res.Err = ErrInvalidAttackTarget
	case c.Count <= 0:
		res.Err = ErrInvalidAttackTarget
	}
	if res.Err != nil {
		replyLaunchDrone(c.Reply, res)
		return
	}

	if target, ok := s.ships[domain.ShipID(c.Target.ID)]; !ok || target.HP <= 0 {
		res.Err = ErrInvalidAttackTarget
		replyLaunchDrone(c.Reply, res)
		return
	}

	// Phase 10.14b: cap the salvo so live drones never exceed the
	// up_drone_control level. The handler refunds the unspawned remainder.
	cap := shipEquipmentLevel(ship, "up_drone_control")
	live := s.liveDroneCount(c.ShipID)
	allowed := cap - live
	if allowed <= 0 {
		res.Err = ErrDroneCapReached
		replyLaunchDrone(c.Reply, res)
		return
	}
	toSpawn := c.Count
	if toSpawn > allowed {
		toSpawn = allowed
	}

	now := c.Now
	if now.IsZero() {
		now = w.clock.Now()
	}
	for i := 0; i < toSpawn; i++ {
		d := combat.LaunchDrone(0, droneSpec, ship, c.Target, now)
		nudgeDroneSpawn(d, i, toSpawn)

		var id domain.DroneID
		if w.droneRepo != nil {
			created, err := w.droneRepo.Create(context.Background(), *d)
			if err != nil {
				w.logger.Error("drone create failed",
					"err", err, "ship", int64(c.ShipID), "sector", int64(s.sectorID))
				break
			}
			id = created
		} else {
			id = s.allocDroneID()
		}
		d.ID = id
		s.drones[id] = d
		s.markDroneDirty(id)
		res.Spawned++
	}
	replyLaunchDrone(c.Reply, res)
}

// nudgeDroneSpawn offsets a freshly-launched drone onto a small ring
// around the owner so a salvo does not stack pixel-perfect on the launch
// point. Deterministic (no rand) for reproducible tests.
func nudgeDroneSpawn(d *domain.Drone, i, count int) {
	const r = 12.0
	a := 2 * math.Pi * float64(i) / float64(count)
	d.Pos = d.Pos.Add(domain.Vec2{X: r * math.Cos(a), Y: r * math.Sin(a)})
}

func replyLaunchDrone(reply chan<- LaunchDroneResult, res LaunchDroneResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}

// RecallDronesResult reports how many still-alive drones were recalled.
// The HTTP handler refunds that many cargo units.
type RecallDronesResult struct {
	Err      error
	Recalled int
}

// RecallDronesCommand removes every live drone owned by ShipID (returning
// them to cargo, handled by the handler). Ownership is enforced.
type RecallDronesCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Reply    chan<- RecallDronesResult
}

func (c RecallDronesCommand) apply(w *Worker, s *sectorState) {
	var res RecallDronesResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
		replyRecallDrones(c.Reply, res)
		return
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
		replyRecallDrones(c.Reply, res)
		return
	}

	var ids []domain.DroneID
	for id, d := range s.drones {
		if d.OwnerShipID == c.ShipID {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		// A recalled drone just vanishes — the DronesRemoved diff tells the
		// SPA; no explosion impact (unlike TTL/owner-loss self-destruct).
		delete(s.drones, id)
		delete(s.dronesDirty, id)
		if w.droneRepo != nil {
			if err := w.droneRepo.Delete(context.Background(), id); err != nil {
				w.logger.Error("drone recall delete failed",
					"err", err, "drone", int64(id), "sector", int64(s.sectorID))
			}
		}
		res.Recalled++
	}
	replyRecallDrones(c.Reply, res)
}

func replyRecallDrones(reply chan<- RecallDronesResult, res RecallDronesResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}

func replyOnce(reply chan<- CmdResult, res CmdResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}
