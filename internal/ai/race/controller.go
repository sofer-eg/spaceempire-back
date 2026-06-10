// Package race is the NPC race AI (phase 5.2): a reactive per-ship
// controller on the 5.1 AI runtime. Each race ship independently patrols an
// anchor, engages hostile ships in detection range, and retreats when its
// hull drops below a threshold — the MVP of the old FleetAI (CFleet::Turn).
// Phase 8.4 adds emergent focus-fire: a ship prefers a hostile an ally is
// already engaging, so race ships pack onto one target. Formal fleet/flight-
// group structures (a coordinator, formations) remain deferred; see
// back/docs/specs/race_ai.md.
package race

import (
	"context"
	"encoding/json"
	"math"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/domain"
)

// Kind is the persisted controller_kind discriminator.
const Kind = "race"

// Order is the controller's current high-level behaviour — the reduced
// successor of the old f_ships Orders bitmask (one active mode at a time).
type Order string

const (
	OrderPatrol  Order = "patrol"
	OrderEngage  Order = "engage"
	OrderRetreat Order = "retreat"
)

// Config tunes the reactive thresholds. Zero fields fall back to defaults.
type Config struct {
	// DetectionRange is the radius (world units) within which hostiles are
	// noticed. Mirrors the old OPT_FLEET_RADAR_RANGE.
	DetectionRange float64
	// FleeThreshold is the hull fraction (0..1) below which the ship
	// retreats from a nearby hostile (old OPT_EVADE_MODE_ENTER ≈ 0.3).
	FleeThreshold float64
	// PatrolRadius is the patrol-circle radius around the anchor (also the
	// flee step length).
	PatrolRadius float64
	// PatrolStep is the patrol angle advanced per tick (radians).
	PatrolStep float64
}

func (c Config) withDefaults() Config {
	if c.DetectionRange <= 0 {
		c.DetectionRange = 600
	}
	if c.FleeThreshold <= 0 {
		c.FleeThreshold = 0.3
	}
	if c.PatrolRadius <= 0 {
		c.PatrolRadius = 150
	}
	if c.PatrolStep <= 0 {
		c.PatrolStep = 0.1
	}
	return c
}

// Targeter decides whether other is a hostile target for self. Production
// backs it with relations.Service (6.2); tests inject a fake. Keeping it an
// injected dependency keeps the race package decoupled from relations.
type Targeter interface {
	IsHostile(self, other domain.Ship) bool
}

// state is the controller's persisted state (ai_state JSON). Anchor is the
// patrol centre, captured from the ship's position on the first tick.
type state struct {
	Race      int         `json:"race"`
	Order     Order       `json:"order"`
	Anchor    domain.Vec2 `json:"anchor"`
	HasAnchor bool        `json:"hasAnchor"`
	Phase     float64     `json:"phase"`
}

// Controller is one race ship's reactive AI.
type Controller struct {
	cfg      Config
	targeter Targeter
	st       state
}

func (c *Controller) Kind() string { return Kind }

// MarshalState serializes the controller's state for the ai_state snapshot.
func (c *Controller) MarshalState() ([]byte, error) { return json.Marshal(c.st) }

// Order reports the controller's current behaviour (test/inspection helper).
func (c *Controller) CurrentOrder() Order { return c.st.Order }

// Tick decides the ship's action for this tick: engage the nearest hostile
// in range, retreat when the hull is low, otherwise patrol around the anchor.
func (c *Controller) Tick(_ context.Context, view ai.WorldView) ai.Action {
	self := view.Self()
	if !c.st.HasAnchor {
		c.st.Anchor = self.Pos
		c.st.HasAnchor = true
	}

	ships := view.Ships()
	nearest, found := c.nearestHostile(self, ships)

	if found {
		if c.hullFraction(self) < c.cfg.FleeThreshold {
			c.st.Order = OrderRetreat
			return ai.MoveTo{Target: c.fleePoint(self.Pos, nearest.Pos)}
		}
		// Focus-fire (8.4): if an ally is already engaging a hostile in range,
		// converge on it instead of the nearest — race ships pack onto one
		// target ("call for help"). Falls back to the nearest hostile.
		target := nearest
		if focus, ok := c.allyFocusTarget(self, ships); ok {
			target = focus
		}
		c.st.Order = OrderEngage
		return ai.Attack{Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target.ID)}}
	}

	c.st.Order = OrderPatrol
	c.st.Phase += c.cfg.PatrolStep
	return ai.MoveTo{Target: c.patrolPoint()}
}

