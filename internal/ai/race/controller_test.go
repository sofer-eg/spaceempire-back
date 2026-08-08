package race_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/domain"
)

// fakeWorld is a minimal ai.WorldView for controller tests.
type fakeWorld struct {
	self  domain.Ship
	ships []domain.Ship
}

func (w fakeWorld) Self() domain.Ship             { return w.self }
func (w fakeWorld) Ships() []domain.Ship          { return w.ships }
func (w fakeWorld) Statics() domain.SectorStatics { return domain.SectorStatics{} }
func (w fakeWorld) Asteroids() []domain.Asteroid  { return nil }

type fakeTargeter struct {
	fn func(self, other domain.Ship) bool
}

func (f fakeTargeter) IsHostile(self, other domain.Ship) bool { return f.fn(self, other) }

var (
	allHostile = fakeTargeter{fn: func(_, _ domain.Ship) bool { return true }}
	noHostile  = fakeTargeter{fn: func(_, _ domain.Ship) bool { return false }}
)

func newController(t *testing.T, targeter race.Targeter, cfg race.Config, stateJSON []byte) *race.Controller {
	t.Helper()
	reg := ai.NewRegistry()
	race.Register(reg, targeter, cfg)
	c, err := reg.Build(race.Kind, stateJSON)
	require.NoError(t, err)
	rc, ok := c.(*race.Controller)
	require.True(t, ok)
	return rc
}

func ship(id int64, pos domain.Vec2, hp, maxHP int) domain.Ship {
	return domain.Ship{ID: domain.ShipID(id), Pos: pos, HP: hp, MaxHP: maxHP}
}

func TestUnit_Race_PatrolWithoutEnemy(t *testing.T) {
	t.Parallel()
	c := newController(t, noHostile, race.Config{PatrolRadius: 150}, nil)
	self := ship(1, domain.Vec2{X: 0, Y: 0}, 100, 100)

	act := c.Tick(context.Background(), fakeWorld{self: self})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo on patrol, got %T", act)
	assert.Equal(t, race.OrderPatrol, c.CurrentOrder())
	// Patrol point sits on the circle of radius 150 around the anchor (0,0).
	assert.InDelta(t, 150.0, mv.Target.Length(), 1e-6)
}

// playersHostile treats real-player ships (PlayerID 99) as hostile and NPC
// allies (PlayerID 0) as friendly — the shape race AI sees in production.
var playersHostile = fakeTargeter{fn: func(_, other domain.Ship) bool { return other.PlayerID == 99 }}

func TestUnit_Race_FocusFiresAllyTarget(t *testing.T) {
	t.Parallel()
	c := newController(t, playersHostile, race.Config{DetectionRange: 600}, nil)
	self := domain.Ship{ID: 1, PlayerID: 0, Pos: domain.Vec2{}, HP: 100, MaxHP: 100}
	near := domain.Ship{ID: 3, PlayerID: 99, Pos: domain.Vec2{X: 50}, HP: 100, MaxHP: 100} // nearest hostile
	far := domain.Ship{ID: 4, PlayerID: 99, Pos: domain.Vec2{X: 200}, HP: 100, MaxHP: 100} // ally-engaged hostile
	ally := domain.Ship{ID: 2, PlayerID: 0, Pos: domain.Vec2{X: 20}, HP: 100, MaxHP: 100,  // NPC ally
		AttackTarget: &domain.EntityRef{Kind: domain.EntityKindShip, ID: 4}}

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, ally, near, far}})
	atk, ok := act.(ai.Attack)
	require.True(t, ok, "expected Attack, got %T", act)
	assert.Equal(t, int64(4), atk.Target.ID, "focus-fire the ally-engaged hostile, not the nearest")
}

func TestUnit_Race_FocusFallsBackToNearest(t *testing.T) {
	t.Parallel()
	c := newController(t, playersHostile, race.Config{DetectionRange: 600}, nil)
	self := domain.Ship{ID: 1, PlayerID: 0, Pos: domain.Vec2{}, HP: 100, MaxHP: 100}
	near := domain.Ship{ID: 3, PlayerID: 99, Pos: domain.Vec2{X: 50}, HP: 100, MaxHP: 100}
	far := domain.Ship{ID: 4, PlayerID: 99, Pos: domain.Vec2{X: 200}, HP: 100, MaxHP: 100}
	ally := domain.Ship{ID: 2, PlayerID: 0, Pos: domain.Vec2{X: 20}, HP: 100, MaxHP: 100} // not engaging anyone

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, ally, near, far}})
	atk, ok := act.(ai.Attack)
	require.True(t, ok, "expected Attack, got %T", act)
	assert.Equal(t, int64(3), atk.Target.ID, "no ally engagement → nearest hostile")
}

