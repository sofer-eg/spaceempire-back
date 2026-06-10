// Package trader is the NPC TS-trader AI (phase 5.3): a reactive per-ship
// controller on the 5.1 runtime that shuttles goods between two stations.
// Each trader has a fixed two-leg route — a home factory and a destination
// trade station, possibly in different sectors — captured at spawn. It flies
// to a leg via ai.SetCourse (the player autopilot + gate handoff carry it
// across sectors), and on arrival hauls cargo with ai.Transfer, then flips to
// the other leg. The MVP of the old CFabNpcShips trader branch; see
// back/docs/specs/npc_traders.md.
package trader

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// Kind is the persisted controller_kind discriminator.
const Kind = "trader"

// Phase is which leg of the route the trader is currently working: heading
// to (and loading at) the home factory, or heading to (and unloading at) the
// destination.
type Phase string

const (
	PhaseHome Phase = "home"
	PhaseDest Phase = "dest"
)

// Leg is one end of the route: a station at a known position in a (possibly
// remote) sector. The position is stored in the controller's own state
// because WorldView exposes only the current sector's statics — a trader
// must know a remote leg's coordinates to set a cross-sector course toward
// it.
type Leg struct {
	Sector domain.SectorID  `json:"sector"`
	Pos    domain.Vec2      `json:"pos"`
	Ref    domain.EntityRef `json:"ref"`
}

// Config tunes the trader. Zero fields fall back to defaults.
type Config struct {
	// ArriveRadius is how close to a leg's station (and stopped) counts as
	// "arrived". Must exceed the autopilot's park distance (Config.DockRange/2)
	// so the trader recognises a parked ship as arrived.
	ArriveRadius float64
	// HaulQty caps the units moved per transfer when the trader's own state
	// carries no per-route override.
	HaulQty int64
}

func (c Config) withDefaults() Config {
	if c.ArriveRadius <= 0 {
		c.ArriveRadius = 6
	}
	if c.HaulQty <= 0 {
		c.HaulQty = 20
	}
	return c
}

// state is the controller's persisted state (ai_state JSON).
type state struct {
	Phase   Phase              `json:"phase"`
	Home    Leg                `json:"home"`
	Dest    Leg                `json:"dest"`
	Goods   domain.GoodsTypeID `json:"goods"`
	HaulQty int64              `json:"haulQty,omitempty"`
}

// Controller is one trader's reactive AI.
type Controller struct {
	cfg Config
	st  state
}

func (c *Controller) Kind() string { return Kind }

// MarshalState serializes the controller's state for the ai_state snapshot.
func (c *Controller) MarshalState() ([]byte, error) { return json.Marshal(c.st) }

// CurrentPhase reports the trader's current leg (test/inspection helper).
func (c *Controller) CurrentPhase() Phase { return c.st.Phase }

// Tick decides the trader's action: when parked at the current leg's
// station, haul cargo and flip to the other leg; otherwise make sure the
// autopilot is steering toward the current leg, then idle while it flies.
func (c *Controller) Tick(_ context.Context, view ai.WorldView) ai.Action {
	self := view.Self()
	leg := c.currentLeg()

	if c.arrived(self, leg) {
		return c.transferAndAdvance(self.ID, leg)
	}
	if !courseSet(self.FinalTarget, leg) {
		approach := leg.Ref
		return ai.SetCourse{Course: domain.Course{
			Sector:   leg.Sector,
			Pos:      leg.Pos,
			Approach: &approach,
		}}
	}
	return ai.Idle{}
}

// currentLeg returns the leg the trader is working this phase.
func (c *Controller) currentLeg() Leg {
	if c.st.Phase == PhaseDest {
		return c.st.Dest
	}
	return c.st.Home
}

// arrived reports whether the ship is parked at the leg's station: in the
// leg's sector, stopped, and within ArriveRadius of the station. The
// autopilot parks ships with Vel exactly zero, so IsZero is a reliable
// "stopped" test; tickAI runs before the autopilot, so Self() reflects the
// previous tick's parked state.
func (c *Controller) arrived(self domain.Ship, leg Leg) bool {
	if self.SectorID != leg.Sector || !self.Vel.IsZero() {
		return false
	}
	return self.Pos.Sub(leg.Pos).Length() <= c.cfg.ArriveRadius
}

// transferAndAdvance emits the load/unload transfer for the current leg and
// flips the phase to the other leg. Loading pulls goods from the home
// factory into the ship; unloading pushes them from the ship to the
// destination.
func (c *Controller) transferAndAdvance(shipID domain.ShipID, leg Leg) ai.Action {
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(shipID)}
	if c.st.Phase == PhaseHome {
		c.st.Phase = PhaseDest
		return ai.Transfer{From: leg.Ref, To: shipRef, GoodsType: c.st.Goods, MaxUnits: c.haulQty()}
	}
	c.st.Phase = PhaseHome
	return ai.Transfer{From: shipRef, To: leg.Ref, GoodsType: c.st.Goods, MaxUnits: c.haulQty()}
}

func (c *Controller) haulQty() int64 {
	if c.st.HaulQty > 0 {
		return c.st.HaulQty
	}
	return c.cfg.HaulQty
}

// courseSet reports whether the ship's autopilot is already aimed at the
// leg. Positions are compared exactly: the controller itself sets them from
// the same stored Leg, so there is no float drift to tolerate.
func courseSet(ft *domain.Course, leg Leg) bool {
	return ft != nil && ft.Sector == leg.Sector && ft.Pos == leg.Pos
}
