// Package passenger is the NPC passenger-TS AI (phase 5.5): a reactive
// per-ship controller on the 5.1 runtime that ferries passengers between
// nearby stations. Each passenger TS is bound to a home trade station and is
// given, at spawn, a pool of candidate destinations within a few gate hops of
// home. It cruises the pool: fly to a station, drop passengers, wait a fixed
// number of ticks, board a fresh random batch, fly to the next. The MVP of the
// old CFabNpcShips passenger branch; see back/docs/specs/npc_passengers.md.
//
// Simplifications vs the original: the destination pool is fixed at spawn and
// home-relative (so the ship never drifts past home_max_hops — that guard is
// moot and dropped), targets are visited round-robin rather than DB-random,
// and the dock wait is counted in ticks (the controller has no clock) rather
// than a wall-clock next_action timestamp.
package passenger

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// Kind is the persisted controller_kind discriminator.
const Kind = "passenger"

// Phase is the passenger TS state-machine position.
type Phase string

const (
	// PhaseFlying: en route to the current destination station.
	PhaseFlying Phase = "flying"
	// PhaseWaiting: parked at a station, waiting out the dock timer before
	// boarding the next batch.
	PhaseWaiting Phase = "waiting"
)

// Leg is a station the TS can dock at: position + ref in a (possibly remote)
// sector. The pool of these is fixed at spawn; the controller stores them in
// its own state because WorldView exposes only the current sector.
type Leg struct {
	Sector domain.SectorID  `json:"sector"`
	Pos    domain.Vec2      `json:"pos"`
	Ref    domain.EntityRef `json:"ref"`
}

// Config tunes the passenger TS. Zero fields fall back to defaults.
type Config struct {
	// ArriveRadius is how close to a station (and stopped) counts as
	// "arrived". Must exceed the autopilot's park distance (DockRange/2).
	ArriveRadius float64
	// DockWaitTicks is how many ticks the TS waits at a station before
	// departing. The app computes it from passenger_dock_wait_seconds / the
	// tick interval (the controller has no clock).
	DockWaitTicks int
	// MaxPassengers caps the random batch boarded on departure.
	MaxPassengers int
}

func (c Config) withDefaults() Config {
	if c.ArriveRadius <= 0 {
		c.ArriveRadius = 6
	}
	if c.DockWaitTicks <= 0 {
		c.DockWaitTicks = 10
	}
	if c.MaxPassengers <= 0 {
		c.MaxPassengers = 33
	}
	return c
}

// state is the controller's persisted state (ai_state JSON).
type state struct {
	Phase    Phase `json:"phase"`
	Pool     []Leg `json:"pool"`
	Dest     Leg   `json:"dest"`     // current destination (and, while waiting, where it is parked)
	RouteIdx int   `json:"routeIdx"` // round-robin cursor over Pool
	WaitLeft int   `json:"waitLeft"` // ticks remaining at the station
}

// Controller is one passenger TS's reactive AI.
type Controller struct {
	cfg Config
	st  state
}

func (c *Controller) Kind() string { return Kind }

// MarshalState serializes the controller's state for the ai_state snapshot.
func (c *Controller) MarshalState() ([]byte, error) { return json.Marshal(c.st) }

// CurrentPhase reports the TS's phase (test/inspection helper).
func (c *Controller) CurrentPhase() Phase { return c.st.Phase }

// Tick dispatches on the current phase.
func (c *Controller) Tick(_ context.Context, view ai.WorldView) ai.Action {
	self := view.Self()
	if c.st.Phase == PhaseFlying {
		return c.tickFlying(self)
	}
	return c.tickWaiting()
}

// tickFlying steers the TS to its destination and, on arrival, drops the
// passengers and starts the dock-wait timer.
func (c *Controller) tickFlying(self domain.Ship) ai.Action {
	if c.arrived(self, c.st.Dest) {
		c.st.Phase = PhaseWaiting
		c.st.WaitLeft = c.cfg.DockWaitTicks
		return ai.DropPassengers{}
	}
	return c.courseTo(self, c.st.Dest)
}

// tickWaiting counts down the dock timer, then picks the next destination,
// boards a fresh batch of passengers, and departs.
func (c *Controller) tickWaiting() ai.Action {
	if c.st.WaitLeft > 0 {
		c.st.WaitLeft--
		return ai.Idle{}
	}
	next, ok := c.pickNext()
	if !ok {
		return ai.Idle{} // nowhere to go — keep waiting at this station
	}
	c.st.Dest = next
	c.st.Phase = PhaseFlying
	return ai.BoardPassengers{Max: c.cfg.MaxPassengers}
}

// pickNext advances the round-robin cursor to the next pool entry that is not
// the current location. Returns ok=false when the pool is empty or holds only
// the current station.
func (c *Controller) pickNext() (Leg, bool) {
	n := len(c.st.Pool)
	if n == 0 {
		return Leg{}, false
	}
	for i := 0; i < n; i++ {
		leg := c.st.Pool[c.st.RouteIdx%n]
		c.st.RouteIdx = (c.st.RouteIdx + 1) % n
		if leg.Ref != c.st.Dest.Ref {
			return leg, true
		}
	}
	return Leg{}, false
}

// arrived reports whether the ship is parked at the leg's station: in the
// leg's sector, stopped, and within ArriveRadius. The autopilot parks ships
// with Vel exactly zero.
func (c *Controller) arrived(self domain.Ship, leg Leg) bool {
	if self.SectorID != leg.Sector || !self.Vel.IsZero() {
		return false
	}
	return self.Pos.Sub(leg.Pos).Length() <= c.cfg.ArriveRadius
}

// courseTo arms the autopilot toward the leg (with an Approach so it parks at
// the station) unless it is already aimed there.
func (c *Controller) courseTo(self domain.Ship, leg Leg) ai.Action {
	if courseSet(self.FinalTarget, leg) {
		return ai.Idle{}
	}
	approach := leg.Ref
	return ai.SetCourse{Course: domain.Course{Sector: leg.Sector, Pos: leg.Pos, Approach: &approach}}
}

// courseSet reports whether the autopilot is already aimed at the leg.
func courseSet(ft *domain.Course, leg Leg) bool {
	return ft != nil && ft.Sector == leg.Sector && ft.Pos == leg.Pos
}
