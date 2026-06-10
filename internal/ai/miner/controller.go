// Package miner is the NPC mining-TS AI (phase 5.4): a reactive per-ship
// controller on the 5.1 runtime that flies to an asteroid, drills ore into
// its hold, and hauls it back to its home factory. The MVP of the old
// CFabNpcShips miner branch; see back/docs/specs/npc_miners.md.
//
// Unlike the original (drill -> ore containers -> pickup, all driven through
// TO_ShipMovement/UseDrill), the deposit is direct: the controller emits
// ai.Mine and the worker subtracts asteroid mass and adds ore to the hold.
// The controller can't see its hold contents (WorldView doesn't expose them),
// so it tracks the mined amount itself and heads home at LoadTarget or when
// the asteroid is depleted.
package miner

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// Kind is the persisted controller_kind discriminator.
const Kind = "miner"

// unloadAll is the MaxUnits passed to the unload Transfer: large enough to
// dump the whole hold (Transfer caps by what the ship actually carries).
const unloadAll = int64(1) << 30

// Phase is the miner's state-machine position.
type Phase string

const (
	// PhaseToAsteroid: flying to the target asteroid (possibly cross-sector).
	PhaseToAsteroid Phase = "to_asteroid"
	// PhaseMining: parked at the asteroid, drilling.
	PhaseMining Phase = "mining"
	// PhaseToHome: hauling ore back to the home factory.
	PhaseToHome Phase = "to_home"
	// PhaseIdle: parked at the home factory, looking for a new asteroid.
	PhaseIdle Phase = "idle"
)

// Leg is the home factory: a station at a known position in a (possibly
// remote) sector. Stored in the controller's own state because WorldView
// exposes only the current sector — a miner must know its home's coordinates
// to set a cross-sector course back to it.
type Leg struct {
	Sector domain.SectorID  `json:"sector"`
	Pos    domain.Vec2      `json:"pos"`
	Ref    domain.EntityRef `json:"ref"`
}

// Target is the asteroid the miner is currently working: id plus its
// position, so the controller can steer toward it across sectors before it
// can see it in WorldView.
type Target struct {
	ID     domain.AsteroidID `json:"id"`
	Sector domain.SectorID   `json:"sector"`
	Pos    domain.Vec2       `json:"pos"`
}

// Config tunes the miner. Zero fields fall back to defaults.
type Config struct {
	// ArriveRadius is how close to the home factory (and stopped) counts as
	// "arrived". Must exceed the autopilot's park distance (DockRange/2).
	ArriveRadius float64
	// MineRange is how close to the asteroid counts as "in drilling range".
	MineRange float64
	// DrillRate is the ore mined per tick while in PhaseMining.
	DrillRate int64
	// LoadTarget is how much ore to accumulate before heading home. Keep it
	// below the ship's hold capacity so cargo.Add never hits ErrNoSpace.
	LoadTarget int64
}

func (c Config) withDefaults() Config {
	if c.ArriveRadius <= 0 {
		c.ArriveRadius = 6
	}
	if c.MineRange <= 0 {
		c.MineRange = 12
	}
	if c.DrillRate <= 0 {
		c.DrillRate = 5
	}
	if c.LoadTarget <= 0 {
		c.LoadTarget = 40
	}
	return c
}

// state is the controller's persisted state (ai_state JSON).
type state struct {
	Phase  Phase              `json:"phase"`
	Home   Leg                `json:"home"`
	Ore    domain.GoodsTypeID `json:"ore"`
	Target Target             `json:"target"`
	Mined  int64              `json:"mined"`
}

// Controller is one miner's reactive AI.
type Controller struct {
	cfg Config
	st  state
}

func (c *Controller) Kind() string { return Kind }

// MarshalState serializes the controller's state for the ai_state snapshot.
func (c *Controller) MarshalState() ([]byte, error) { return json.Marshal(c.st) }

// CurrentPhase reports the miner's phase (test/inspection helper).
func (c *Controller) CurrentPhase() Phase { return c.st.Phase }

// Tick dispatches on the current phase.
func (c *Controller) Tick(_ context.Context, view ai.WorldView) ai.Action {
	self := view.Self()
	switch c.st.Phase {
	case PhaseMining:
		return c.tickMining(self, view)
	case PhaseToHome:
		return c.tickToHome(self)
	case PhaseIdle:
		return c.tickIdle(self, view)
	default: // PhaseToAsteroid
		return c.tickToAsteroid(self, view)
	}
}

// tickToAsteroid steers the miner to its target asteroid. When in the
// asteroid's sector it checks the asteroid still exists (re-picks or heads
// home otherwise) and, once within MineRange, switches to drilling.
func (c *Controller) tickToAsteroid(self domain.Ship, view ai.WorldView) ai.Action {
	if self.SectorID == c.st.Target.Sector {
		ast, ok := findAsteroid(view, c.st.Target.ID)
		if !ok {
			return c.repickOrHome(self, view)
		}
		if self.Pos.Sub(ast.Pos).Length() <= c.cfg.MineRange {
			c.st.Phase = PhaseMining
			return c.mine()
		}
	}
	return c.courseTo(self, c.st.Target.Sector, c.st.Target.Pos, nil)
}

