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
	// ErrAsteroidNotFound is reported by MineCommand when the target asteroid
	// id is not in the ship's sector (already depleted or wrong sector).
	ErrAsteroidNotFound = errors.New("sector: asteroid not found")
	// ErrAsteroidOutOfRange is reported by MineCommand when the ship is farther
	// than MineRange from the asteroid it tries to start mining.
	ErrAsteroidOutOfRange = errors.New("sector: asteroid out of range")
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
	// ErrInstallerUnavailable is reported by InstallJammerCommand /
	// InstallSatelliteCommand when the worker has no StaticInstaller wired
	// (TASK-144). The installer is the ONLY install path — it is what charges the
	// goods in the same transaction as the object INSERT — so a missing one is a
	// wiring fault, not a licence to deploy for free. HTTP maps it to 503.
	ErrInstallerUnavailable = errors.New("sector: static installer not wired")
	// ErrOrdnanceUnavailable is reported by LaunchMissileCommand /
	// LaunchTorpedoCommand / LaunchDroneCommand when the worker has no Ordnance
	// wired (TASK-147). The ordnance is the ONLY launch path — it is what charges
	// the ammunition in the same transaction as the projectile INSERTs — so a
	// missing one is a wiring fault, not a licence to fire for free. HTTP maps it
	// to 503. Paired with ErrInstallerUnavailable, same doctrine.
	ErrOrdnanceUnavailable = errors.New("sector: ordnance not wired")
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
		// A fresh move order ends any sustained mining (phase 10.3.6): the
		// player is flying the ship off station, so it can no longer drill.
		ship.MiningTarget = nil
		// A fresh move also abandons an in-progress external dock (phase
		// 10.3.23) — the clamps cannot engage while flying off.
		ship.ExternalDock = nil
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
		// Arming a course abandons an in-progress external dock (phase 10.3.23).
		ship.ExternalDock = nil
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
	ship.MiningTarget = cloneAsteroidID(ship.MiningTarget)
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
		err := w.dbCall(context.Background(), func(ctx context.Context) error {
			return w.repo.Delete(ctx, c.ShipID)
		})
		if err != nil {
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
	CargoBay       float64
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
		// A deliberate shipyard re-outfit rebuilds the fit from scratch, so it
		// clears the combat "shield generator destroyed" marker (TASK-100.3.9.1):
		// the shield is governed by the new equipment again, keeping RAM in step
		// with the DB (SaveEquipment writes the same false via outfitShip).
		ship.ShieldGeneratorDestroyed = false
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
		ship.CargoBay = c.CargoBay
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

// AttackCommand sets a ship's AttackTarget: another ship, or — since TASK-112 — a
// shoot-downable projectile (isProjectileTargetKind, i.e. a torpedo). Other kinds
// reply ErrInvalidAttackTarget; statics stay player-unattackable by design
// (TASK-53.2), and self-attack is rejected. On success the worker writes the new
// AttackTarget immediately via repo.Save so a crash between ticks does not lose the
// player's intent.
//
// Aiming at a torpedo is deliberately NOT hostility-gated (ЧТЗ doc-1 R-02 keeps
// splash friendly-fire unselective, and aborting your own torpedo is a legitimate
// move). Automatic point defence IS gated — see nearestHostileTorpedo. Until this
// command accepted a projectile the shoot-down mechanism from TASK-100.3.5.6 was
// structurally present but unreachable: nothing could be told to fire at a torpedo.
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
	case !attackTargetable(c.ShipID, c.Target):
		res.Err = ErrInvalidAttackTarget
	default:
		target := c.Target
		ship.AttackTarget = &target
		s.markDirty(c.ShipID)
		w.immediateSave(ship)
	}
	replyOnce(c.Reply, res)
}