func TestUnit_Race_EngageHostileInRange(t *testing.T) {
	t.Parallel()
	c := newController(t, allHostile, race.Config{DetectionRange: 600}, nil)
	self := ship(1, domain.Vec2{X: 0, Y: 0}, 100, 100)
	enemy := ship(2, domain.Vec2{X: 100, Y: 0}, 100, 100)

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, enemy}})
	atk, ok := act.(ai.Attack)
	require.True(t, ok, "expected Attack, got %T", act)
	assert.Equal(t, domain.EntityKindShip, atk.Target.Kind)
	assert.Equal(t, int64(2), atk.Target.ID)
	assert.Equal(t, race.OrderEngage, c.CurrentOrder())
}

func TestUnit_Race_DoesNotAttackFriendly(t *testing.T) {
	t.Parallel()
	// A ship is in range, but the targeter says it is not hostile → patrol,
	// never attack (criterion: "не атакует союзников").
	c := newController(t, noHostile, race.Config{DetectionRange: 600}, nil)
	self := ship(1, domain.Vec2{X: 0, Y: 0}, 100, 100)
	ally := ship(2, domain.Vec2{X: 50, Y: 0}, 100, 100)

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, ally}})
	_, isAttack := act.(ai.Attack)
	assert.False(t, isAttack, "must not attack a non-hostile ship")
	assert.Equal(t, race.OrderPatrol, c.CurrentOrder())
}

func TestUnit_Race_IgnoresHostileOutOfRange(t *testing.T) {
	t.Parallel()
	c := newController(t, allHostile, race.Config{DetectionRange: 600}, nil)
	self := ship(1, domain.Vec2{X: 0, Y: 0}, 100, 100)
	far := ship(2, domain.Vec2{X: 1000, Y: 0}, 100, 100)

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, far}})
	_, isAttack := act.(ai.Attack)
	assert.False(t, isAttack)
	assert.Equal(t, race.OrderPatrol, c.CurrentOrder())
}

func TestUnit_Race_RetreatsOnLowHull(t *testing.T) {
	t.Parallel()
	c := newController(t, allHostile, race.Config{DetectionRange: 600, FleeThreshold: 0.3, PatrolRadius: 150}, nil)
	self := ship(1, domain.Vec2{X: 0, Y: 0}, 20, 100) // 20% hull < 30% threshold
	enemy := ship(2, domain.Vec2{X: 100, Y: 0}, 100, 100)

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, enemy}})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo (flee), got %T", act)
	assert.Equal(t, race.OrderRetreat, c.CurrentOrder())
	// Flees away from the threat (enemy at +x → flee toward -x).
	assert.Less(t, mv.Target.X, 0.0, "should move away from the threat")
}

// --- Squad coordination (8.4 / TASK-66): emergent flight groups ---

// warshipClassID marks a combat class (M3/M4/M6) in squadCfg's warship set.
const warshipClassID = domain.ShipClassID(42)

func squadCfg() race.Config {
	return race.Config{
		DetectionRange:   600,
		PatrolRadius:     150,
		FormationSpacing: 50,
		WarshipClassIDs:  map[domain.ShipClassID]bool{warshipClassID: true},
	}
}

func warship(id int64, raceID int16, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID: domain.ShipID(id), Race: domain.RaceID(raceID), ShipClassID: warshipClassID,
		Pos: pos, HP: 100, MaxHP: 100,
	}
}

func TestUnit_Race_FollowerHoldsFormationOffset(t *testing.T) {
	t.Parallel()
	c := newController(t, noHostile, squadCfg(), nil)
	self := warship(2, 1, domain.Vec2{X: 0, Y: 0})
	leader := warship(1, 1, domain.Vec2{X: 100, Y: 0}) // lowest ID → leader

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, leader}})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo (formation), got %T", act)
	assert.Equal(t, race.OrderPatrol, c.CurrentOrder())
	// Rank 1 wingman: one wedge row behind-left of the leader (spacing 50).
	assert.Equal(t, domain.Vec2{X: 50, Y: 50}, mv.Target)
}

func TestUnit_Race_LeaderPatrolsWithFollowers(t *testing.T) {
	t.Parallel()
	c := newController(t, noHostile, squadCfg(), nil)
	self := warship(1, 1, domain.Vec2{X: 0, Y: 0}) // lowest ID → leader
	follower := warship(2, 1, domain.Vec2{X: 100, Y: 0})

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, follower}})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo (patrol), got %T", act)
	assert.Equal(t, race.OrderPatrol, c.CurrentOrder())
	// The leader patrols the anchor circle exactly as a solo ship would.
	assert.InDelta(t, 150.0, mv.Target.Length(), 1e-6)
}