// tickMining drills the asteroid until the load target is met or the asteroid
// is depleted, then heads home. If the ship has drifted out of range it
// re-approaches.
func (c *Controller) tickMining(self domain.Ship, view ai.WorldView) ai.Action {
	ast, ok := findAsteroid(view, c.st.Target.ID)
	if !ok || ast.Mass <= 0 || c.st.Mined >= c.cfg.LoadTarget {
		return c.goHome(self)
	}
	if self.Pos.Sub(ast.Pos).Length() > c.cfg.MineRange {
		c.st.Phase = PhaseToAsteroid
		return c.courseTo(self, c.st.Target.Sector, c.st.Target.Pos, nil)
	}
	return c.mine()
}

// tickToHome flies the miner back to its home factory and, on arrival,
// unloads the whole hold into the factory, then goes idle.
func (c *Controller) tickToHome(self domain.Ship) ai.Action {
	if c.arrived(self, c.st.Home) {
		shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(self.ID)}
		c.st.Mined = 0
		c.st.Phase = PhaseIdle
		return ai.Transfer{From: shipRef, To: c.st.Home.Ref, GoodsType: c.st.Ore, MaxUnits: unloadAll}
	}
	approach := c.st.Home.Ref
	return c.courseTo(self, c.st.Home.Sector, c.st.Home.Pos, &approach)
}

// tickIdle, parked at the home factory, picks the nearest live asteroid of
// the miner's ore type in the current sector and heads out; if none, it waits.
func (c *Controller) tickIdle(self domain.Ship, view ai.WorldView) ai.Action {
	ast, ok := c.nearestAsteroid(view, self.Pos)
	if !ok {
		return ai.Idle{}
	}
	c.st.Target = Target{ID: ast.ID, Sector: self.SectorID, Pos: ast.Pos}
	c.st.Mined = 0
	c.st.Phase = PhaseToAsteroid
	return c.courseTo(self, c.st.Target.Sector, c.st.Target.Pos, nil)
}

// repickOrHome handles a target asteroid that vanished while the miner was in
// its sector: pick another local one, else head home with whatever is held.
func (c *Controller) repickOrHome(self domain.Ship, view ai.WorldView) ai.Action {
	if ast, ok := c.nearestAsteroid(view, self.Pos); ok {
		c.st.Target = Target{ID: ast.ID, Sector: self.SectorID, Pos: ast.Pos}
		return c.courseTo(self, c.st.Target.Sector, c.st.Target.Pos, nil)
	}
	return c.goHome(self)
}

// mine accounts for one drill tick and emits the Mine action.
func (c *Controller) mine() ai.Action {
	c.st.Mined += c.cfg.DrillRate
	return ai.Mine{Asteroid: c.st.Target.ID, Amount: c.cfg.DrillRate}
}

// goHome switches to the home leg and emits its first navigation action.
func (c *Controller) goHome(self domain.Ship) ai.Action {
	c.st.Phase = PhaseToHome
	return c.tickToHome(self)
}

// arrived reports whether the ship is parked at the leg's station: in the
// leg's sector, stopped, and within ArriveRadius. Mirrors the trader; the
// autopilot parks ships with Vel exactly zero.
func (c *Controller) arrived(self domain.Ship, leg Leg) bool {
	if self.SectorID != leg.Sector || !self.Vel.IsZero() {
		return false
	}
	return self.Pos.Sub(leg.Pos).Length() <= c.cfg.ArriveRadius
}

// nearestAsteroid returns the closest live asteroid of the miner's ore type
// in the current sector. Asteroids() is sorted by id, so ties resolve
// deterministically to the lowest id.
func (c *Controller) nearestAsteroid(view ai.WorldView, from domain.Vec2) (domain.Asteroid, bool) {
	var best domain.Asteroid
	bestDist := 0.0
	found := false
	for _, a := range view.Asteroids() {
		if a.OreType != c.st.Ore || a.Mass <= 0 {
			continue
		}
		d := a.Pos.Sub(from).Length()
		if !found || d < bestDist {
			best, bestDist, found = a, d, true
		}
	}
	return best, found
}

// courseTo arms the autopilot toward (sector, pos) unless it is already
// aimed there. approach is nil for an asteroid (just fly to the point) and
// the home station ref when returning (so the autopilot parks).
func (c *Controller) courseTo(self domain.Ship, sector domain.SectorID, pos domain.Vec2, approach *domain.EntityRef) ai.Action {
	if courseSet(self.FinalTarget, sector, pos) {
		return ai.Idle{}
	}
	return ai.SetCourse{Course: domain.Course{Sector: sector, Pos: pos, Approach: approach}}
}

// findAsteroid looks up an asteroid by id in the current sector's view.
func findAsteroid(view ai.WorldView, id domain.AsteroidID) (domain.Asteroid, bool) {
	for _, a := range view.Asteroids() {
		if a.ID == id {
			return a, true
		}
	}
	return domain.Asteroid{}, false
}

// courseSet reports whether the autopilot is already aimed at (sector, pos).
// Positions are compared exactly: the controller sets them from the same
// stored value, so there is no float drift to tolerate.
func courseSet(ft *domain.Course, sector domain.SectorID, pos domain.Vec2) bool {
	return ft != nil && ft.Sector == sector && ft.Pos == pos
}