// attackTargetable reports whether ref is something ShipID may point its laser at:
// another ship, or a shoot-downable projectile (a torpedo). A projectile carries no
// self-target case — a ship and a torpedo cannot share an id space — and statics
// remain out of the set on purpose (TASK-53.2: player-unattackable by design, the
// precedent this task follows in reverse).
func attackTargetable(shipID domain.ShipID, ref domain.EntityRef) bool {
	if ref.Kind == domain.EntityKindShip {
		return domain.ShipID(ref.ID) != shipID
	}
	return isProjectileTargetKind(ref.Kind)
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
		// Cease-fire is the "stop what this ship is doing" command: it clears
		// both the combat target and any sustained mining (phase 10.3.6), so the
		// SPA's stop button works for a drilling ship too.
		if ship.AttackTarget != nil || ship.MiningTarget != nil {
			ship.AttackTarget = nil
			ship.MiningTarget = nil
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
// Ownership is enforced (PlayerID must match the launcher's owner). The target
// must be in the same sector and either a different ship or a destructible
// static (TASK-113: missileTargetable); other kinds, self-targeting, and a
// dead/missing target are rejected with ErrInvalidAttackTarget.
// The ammunition debit (1 missile) happens inside apply, through Ordnance
// (TASK-147), so a lost ack cannot leave the player with a free missile.
type LaunchMissileCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	// Class is the ammunition class 1-5 (ct_missiles), which selects the missile's
	// balance profile via combat.DefaultMissileSpec. The handler validates it
	// before sending; the zero value a fixture leaves falls back to class 1.
	Class int
	// GoodsType is the goods id one missile of Class costs (10-14); the handler
	// owns that mapping so the sector package stays free of the goods catalog.
	GoodsType domain.GoodsTypeID
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
	case !missileTargetable(c.ShipID, c.Target):
		// TASK-113/110/111: a missile may strike a ship (not itself, spacesuits
		// included), any destructible static — gates among them — or a loot
		// container. Anything else is refused before the hold is touched.
		res.Err = ErrInvalidAttackTarget
		replyLaunchMissile(c.Reply, res)
		return
	}

	// Resolve the target's current position and confirm it exists and is alive
	// BEFORE spending energy or ammunition — a launch at a missing/dead target
	// must not drain the launcher (TASK-113 FR-07, AC-7). targetPos also seeds
	// the missile's homing course. Mirrors LaunchTorpedoCommand.
	targetPos, targetOK := s.resolveTargetPos(c.Target)
	if !targetOK {
		res.Err = ErrInvalidAttackTarget
		replyLaunchMissile(c.Reply, res)
		return
	}

	// Phase 10.3.1: a launch is an "action" energy expense. Reject when the pool
	// cannot cover the launcher's cost. Cost 0 disables the gate (tests). The
	// debit itself waits until the launch has committed (below).
	if ship.Energy < c.EnergyCost {
		res.Err = ErrNotEnoughEnergy
		replyLaunchMissile(c.Reply, res)
		return
	}

	// Charge the ammunition (TASK-147). Last thing before the missile exists, so
	// every gate above rejects without touching the hold; and if the charge fails
	// the energy is still untouched — a refused launch spends nothing.
	if err := w.spendMissile(ship, c.GoodsType); err != nil {
		res.Err = err
		replyLaunchMissile(c.Reply, res)
		return
	}

	now := c.Now
	if now.IsZero() {
		now = w.clock.Now()
	}
	id := s.allocMissileID()
	m := combat.LaunchMissile(id, combat.DefaultMissileSpec(c.Class), ship, c.Target, targetPos, now)
	s.missiles[id] = m

	// Debit the action energy only once the launch has committed, so a rejected or
	// failed launch spends nothing (mirrors LaunchTorpedoCommand / ЧТЗ AC-3).
	if c.EnergyCost > 0 {
		ship.Energy -= c.EnergyCost
		s.markDirty(c.ShipID)
	}

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

// LaunchTorpedoResult carries the torpedo id allocated for a launch back to the
// HTTP handler so the response can echo it. On success TorpedoID is the
// DB-assigned primary key of the torpedos row the launch created (TASK-100.3.5.4
// spawned the object; TASK-147 made that row and the ammunition debit one
// transaction), so a non-zero id is also proof the player paid. On error
// TorpedoID is zero and Err is non-nil.
type LaunchTorpedoResult struct {
	Err       error
	TorpedoID domain.TorpedoID
}

// LaunchTorpedoCommand fires one torpedo from ShipID at Target (ЧТЗ doc-1 §3
// FR-002/004/006). Modelled on LaunchMissileCommand: ownership is enforced, the
// ship must carry up_torpedo_launcher and be undocked, and a launch spends the
// launcher's "action" energy. Unlike a missile, a torpedo may also strike a
// destructible static (IsStaticTargetKind), not just a ship. The ammunition debit
// (1 unit of GoodsType) happens inside apply, in the same transaction as the
// torpedo row (TASK-147).
type LaunchTorpedoCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	// Class is the ammunition class: 2 (gt23 "Огненная Буря") or 3 (gt24
	// "Святая Торпеда"). It selects the balance spec the torpedo spawns with; the
	// launch gates here do not depend on it (the handler maps class → GoodsType).
	Class int
	// GoodsType is the goods id one torpedo of Class costs (gt23 / gt24); the
	// handler owns that mapping so the sector package stays free of the catalog.
	GoodsType domain.GoodsTypeID
	// EnergyCost is the "action" energy a launch spends (phase 10.3.1), sourced
	// from up_torpedo_launcher.energy_usage by the HTTP handler. The worker
	// rejects the launch with ErrNotEnoughEnergy when Energy < EnergyCost and
	// otherwise debits it. Zero (the test/legacy default) disables the gate.
	EnergyCost int
	Reply      chan<- LaunchTorpedoResult
}