func TestUnit_Race_CivilianSameRaceNotInSquad(t *testing.T) {
	t.Parallel()
	c := newController(t, noHostile, squadCfg(), nil)
	self := warship(2, 1, domain.Vec2{X: 0, Y: 0})
	// Same race, lower ID, but a civilian class: if it counted as squad
	// leader, self would fly a formation offset toward it. It must not.
	civilian := domain.Ship{ID: 1, Race: 1, ShipClassID: 7, Pos: domain.Vec2{X: 300, Y: 0}, HP: 100, MaxHP: 100}

	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, civilian}})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo (patrol), got %T", act)
	// Solo patrol circle around the anchor, not a formation slot at the civilian.
	assert.InDelta(t, 150.0, mv.Target.Length(), 1e-6)
}

func TestUnit_Race_SquadRetreatsOnLosses(t *testing.T) {
	t.Parallel()
	c := newController(t, playersHostile, squadCfg(), nil)
	self := warship(1, 1, domain.Vec2{X: 0, Y: 0})
	ally2 := warship(2, 1, domain.Vec2{X: 60, Y: 0})
	ally3 := warship(3, 1, domain.Vec2{X: 0, Y: 60})
	enemy := domain.Ship{ID: 9, PlayerID: 99, Pos: domain.Vec2{X: 100, Y: 0}, HP: 100, MaxHP: 100}

	// Full squad of 3 engages: peak becomes 3.
	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, ally2, ally3, enemy}})
	_, ok := act.(ai.Attack)
	require.True(t, ok, "full squad should engage, got %T", act)

	// The peak must live in ai_state: rebuild the controller from a snapshot.
	saved, err := c.MarshalState()
	require.NoError(t, err)
	c2 := newController(t, playersHostile, squadCfg(), saved)

	// Both allies are gone (1 < 3×0.5): retreat to the anchor at full hull.
	act = c2.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, enemy}})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo (group retreat), got %T", act)
	assert.Equal(t, race.OrderRetreat, c2.CurrentOrder())
	assert.Equal(t, domain.Vec2{X: 0, Y: 0}, mv.Target, "group retreat heads to the anchor")
}

func TestUnit_Race_PeakRebasesAfterEnemyLeaves(t *testing.T) {
	t.Parallel()
	c := newController(t, playersHostile, squadCfg(), nil)
	self := warship(1, 1, domain.Vec2{X: 0, Y: 0})
	ally2 := warship(2, 1, domain.Vec2{X: 60, Y: 0})
	ally3 := warship(3, 1, domain.Vec2{X: 0, Y: 60})
	enemy := domain.Ship{ID: 9, PlayerID: 99, Pos: domain.Vec2{X: 100, Y: 0}, HP: 100, MaxHP: 100}

	// Peak = 3 while the enemy is around.
	c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, ally2, ally3, enemy}})

	// Enemy gone, allies lost for good: the peak rebases to the current 1.
	act := c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self}})
	_, ok := act.(ai.MoveTo)
	require.True(t, ok, "expected MoveTo (patrol), got %T", act)
	assert.Equal(t, race.OrderPatrol, c.CurrentOrder())

	// Enemy returns: 1 ≥ 1×0.5 — the lone survivor engages, no eternal retreat.
	act = c.Tick(context.Background(), fakeWorld{self: self, ships: []domain.Ship{self, enemy}})
	_, ok = act.(ai.Attack)
	require.True(t, ok, "rebased squad must engage again, got %T", act)
	assert.Equal(t, race.OrderEngage, c.CurrentOrder())
}

func TestUnit_Race_StateSurvivesRebuild(t *testing.T) {
	t.Parallel()
	cfg := race.Config{PatrolRadius: 150}
	a := newController(t, noHostile, cfg, nil)

	// First tick captures the anchor at (0,0).
	a.Tick(context.Background(), fakeWorld{self: ship(1, domain.Vec2{X: 0, Y: 0}, 100, 100)})
	saved, err := a.MarshalState()
	require.NoError(t, err)

	// Rebuild from saved state, then tick with the ship now far away. The
	// anchor must come from the saved state (0,0), not be re-captured at the
	// new position — proving HasAnchor/Anchor persisted.
	b := newController(t, noHostile, cfg, saved)
	act := b.Tick(context.Background(), fakeWorld{self: ship(1, domain.Vec2{X: 5000, Y: 5000}, 100, 100)})
	mv, ok := act.(ai.MoveTo)
	require.True(t, ok)
	assert.InDelta(t, 150.0, mv.Target.Length(), 1e-6,
		"patrol still circles the persisted anchor at the origin, not the new position")
}