// allyFocusTarget returns the nearest in-range hostile that a same-side ally
// (a non-hostile ship per the Targeter) is already attacking — the focus-fire
// pick (8.4). found=false when no ally is engaging a hostile in range.
//
// Allies are identified as "not hostile to me" because race NPCs share the
// system player and the relations oracle reports them mutually non-hostile;
// distinguishing individual races needs a per-ship race field (deferred).
func (c *Controller) allyFocusTarget(self domain.Ship, ships []domain.Ship) (domain.Ship, bool) {
	engaged := make(map[domain.ShipID]bool)
	for _, o := range ships {
		if o.ID == self.ID || o.HP <= 0 || c.targeter.IsHostile(self, o) {
			continue
		}
		if o.AttackTarget != nil && o.AttackTarget.Kind == domain.EntityKindShip {
			engaged[domain.ShipID(o.AttackTarget.ID)] = true
		}
	}
	if len(engaged) == 0 {
		return domain.Ship{}, false
	}
	rangeSq := c.cfg.DetectionRange * c.cfg.DetectionRange
	var best domain.Ship
	bestSq := math.MaxFloat64
	found := false
	for _, o := range ships {
		if o.ID == self.ID || o.HP <= 0 || !engaged[o.ID] || !c.targeter.IsHostile(self, o) {
			continue
		}
		d := o.Pos.Sub(self.Pos)
		sq := d.X*d.X + d.Y*d.Y
		if sq > rangeSq || sq >= bestSq {
			continue
		}
		best, bestSq, found = o, sq, true
	}
	return best, found
}

// nearestHostile returns the closest live hostile ship within DetectionRange,
// or found=false when there is none.
func (c *Controller) nearestHostile(self domain.Ship, ships []domain.Ship) (domain.Ship, bool) {
	rangeSq := c.cfg.DetectionRange * c.cfg.DetectionRange
	var best domain.Ship
	bestSq := math.MaxFloat64
	found := false
	for _, other := range ships {
		if other.ID == self.ID || other.HP <= 0 {
			continue
		}
		if !c.targeter.IsHostile(self, other) {
			continue
		}
		d := other.Pos.Sub(self.Pos)
		sq := d.X*d.X + d.Y*d.Y
		if sq > rangeSq || sq >= bestSq {
			continue
		}
		best, bestSq, found = other, sq, true
	}
	return best, found
}

func (c *Controller) hullFraction(self domain.Ship) float64 {
	if self.MaxHP <= 0 {
		return 1
	}
	return float64(self.HP) / float64(self.MaxHP)
}

// fleePoint is a point PatrolRadius away from the threat, on the line from
// the threat through the ship (i.e. directly away). Falls back to the anchor
// when the ship and threat overlap exactly.
func (c *Controller) fleePoint(selfPos, threatPos domain.Vec2) domain.Vec2 {
	dir := selfPos.Sub(threatPos)
	length := dir.Length()
	if length == 0 {
		return c.st.Anchor
	}
	return selfPos.Add(dir.Scale(c.cfg.PatrolRadius / length))
}

func (c *Controller) patrolPoint() domain.Vec2 {
	return c.st.Anchor.Add(domain.Vec2{
		X: c.cfg.PatrolRadius * math.Cos(c.st.Phase),
		Y: c.cfg.PatrolRadius * math.Sin(c.st.Phase),
	})
}