func (c LaunchTorpedoCommand) apply(w *Worker, s *sectorState) {
	var res LaunchTorpedoResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	case shipEquipmentLevel(ship, "up_torpedo_launcher") < 1:
		// Torpedoes require an installed launcher, mirroring up_launcher for
		// missiles (ЧТЗ §3 FR-002).
		res.Err = ErrEquipmentRequired
	case !torpedoTargetable(c.ShipID, c.Target):
		// A torpedo may strike a ship or any destructible static; gates are
		// excluded (not destructible, ЧТЗ C-04). Self-targeting is rejected.
		res.Err = ErrInvalidAttackTarget
	}
	if res.Err != nil {
		replyLaunchTorpedo(c.Reply, res)
		return
	}

	// Resolve the target's current position and confirm it exists and is alive
	// BEFORE spending energy or ammunition — a launch at a missing/dead target
	// must not drain the launcher (mirrors LaunchMissileCommand's target gate).
	// targetPos also seeds the torpedo's LastTargetPos fallback course.
	targetPos, targetOK := s.resolveTargetPos(c.Target)
	if !targetOK {
		res.Err = ErrInvalidAttackTarget
		replyLaunchTorpedo(c.Reply, res)
		return
	}

	// Phase 10.3.1: a launch is an "action" energy expense. Reject when the
	// pool cannot cover the launcher's cost. Cost 0 disables the gate (tests).
	if ship.Energy < c.EnergyCost {
		res.Err = ErrNotEnoughEnergy
		replyLaunchTorpedo(c.Reply, res)
		return
	}

	// Spawn the torpedo from the class spec. The ammunition debit and the row
	// INSERT commit as ONE transaction through Ordnance (TASK-147), so the DB id
	// that comes back — the authoritative TorpedoID that survives restarts — is
	// proof the player paid. A failure means neither happened.
	now := w.clock.Now()
	spec := combat.DefaultTorpedoSpec(c.Class)
	t := combat.LaunchTorpedo(0, c.Class, spec, ship, c.Target, targetPos, now)
	id, err := w.launchTorpedo(ship, c.GoodsType, *t)
	if err != nil {
		// Nothing committed, and no energy was debited yet — the HTTP handler has
		// nothing to compensate.
		res.Err = err
		replyLaunchTorpedo(c.Reply, res)
		return
	}
	t.ID = id
	s.torpedos[id] = t
	s.markTorpedoDirty(id)

	// Debit the action energy only once the launch has committed, so a rejected
	// or failed launch spends nothing (ЧТЗ AC-3).
	if c.EnergyCost > 0 {
		ship.Energy -= c.EnergyCost
		s.markDirty(c.ShipID)
	}

	res.TorpedoID = id
	replyLaunchTorpedo(c.Reply, res)
}

