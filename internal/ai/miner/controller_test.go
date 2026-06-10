package miner_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/miner"
	"spaceempire/back/internal/domain"
)

// fakeWorld is a minimal ai.WorldView for miner controller tests.
type fakeWorld struct {
	self      domain.Ship
	asteroids []domain.Asteroid
}

func (w fakeWorld) Self() domain.Ship             { return w.self }
func (w fakeWorld) Ships() []domain.Ship          { return []domain.Ship{w.self} }
func (w fakeWorld) Statics() domain.SectorStatics { return domain.SectorStatics{} }
func (w fakeWorld) Asteroids() []domain.Asteroid  { return w.asteroids }

const oreType = domain.GoodsTypeID(2)

var (
	homeLeg = miner.Leg{
		Sector: 1,
		Pos:    domain.Vec2{X: 100, Y: 100},
		Ref:    domain.EntityRef{Kind: domain.EntityKindStation, ID: 1},
	}
	astTarget = miner.Target{ID: 7, Sector: 1, Pos: domain.Vec2{X: 60, Y: 140}}
)

// liveAsteroid is the seeded asteroid the target points at, with mass to spare.
func liveAsteroid() domain.Asteroid {
	return domain.Asteroid{ID: 7, SectorID: 1, Pos: astTarget.Pos, Mass: 100, OreType: oreType}
}

func testCfg() miner.Config {
	return miner.Config{ArriveRadius: 6, MineRange: 12, DrillRate: 5, LoadTarget: 5}
}

func newMiner(t *testing.T, cfg miner.Config) *miner.Controller {
	t.Helper()
	reg := ai.NewRegistry()
	miner.Register(reg, cfg)
	stateJSON, err := miner.NewInitialState(homeLeg, oreType, astTarget)
	require.NoError(t, err)
	c, err := reg.Build(miner.Kind, stateJSON)
	require.NoError(t, err)
	mc, ok := c.(*miner.Controller)
	require.True(t, ok)
	return mc
}

func tick(c *miner.Controller, self domain.Ship, asteroids []domain.Asteroid) ai.Action {
	return c.Tick(context.Background(), fakeWorld{self: self, asteroids: asteroids})
}

// shipAt is a parked (Vel zero) ship at pos in sector 1.
func shipAt(pos domain.Vec2) domain.Ship {
	return domain.Ship{ID: 5, SectorID: 1, Pos: pos}
}

func TestUnit_Miner_SetsCourseToAsteroidWhenFar(t *testing.T) {
	t.Parallel()
	c := newMiner(t, testCfg())
	// Freshly spawned at the home factory, asteroid far away (~56 units).
	act := tick(c, shipAt(homeLeg.Pos), []domain.Asteroid{liveAsteroid()})
	sc, ok := act.(ai.SetCourse)
	require.True(t, ok, "expected SetCourse to asteroid, got %T", act)
	assert.Equal(t, astTarget.Sector, sc.Course.Sector)
	assert.Equal(t, astTarget.Pos, sc.Course.Pos)
	assert.Nil(t, sc.Course.Approach, "asteroid course has no docking approach")
	assert.Equal(t, miner.PhaseToAsteroid, c.CurrentPhase())
}

func TestUnit_Miner_MinesWhenInRange(t *testing.T) {
	t.Parallel()
	c := newMiner(t, testCfg())
	// Within MineRange of the asteroid.
	act := tick(c, shipAt(domain.Vec2{X: 62, Y: 142}), []domain.Asteroid{liveAsteroid()})
	m, ok := act.(ai.Mine)
	require.True(t, ok, "expected Mine, got %T", act)
	assert.Equal(t, astTarget.ID, m.Asteroid)
	assert.EqualValues(t, 5, m.Amount, "drills DrillRate per tick")
	assert.Equal(t, miner.PhaseMining, c.CurrentPhase())
}

func TestUnit_Miner_GoesHomeWhenLoaded(t *testing.T) {
	t.Parallel()
	c := newMiner(t, testCfg()) // LoadTarget 5, DrillRate 5
	inRange := shipAt(domain.Vec2{X: 62, Y: 142})
	tick(c, inRange, []domain.Asteroid{liveAsteroid()}) // → mining, Mined=5

	// Second mining tick: load target reached → head home (asteroid still alive).
	act := tick(c, inRange, []domain.Asteroid{liveAsteroid()})
	sc, ok := act.(ai.SetCourse)
	require.True(t, ok, "expected SetCourse home, got %T", act)
	assert.Equal(t, homeLeg.Sector, sc.Course.Sector)
	assert.Equal(t, homeLeg.Pos, sc.Course.Pos)
	require.NotNil(t, sc.Course.Approach, "home course parks at the factory")
	assert.Equal(t, homeLeg.Ref, *sc.Course.Approach)
	assert.Equal(t, miner.PhaseToHome, c.CurrentPhase())
}

