// Package ai is the NPC AI runtime (phase 5.1): the framework every NPC
// controller plugs into. A Controller is ticked once per sector tick from
// the sector worker's single goroutine (no extra goroutines — the
// one-writer-per-sector invariant holds) and returns an Action describing
// what it wants to do. The worker, not the controller, mutates sector
// state — so a controller can only read the world (through WorldView) and
// propose an Action.
//
// This package deliberately depends only on domain: the sector package
// imports ai (to hold and tick controllers and to implement WorldView over
// its live state), never the reverse. Concrete controllers (race AI,
// traders, miners, passengers) land in phases 5.2–5.5 and register their
// constructors with a Registry.
package ai

import (
	"context"

	"spaceempire/back/internal/domain"
)

// WorldView is the read-only window a Controller gets into its sector each
// tick. It exposes no mutators on purpose: an AI influences the world only
// by returning an Action, which the worker applies. Returned values are
// copies — mutating them has no effect on the live sector.
type WorldView interface {
	// Self is the ship this controller drives. Treat it as read-only; to
	// move/act, return an Action.
	Self() domain.Ship
	// Ships are all ships currently in the sector (including Self), as
	// value copies. Used by controllers that react to neighbours (combat,
	// avoidance). O(n) per call — acceptable at current scale.
	Ships() []domain.Ship
	// Statics are the sector's static objects (stations, shipyards, trade
	// stations, pirbases, laser towers). Navigation/trade targets for
	// traders and passengers.
	Statics() domain.SectorStatics
	// Asteroids are the minable ore bodies currently in the sector, as value
	// copies. Miners (phase 5.4) read them to detect arrival/depletion and to
	// re-pick a target when their current asteroid is mined out.
	Asteroids() []domain.Asteroid
}

// Action is what a Controller returns each tick. It is a closed set of
// command-like values the worker knows how to apply; the worker switches on
// the concrete type. Phase 5.1 implements the minimum the runtime needs —
// later NPC phases extend the set (Attack, Dock, Buy, …) alongside the
// controllers that issue them.
type Action interface {
	isAction()
}

// Idle is the no-op action: the controller has nothing to do this tick.
type Idle struct{}

func (Idle) isAction() {}

// MoveTo sets the ship's in-sector waypoint, exactly like a player's
// MoveCommand. The worker writes ship.Target and clears any AttackTarget
// (an AI MoveTo means "go here, don't fight"). Movement integrates toward
// it on the same tick.
type MoveTo struct {
	Target domain.Vec2
}

func (MoveTo) isAction() {}

// Attack engages a target: the worker sets the ship's AttackTarget (the
// laser tick fires at it, phase 4.2) and steers the ship toward the
// target's current position so it closes to weapons range. Phase 5.2 —
// issued by NPC controllers that have decided the target is hostile.
type Attack struct {
	Target domain.EntityRef
}

func (Attack) isAction() {}

// SetCourse arms the autopilot: the worker writes ship.FinalTarget so the
// existing cross-sector machinery (resolveAutopilot + tryAutoJump, phases
// 2.4/2.5) routes the ship hop-by-hop through gates and parks it at the
// destination. Unlike MoveTo (a single in-sector waypoint), SetCourse can
// target a static in another sector. Phase 5.3 — issued by NPC traders to
// fly between stations.
type SetCourse struct {
	Course domain.Course
}

func (SetCourse) isAction() {}

// Transfer moves goods between two cargo owners (station↔ship) as an
// in-tick database transaction, executed by the worker's TraderLogistics
// dependency. MaxUnits caps the amount; the worker hauls
// min(available-at-From, MaxUnits, room-in-To). Phase 5.3 — issued by NPC
// traders on arrival to load at the source and unload at the destination.
// Logistics are free (no cash), mirroring the old "BUYING без списания
// кредитов".
type Transfer struct {
	From      domain.EntityRef
	To        domain.EntityRef
	GoodsType domain.GoodsTypeID
	MaxUnits  int64
}

func (Transfer) isAction() {}

// Mine drills an asteroid the ship is parked next to: the worker subtracts
// Amount (capped by the asteroid's remaining mass and the hold's free space)
// from the asteroid and deposits that much ore into the ship's hold, removing
// the asteroid when its mass reaches zero. It also holds the ship in place
// (clears Target/FinalTarget) so it stays on station while drilling. Phase 5.4
// — issued by NPC miners once in mining range.
type Mine struct {
	Asteroid domain.AsteroidID
	Amount   int64
}

func (Mine) isAction() {}

// BoardPassengers fills the ship's hold with a fresh batch of passengers as it
// leaves a station: the worker rolls a random count in [1, Max] (using its own
// RNG, since controllers have none) and writes ship.Passengers. Phase 5.5 —
// issued by an NPC passenger TS on departure.
type BoardPassengers struct {
	Max int
}

func (BoardPassengers) isAction() {}

// DropPassengers clears the ship's passenger count to zero on arrival at a
// station (the passengers disembark). Phase 5.5.
type DropPassengers struct{}

func (DropPassengers) isAction() {}

// Controller is one NPC's decision logic. The worker owns its lifecycle:
// it is built at cold-start from the persisted AIState (via a Registry),
// ticked every sector tick, and snapshotted periodically (MarshalState).
type Controller interface {
	// Kind returns the persisted controller_kind discriminator — the key
	// the Registry uses to rebuild this controller after a restart.
	Kind() string
	// Tick reads the world and returns the action to apply this tick.
	// Called from the worker's tick goroutine only.
	Tick(ctx context.Context, view WorldView) Action
	// MarshalState serializes the controller's mutable state for the
	// periodic ai_state snapshot. Returning (nil, nil) means "no state to
	// persist" — the controller rebuilds from its zero value next start.
	MarshalState() ([]byte, error)
}