// torpedoTargetable reports whether ref is a legal torpedo target for a launch
// from shipID: a different ship, or a destructible static (IsStaticTargetKind).
// Gates are excluded until they become destructible (ЧТЗ C-04, TASK-110).
func torpedoTargetable(shipID domain.ShipID, ref domain.EntityRef) bool {
	if ref.Kind == domain.EntityKindShip {
		return domain.ShipID(ref.ID) != shipID
	}
	return IsStaticTargetKind(ref.Kind)
}

// missileTargetable reports whether ref is a legal missile target for a launch
// from shipID. Missiles and torpedoes share the same target set (a different
// ship, or a destructible static; gates excluded until TASK-110), so this
// mirrors torpedoTargetable (TASK-113 FR-07).
func missileTargetable(shipID domain.ShipID, ref domain.EntityRef) bool {
	if ref.Kind == domain.EntityKindShip {
		return domain.ShipID(ref.ID) != shipID
	}
	return IsMissileTargetKind(ref.Kind)
}

func replyLaunchTorpedo(reply chan<- LaunchTorpedoResult, res LaunchTorpedoResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}

// LaunchDroneResult reports how many drones were actually spawned, which the
// handler echoes for client-side tracking. Since TASK-147 the salvo is charged and
// INSERTed in one transaction, so Spawned is either the number of drones that flew
// or (on error) zero — never a fraction the handler has to refund. Since TASK-176
// it can also be short of the cap, when the hold had fewer drones than that.
type LaunchDroneResult struct {
	Err     error
	Spawned int
}

// LaunchDroneCommand spawns a salvo of combat drones from ShipID, each launched at
// Target. Ownership is enforced; the target must be a live ship in the same sector
// (phase 4.4: explicitly-assigned target only, see drones.md §4).
//
// The SERVER decides the salvo size (TASK-176):
// min(up_drone_control level − live drones, drones in the hold). The first half is
// clamped here, the second inside the Ordnance transaction — and both must be,
// because only the worker knows the cap and only the transaction can size the hold
// without racing it. The whole salvo is then charged and INSERTed as ONE
// transaction (TASK-147), so each drone survives a restart under its DB primary key
// and the player pays for exactly the drones that flew.
type LaunchDroneCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Target   domain.EntityRef
	// Count is an OPTIONAL upper bound on the salvo. Zero (the HTTP path never
	// sets it) means "as many as up_drone_control allows" — the product rule since
	// TASK-176: the clients used to send a fixed salvo of 3 each, and the entry
	// point that did not also clamp it to the hold answered 400 for a launch the
	// player could plainly see they could make. Kept for callers that need a
	// deterministic number, which is what the worker tests are.
	Count int
	// GoodsType is the goods id one drone costs (21); the handler owns that
	// constant so the sector package stays free of the goods catalog.
	GoodsType domain.GoodsTypeID
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
	// up_drone_control level. Since TASK-147 the clamped size — not the requested
	// Count — is what gets charged: the cap is only known here, in the worker, so
	// billing the request would make the player pay for drones the cap refuses.
	// The visible consequence is deliberately milder than before: asking for 5
	// with a level-2 module and 3 units in the hold used to fail the handler's
	// Consume(5) outright; now 2 launch and 2 are charged.
	//
	// Since TASK-176 the cap IS the salvo unless the caller names a smaller Count:
	// the launch is "as many drones as the drone-control system runs".
	cap := shipEquipmentLevel(ship, "up_drone_control")
	live := s.liveDroneCount(c.ShipID)
	allowed := cap - live
	if allowed <= 0 {
		res.Err = ErrDroneCapReached
		replyLaunchDrone(c.Reply, res)
		return
	}
	toSpawn := allowed
	if c.Count > 0 && c.Count < allowed {
		toSpawn = c.Count
	}

	now := c.Now
	if now.IsZero() {
		now = w.clock.Now()
	}
	ds := make([]domain.Drone, toSpawn)
	for i := range ds {
		d := combat.LaunchDrone(0, droneSpec, ship, c.Target, now)
		nudgeDroneSpawn(d, i, toSpawn)
		ds[i] = *d
	}

	// One transaction sizes the salvo by the hold, charges it and INSERTs it: what
	// flies and what is paid for cannot disagree, and no INSERT can fail halfway.
	// A hold shorter than the cap shortens the salvo (TASK-176) instead of rejecting
	// it; an EMPTY hold is still cargo.ErrInsufficientQuantity → 400.
	ids, err := w.launchDrones(ship, c.GoodsType, ds)
	if err != nil {
		res.Err = err
		replyLaunchDrone(c.Reply, res)
		return
	}
	// ids[i] belongs to ds[i] over the launched prefix — launchDrones guarantees
	// len(ids) <= len(ds), so the pairing cannot index past the salvo, and the
	// drones beyond it were never charged and are simply dropped.
	for i, id := range ids {
		d := ds[i]
		d.ID = id
		s.drones[id] = &d
		s.markDroneDirty(id)
	}
	res.Spawned = len(ids)
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