func TestUnit_Miner_GoesHomeWhenAsteroidDepleted(t *testing.T) {
	t.Parallel()
	c := newMiner(t, miner.Config{ArriveRadius: 6, MineRange: 12, DrillRate: 5, LoadTarget: 40})
	inRange := shipAt(domain.Vec2{X: 62, Y: 142})
	tick(c, inRange, []domain.Asteroid{liveAsteroid()}) // → mining

	// Asteroid mined out by the worker (gone from the world) before the load
	// target is met → head home with whatever was drilled.
	act := tick(c, inRange, nil)
	_, ok := act.(ai.SetCourse)
	require.True(t, ok, "expected SetCourse home after depletion, got %T", act)
	assert.Equal(t, miner.PhaseToHome, c.CurrentPhase())
}

func TestUnit_Miner_UnloadsAtHomeThenRepicks(t *testing.T) {
	t.Parallel()
	c := newMiner(t, testCfg())
	inRange := shipAt(domain.Vec2{X: 62, Y: 142})
	tick(c, inRange, []domain.Asteroid{liveAsteroid()}) // → mining, Mined=5
	tick(c, inRange, []domain.Asteroid{liveAsteroid()}) // loaded → to_home

	// Parked at the home factory: unload the whole hold, go idle.
	act := tick(c, shipAt(homeLeg.Pos), []domain.Asteroid{liveAsteroid()})
	tr, ok := act.(ai.Transfer)
	require.True(t, ok, "expected Transfer (unload), got %T", act)
	assert.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 5}, tr.From, "unload pushes from the ship")
	assert.Equal(t, homeLeg.Ref, tr.To)
	assert.Equal(t, oreType, tr.GoodsType)
	assert.Equal(t, miner.PhaseIdle, c.CurrentPhase())

	// Idle at the factory with a live asteroid in the sector → set out again.
	act = tick(c, shipAt(homeLeg.Pos), []domain.Asteroid{liveAsteroid()})
	sc, ok := act.(ai.SetCourse)
	require.True(t, ok, "expected SetCourse to a freshly picked asteroid, got %T", act)
	assert.Equal(t, astTarget.Pos, sc.Course.Pos)
	assert.Equal(t, miner.PhaseToAsteroid, c.CurrentPhase())
}

func TestUnit_Miner_IdleWaitsWhenNoAsteroid(t *testing.T) {
	t.Parallel()
	c := newMiner(t, testCfg())
	inRange := shipAt(domain.Vec2{X: 62, Y: 142})
	tick(c, inRange, []domain.Asteroid{liveAsteroid()})             // → mining
	tick(c, inRange, []domain.Asteroid{liveAsteroid()})             // loaded → to_home
	tick(c, shipAt(homeLeg.Pos), []domain.Asteroid{liveAsteroid()}) // unload → idle
	require.Equal(t, miner.PhaseIdle, c.CurrentPhase())

	// No asteroids left in the sector → wait at the factory.
	act := tick(c, shipAt(homeLeg.Pos), nil)
	_, ok := act.(ai.Idle)
	assert.True(t, ok, "expected Idle with no asteroids in radius, got %T", act)
	assert.Equal(t, miner.PhaseIdle, c.CurrentPhase())
}

func TestUnit_Miner_IdleSkipsWrongOreType(t *testing.T) {
	t.Parallel()
	c := newMiner(t, testCfg())
	inRange := shipAt(domain.Vec2{X: 62, Y: 142})
	tick(c, inRange, []domain.Asteroid{liveAsteroid()})
	tick(c, inRange, []domain.Asteroid{liveAsteroid()})
	tick(c, shipAt(homeLeg.Pos), []domain.Asteroid{liveAsteroid()})

	// Only a different-ore asteroid is present → stays idle.
	other := domain.Asteroid{ID: 9, SectorID: 1, Pos: domain.Vec2{X: 50, Y: 50}, Mass: 100, OreType: oreType + 1}
	act := tick(c, shipAt(homeLeg.Pos), []domain.Asteroid{other})
	_, ok := act.(ai.Idle)
	assert.True(t, ok, "miner ignores asteroids of the wrong ore type, got %T", act)
}

func TestUnit_Miner_StateSurvivesRebuild(t *testing.T) {
	t.Parallel()
	// LoadTarget high enough that one drill tick does not fill the hold, so a
	// rebuilt miner is still mid-drill rather than already heading home.
	cfg := miner.Config{ArriveRadius: 6, MineRange: 12, DrillRate: 5, LoadTarget: 40}
	a := newMiner(t, cfg)
	a.Tick(context.Background(), fakeWorld{self: shipAt(domain.Vec2{X: 62, Y: 142}), asteroids: []domain.Asteroid{liveAsteroid()}}) // → mining
	saved, err := a.MarshalState()
	require.NoError(t, err)

	// Rebuild (as a gate handoff would): phase + target survive, so the miner
	// keeps drilling rather than restarting.
	reg := ai.NewRegistry()
	miner.Register(reg, cfg)
	rebuilt, err := reg.Build(miner.Kind, saved)
	require.NoError(t, err)
	b, ok := rebuilt.(*miner.Controller)
	require.True(t, ok)
	assert.Equal(t, miner.PhaseMining, b.CurrentPhase())

	act := b.Tick(context.Background(), fakeWorld{self: shipAt(domain.Vec2{X: 62, Y: 142}), asteroids: []domain.Asteroid{liveAsteroid()}})
	_, ok = act.(ai.Mine)
	assert.True(t, ok, "rebuilt miner keeps drilling, got %T", act)
}
