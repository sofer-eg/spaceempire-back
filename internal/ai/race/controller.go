// Package race is the NPC race AI (phase 5.2): a reactive per-ship
// controller on the 5.1 AI runtime. Each race ship independently patrols an
// anchor, engages hostile ships in detection range, and retreats when its
// hull drops below a threshold — the MVP of the old FleetAI (CFleet::Turn).
// Phase 8.4 adds emergent focus-fire: a ship prefers a hostile an ally is
// already engaging, so race ships pack onto one target. TASK-66 adds emergent
// squads: same-race warships of the sector form a flight group (leader =
// lowest ShipID), wingmen hold a wedge formation on patrol, and the whole
// squad retreats to the anchor when it has lost too many ships — all derived
// per tick from the WorldView, without a coordinator or fleet tables; see
// back/docs/specs/race_ai.md.
package race

import (
	"context"
	"encoding/json"
	"math"
	"slices"

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
	// FormationSpacing is the wedge spacing (world units) between formation
	// rows behind the squad leader (TASK-66, the pos_in_order successor).
	FormationSpacing float64
	// SquadRetreatFraction: with a hostile in range, the squad retreats to
	// the anchor once its live count drops below peak × fraction.
	SquadRetreatFraction float64
	// PeakRebaseTicks is the rebase hysteresis (TASK-208): the squad peak
	// drops to the current live count only after this many consecutive ticks
	// with no hostile in DetectionRange. A hostile flickering on the radius
	// edge therefore cannot collapse the peak and flip a group retreat back
	// into engagement (the kite oscillation).
	PeakRebaseTicks int
	// WarshipClassIDs marks the ship classes that count as squad members
	// (M2..M6 in production wiring, balance.ShipClass.IsWarship — npc_spawner
	// gives civilian TS a race too, so Race alone cannot define membership).
	// nil/empty → no squads, every ship behaves solo as before TASK-66.
	WarshipClassIDs map[domain.ShipClassID]bool
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
	if c.FormationSpacing <= 0 {
		c.FormationSpacing = 50
	}
	if c.SquadRetreatFraction <= 0 {
		c.SquadRetreatFraction = 0.5
	}
	if c.PeakRebaseTicks <= 0 {
		c.PeakRebaseTicks = 10
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
	// SquadPeak is the highest live squad size seen while a hostile was in
	// range (TASK-66 group retreat). Rebases to the current size once no
	// hostile has been around for PeakRebaseTicks consecutive ticks
	// (TASK-208). Absent in pre-TASK-66 snapshots → 0, raised on the first
	// tick.
	SquadPeak int `json:"squadPeak,omitempty"`
	// NoEnemyTicks counts consecutive ticks with no hostile in range, clamped
	// at PeakRebaseTicks (the TASK-208 rebase hysteresis). Any tick with a
	// hostile in range resets it. Absent in pre-TASK-208 snapshots → 0.
	NoEnemyTicks int `json:"noEnemyTicks,omitempty"`
	// Standalone (TASK-207) marks a ship that never joins emergent squads —
	// quest NPCs (escorted traders, protect targets) share the race controller
	// but must not become wingmen of a same-race navy or follow its group
	// retreat. Set by NewInitialStandaloneState; absent in older snapshots →
	// false (a normal squad member).
	Standalone bool `json:"standalone,omitempty"`
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

// Tick decides the ship's action for this tick. With a hostile in range:
// personal flee on low hull, then group retreat on squad losses, then engage
// with focus-fire. Without one: rebase the squad peak (only after
// PeakRebaseTicks consecutive quiet ticks, TASK-208) and patrol — the leader
// (and any solo ship) circles the anchor, wingmen hold a formation offset.
func (c *Controller) Tick(_ context.Context, view ai.WorldView) ai.Action {
	self := view.Self()
	if !c.st.HasAnchor {
		c.st.Anchor = self.Pos
		c.st.HasAnchor = true
	}

	ships := view.Ships()
	squad := c.squad(self, ships)
	nearest, found := c.nearestHostile(self, ships)

	if found {
		c.st.NoEnemyTicks = 0
		if len(squad) > c.st.SquadPeak {
			c.st.SquadPeak = len(squad)
		}
		if c.hullFraction(self) < c.cfg.FleeThreshold {
			c.st.Order = OrderRetreat
			return ai.MoveTo{Target: c.fleePoint(self.Pos, nearest.Pos)}
		}
		// Group retreat (TASK-66): the squad has lost too many ships since
		// the fight started — fall back to the anchor even at full hull,
		// firing at the nearest hostile on the way (TASK-208: an enemy
		// camping the anchor gets no free farm).
		if float64(len(squad)) < float64(c.st.SquadPeak)*c.cfg.SquadRetreatFraction {
			c.st.Order = OrderRetreat
			return ai.MoveAndFire{
				Target: c.st.Anchor,
				Fire:   domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(nearest.ID)},
			}
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

	// No hostile around: after PeakRebaseTicks consecutive quiet ticks,
	// rebase the peak to the current size so irreplaceable losses do not lock
	// the squad into an eternal retreat. The hysteresis (TASK-208) keeps a
	// hostile flickering on the detection edge from collapsing the peak
	// mid-retreat.
	if c.st.NoEnemyTicks < c.cfg.PeakRebaseTicks {
		c.st.NoEnemyTicks++
	}
	if c.st.NoEnemyTicks >= c.cfg.PeakRebaseTicks {
		c.st.SquadPeak = len(squad)
	}
	c.st.Order = OrderPatrol
	if leaderPos, rank, wingman := c.formationSlot(self, squad, ships); wingman {
		return ai.MoveTo{Target: leaderPos.Add(c.formationOffset(rank))}
	}
	c.st.Phase += c.cfg.PatrolStep
	return ai.MoveTo{Target: c.patrolPoint()}
}

// squad returns the ID-sorted flight group self belongs to: the sector's live
// same-race warships (WarshipClassIDs), self included. Empty when self is not
// a warship (civilians of the race never join; ShipClassID 0 — spacesuits /
// legacy rows — is never a warship class) or is standalone (TASK-207: quest
// NPCs behave solo — the pre-TASK-66 patrol/engage/flee, no formation, no
// group retreat).
func (c *Controller) squad(self domain.Ship, ships []domain.Ship) []domain.ShipID {
	if c.st.Standalone || !c.cfg.WarshipClassIDs[self.ShipClassID] {
		return nil
	}
	ids := []domain.ShipID{self.ID}
	for _, o := range ships {
		if o.ID == self.ID || o.HP <= 0 || o.Race != self.Race || !c.cfg.WarshipClassIDs[o.ShipClassID] {
			continue
		}
		ids = append(ids, o.ID)
	}
	slices.Sort(ids)
	return ids
}

// formationSlot resolves self's place in the squad formation. The leader
// (lowest ShipID) and single-ship squads get wingman=false — they patrol the
// anchor circle as before. A wingman gets the leader's position plus its
// 1-based rank in the ID-sorted squad (the pos_in_order successor).
func (c *Controller) formationSlot(self domain.Ship, squad []domain.ShipID, ships []domain.Ship) (leaderPos domain.Vec2, rank int, wingman bool) {
	if len(squad) < 2 || squad[0] == self.ID {
		return domain.Vec2{}, 0, false
	}
	rank, _ = slices.BinarySearch(squad, self.ID)
	for _, o := range ships {
		if o.ID == squad[0] {
			return o.Pos, rank, true
		}
	}
	// Unreachable: a non-self leader always comes from the ships slice.
	return domain.Vec2{}, 0, false
}

// formationOffset is the deterministic wedge slot for a wingman: ranks 1 and 2
// sit one FormationSpacing row behind the leader on alternating sides, ranks
// 3 and 4 two rows behind, and so on. "Behind" is world -X — the wedge is a
// fixed-axis formation, deterministic regardless of the leader's heading.
func (c *Controller) formationOffset(rank int) domain.Vec2 {
	row := float64((rank + 1) / 2)
	side := 1.0
	if rank%2 == 0 {
		side = -1
	}
	return domain.Vec2{
		X: -row * c.cfg.FormationSpacing,
		Y: side * row * c.cfg.FormationSpacing,
	}
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