// RecallDronesResult reports how many cargo units the recall credited (one per
// drone row it actually deleted) and how many of the ship's drones are still out
// there afterwards — non-zero when the hold could not take them all (TASK-156).
// See RecallDronesCommand.
type RecallDronesResult struct {
	Err      error
	Recalled int
	Left     int
}

// RecallDronesCommand returns as many of ShipID's live drones to its hold as the
// hold can take, leaving the rest flying (TASK-156). Ownership is enforced.
type RecallDronesCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	// GoodsType is the goods id one recalled drone is worth (21), the same
	// constant LaunchDroneCommand carries; the handler owns it so the sector
	// package stays free of the goods catalog.
	GoodsType domain.GoodsTypeID
	Reply     chan<- RecallDronesResult
}

// apply deletes the rows and credits the units in ONE transaction through
// sector.Ordnance (TASK-152), then — and only then — drops the drones from RAM.
//
// Before TASK-152 the worker deleted the drones and the HTTP handler credited the
// cargo after the ack. AckTimeout is only TickInterval + 1s, so a delayed tick
// made the handler answer 504 and walk away while the command applied a moment
// later: drones gone, nothing paid back, the consumable simply lost. Same defect
// as the launch side (TASK-147), running the other way.
//
// RAM is mutated only after the transaction commits, and only for the drones the
// transaction settled (TASK-156: a hold with room for two returns two, and the
// other three stay on the radar). The tick goroutine is the sole writer of
// sectorState and stays parked in recallDrones for the duration, so the collected
// ids cannot go stale in between — and a rolled-back transaction leaves the drones
// flying instead of deleting them in RAM alone. What the credit counts is rows
// deleted, not RAM entries: a drone whose row is already gone is cleared from RAM
// but worth nothing (its unit was paid out once already). See recallDrones and
// logRecallError for the deadline case behind that.
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
	// An empty id set is not short-circuited here: recallDrones still applies the
	// nil-Ordnance gate to it, so a misconfigured worker refuses every recall
	// rather than only the ones that would have moved something.
	out, err := w.recallDrones(ship, c.GoodsType, ids)
	if err != nil {
		res.Err = err
		replyRecallDrones(c.Reply, res)
		return
	}
	for _, id := range out.Removed {
		// A recalled drone just vanishes — the DronesRemoved diff tells the
		// SPA; no explosion impact (unlike TTL/owner-loss self-destruct).
		delete(s.drones, id)
		delete(s.dronesDirty, id)
	}
	res.Recalled = out.Credited
	res.Left = len(ids) - len(out.Removed)
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

// MineCommand starts sustained ore mining on a ship (phase 10.3.6). It only
// arms the mode — the per-tick drilling (energy gate, ore extraction, deposit
// by up_drill level) runs in the worker's tickPlayerMining using the per-tick
// world parameters (cfg.MineRange/MineRate/MineEnergyCost). Ownership is
// enforced; the ship must carry an up_drill module (ErrEquipmentRequired), must
// not be docked, and the target asteroid must be a live body in the same sector
// within cfg.MineRange. A nil Asteroid is a stop request: it clears any active
// MiningTarget (idempotent), mirroring CeaseFireCommand.
type MineCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Asteroid *domain.AsteroidID
	Reply    chan<- CmdResult
}

func (c MineCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
		replyOnce(c.Reply, res)
		return
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
		replyOnce(c.Reply, res)
		return
	}

	// A nil asteroid means "stop mining" — always allowed, no equipment gate
	// (a ship can stop regardless of its fit), idempotent.
	if c.Asteroid == nil {
		if ship.MiningTarget != nil {
			ship.MiningTarget = nil
			s.markDirty(c.ShipID)
		}
		replyOnce(c.Reply, res)
		return
	}

	switch {
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	case shipEquipmentLevel(ship, "up_drill") < 1:
		// Phase 10.3.6: drilling requires an installed up_drill module, mirroring
		// the original StarWind UseDrill capability gate.
		res.Err = ErrEquipmentRequired
	default:
		ast, ok := s.asteroids[*c.Asteroid]
		switch {
		case !ok || ast.Mass <= 0:
			res.Err = ErrAsteroidNotFound
		case ship.Pos.Sub(ast.Pos).Length() > w.cfg.MineRange:
			res.Err = ErrAsteroidOutOfRange
		default:
			target := *c.Asteroid
			ship.MiningTarget = &target
			// Hold station immediately so the ship does not coast away before
			// the next tickPlayerMining (same stance as the NPC applyMine).
			ship.Target = nil
			ship.FinalTarget = nil
			ship.AttackTarget = nil
			ship.Vel = domain.Vec2{}
			s.markDirty(c.ShipID)
		}
	}
	replyOnce(c.Reply, res)
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

// CaptureResult reports the capture outcome to the HTTP handler: Captured is true
// only when the roll succeeded and the ship changed owner. On a gate rejection Err
// is non-nil and Captured is false.
type CaptureResult struct {
	Err      error
	Captured bool
}

// CaptureShipCommand attempts to seize a hostile ship with the up_capture module
// (SP DoCapture, ЧТЗ doc-4 §3 Механика C). It is the payoff of the shield-strip
// chain: only a target whose shield generator is gone (MaxShield 0 — class-less or
// knocked off by TASK-100.3.9.1) can be captured. Ownership + undocked + up_capture
// + a hostile ship target in range + no working shield + energy are all gated; then
// a low-chance roll either re-owns the ship (changeShipOwner, .2) or damages/destroys
// its hull. EnergyCost (up_capture.energy_usage, resolved by the HTTP handler) is
// spent on either outcome.
type CaptureShipCommand struct {
	PlayerID   domain.PlayerID
	ShipID     domain.ShipID
	Target     domain.EntityRef
	EnergyCost int
	Reply      chan<- CaptureResult
}

func (c CaptureShipCommand) apply(w *Worker, s *sectorState) {
	var res CaptureResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	case shipEquipmentLevel(ship, captureModuleType) < 1:
		res.Err = ErrEquipmentRequired
	case c.Target.Kind != domain.EntityKindShip:
		res.Err = ErrInvalidAttackTarget
	case domain.ShipID(c.Target.ID) == c.ShipID:
		res.Err = ErrInvalidAttackTarget
	}
	if res.Err != nil {
		replyCapture(c.Reply, res)
		return
	}

	target, ok := s.ships[domain.ShipID(c.Target.ID)]
	switch {
	case !ok || target.HP <= 0:
		res.Err = ErrInvalidAttackTarget
	case w.shipsAreFriendly(ship, target):
		// Damage-parity gate: capture any non-allied ship (SP DoCapture had no
		// hostility gate at all). The weapon damage that strips the shield gates on
		// !shipsAreFriendly (combat.go), so capture uses the same "not an ally"
		// test — this keeps the ЧТЗ C-06 main case (capturing NPC ships, which share
		// the __npc__ owner and are not friendly) working. A friendly (own clan /
		// declared friend, incl. self via relations a==b → Friend) target is rejected.
		res.Err = ErrInvalidAttackTarget
	case ship.Pos.Sub(target.Pos).Length() > w.cfg.CaptureRange:
		res.Err = ErrCaptureOutOfRange
	case target.MaxShield > 0:
		// A working shield generator blocks capture (SP DoCapture). MaxShield 0
		// means either a class with no shield or an up_shield knocked off in combat
		// (TASK-100.3.9.1 forces MaxShield 0 via ShieldGeneratorDestroyed).
		res.Err = ErrCaptureShielded
	case ship.Energy < c.EnergyCost:
		res.Err = ErrNotEnoughEnergy
	}
	if res.Err != nil {
		replyCapture(c.Reply, res)
		return
	}

	// All gates passed — the attempt commits. Energy is spent on either outcome
	// (FR-C3/C5), so debit it before the roll.
	if c.EnergyCost > 0 {
		ship.Energy -= c.EnergyCost
		s.markDirty(c.ShipID)
	}

	// Capture roll (FR-C3): rng.Float64()*1000 > threshold. Kha'ak targets are
	// harder (higher threshold). Read the race BEFORE changeShipOwner neutralises it.
	capturedRace := target.Race
	threshold := w.cfg.CaptureChance
	if capturedRace == khaakRace {
		threshold = w.cfg.KhaakCaptureChance
	}
	if w.rng.Float64()*1000 > threshold {
		oldOwner := target.PlayerID
		if err := w.changeShipOwner(context.Background(), s, target, c.PlayerID); err != nil {
			// The roll succeeded but the transfer could not be persisted, so the
			// ship stays with its old owner in both RAM and the DB (TASK-148). The
			// attempt is reported as failed — the energy is spent either way
			// (FR-C3/C5), exactly as on a losing roll. Reporting success instead
			// would hand the captor a ship that reverts at the next cold start.
			res.Err = err
			replyCapture(c.Reply, res)
			return
		}
		if w.reputation != nil {
			err := w.dbCall(context.Background(), func(ctx context.Context) error {
				return w.reputation.OnShipCaptured(ctx, c.PlayerID, capturedRace)
			})
			if err != nil {
				w.logger.Error("reputation: on capture",
					"err", err, "capturer", int64(c.PlayerID), "ship", int64(target.ID))
			}
		}
		w.publishShipCapture(context.Background(), s, c.PlayerID, target.ID, true, true)
		w.publishShipCapture(context.Background(), s, oldOwner, target.ID, false, true)
		res.Captured = true
		replyCapture(c.Reply, res)
		return
	}

	// Failed capture (FR-C5): hull damage maxHP/16 (SP maxHull>>4). A lethal blow
	// destroys the target via the standard kill path (attributed to the captor).
	hullDown := target.MaxHP / 16
	if hullDown >= target.HP {
		target.LastAttacker = c.PlayerID
		w.killShip(context.Background(), s, target)
	} else {
		target.HP -= hullDown
		s.markDirty(target.ID)
		w.immediateSave(target)
	}
	w.publishShipCapture(context.Background(), s, c.PlayerID, target.ID, true, false)
	replyCapture(c.Reply, res)
}

func replyCapture(reply chan<- CaptureResult, res CaptureResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}
