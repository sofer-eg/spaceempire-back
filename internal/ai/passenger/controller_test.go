package passenger_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/passenger"
	"spaceempire/back/internal/domain"
)

// fakeWorld is a minimal ai.WorldView for passenger controller tests — the
// controller reads only Self().
type fakeWorld struct {
	self domain.Ship
}

func (w fakeWorld) Self() domain.Ship             { return w.self }
func (w fakeWorld) Ships() []domain.Ship          { return []domain.Ship{w.self} }
func (w fakeWorld) Statics() domain.SectorStatics { return domain.SectorStatics{} }
func (w fakeWorld) Asteroids() []domain.Asteroid  { return nil }

var (
	homeLeg  = passenger.Leg{Sector: 1, Pos: domain.Vec2{X: 0, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}}
	stationA = passenger.Leg{Sector: 1, Pos: domain.Vec2{X: 100, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 10}}
	stationB = passenger.Leg{Sector: 2, Pos: domain.Vec2{X: 50, Y: 50}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 20}}
)

func testCfg() passenger.Config {
	return passenger.Config{ArriveRadius: 6, DockWaitTicks: 3, MaxPassengers: 33}
}

func newController(t *testing.T, cfg passenger.Config, pool []passenger.Leg) *passenger.Controller {
	t.Helper()
	reg := ai.NewRegistry()
	passenger.Register(reg, cfg)
	stateJSON, err := passenger.NewInitialState(homeLeg, pool, 0)
	require.NoError(t, err)
	c, err := reg.Build(passenger.Kind, stateJSON)
	require.NoError(t, err)
	pc, ok := c.(*passenger.Controller)
	require.True(t, ok)
	return pc
}

func tick(c *passenger.Controller, self domain.Ship) ai.Action {
	return c.Tick(context.Background(), fakeWorld{self: self})
}

// shipAt is a parked (Vel zero) ship at pos in the given sector.
func shipAt(sector domain.SectorID, pos domain.Vec2) domain.Ship {
	return domain.Ship{ID: 5, SectorID: sector, Pos: pos}
}

func TestUnit_Passenger_BoardsAndDepartsWhenWaitDone(t *testing.T) {
	t.Parallel()
	c := newController(t, testCfg(), []passenger.Leg{homeLeg, stationA, stationB})
	// Freshly spawned, parked at home, wait timer already zero.
	act := tick(c, shipAt(1, homeLeg.Pos))
	b, ok := act.(ai.BoardPassengers)
	require.True(t, ok, "expected BoardPassengers on departure, got %T", act)
	assert.Equal(t, 33, b.Max)
	assert.Equal(t, passenger.PhaseFlying, c.CurrentPhase())
}

func TestUnit_Passenger_SetsCourseToDestWhileFlying(t *testing.T) {
	t.Parallel()
	c := newController(t, testCfg(), []passenger.Leg{homeLeg, stationA, stationB})
	tick(c, shipAt(1, homeLeg.Pos)) // board → flying, Dest = stationA

	act := tick(c, shipAt(1, homeLeg.Pos)) // still at home, not arrived
	sc, ok := act.(ai.SetCourse)
	require.True(t, ok, "expected SetCourse to dest, got %T", act)
	assert.Equal(t, stationA.Sector, sc.Course.Sector)
	assert.Equal(t, stationA.Pos, sc.Course.Pos)
	require.NotNil(t, sc.Course.Approach, "passenger docks at the station")
	assert.Equal(t, stationA.Ref, *sc.Course.Approach)
}

func TestUnit_Passenger_DropsOnArrival(t *testing.T) {
	t.Parallel()
	c := newController(t, testCfg(), []passenger.Leg{homeLeg, stationA, stationB})
	tick(c, shipAt(1, homeLeg.Pos)) // board → flying, Dest = stationA

	act := tick(c, shipAt(1, stationA.Pos)) // parked at stationA
	_, ok := act.(ai.DropPassengers)
	require.True(t, ok, "expected DropPassengers on arrival, got %T", act)
	assert.Equal(t, passenger.PhaseWaiting, c.CurrentPhase())
}

func TestUnit_Passenger_WaitsThenDepartsToNextDest(t *testing.T) {
	t.Parallel()
	c := newController(t, testCfg(), []passenger.Leg{homeLeg, stationA, stationB})
	tick(c, shipAt(1, homeLeg.Pos))  // board → flying (Dest A)
	tick(c, shipAt(1, stationA.Pos)) // arrive → drop, waiting (WaitLeft=3)

	// Three idle ticks burn the dock timer.
	for i := 0; i < 3; i++ {
		act := tick(c, shipAt(1, stationA.Pos))
		_, ok := act.(ai.Idle)
		require.Truef(t, ok, "wait tick %d should be Idle, got %T", i, act)
	}

	// Timer expired → board for the next destination (stationB).
	act := tick(c, shipAt(1, stationA.Pos))
	_, ok := act.(ai.BoardPassengers)
	require.True(t, ok, "expected BoardPassengers after wait, got %T", act)

	// And it now routes to stationB (cross-sector), not back to stationA.
	sc, ok := tick(c, shipAt(1, stationA.Pos)).(ai.SetCourse)
	require.True(t, ok, "expected SetCourse to next dest")
	assert.Equal(t, stationB.Sector, sc.Course.Sector)
	assert.Equal(t, stationB.Pos, sc.Course.Pos)
}

func TestUnit_Passenger_StaysWhenNoOtherDestination(t *testing.T) {
	t.Parallel()
	// Pool holds only home → nowhere to ferry to.
	c := newController(t, testCfg(), []passenger.Leg{homeLeg})
	act := tick(c, shipAt(1, homeLeg.Pos))
	_, ok := act.(ai.Idle)
	assert.True(t, ok, "expected Idle with no other destination, got %T", act)
	assert.Equal(t, passenger.PhaseWaiting, c.CurrentPhase())
}

func TestUnit_Passenger_StateSurvivesRebuild(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	a := newController(t, cfg, []passenger.Leg{homeLeg, stationA, stationB})
	a.Tick(context.Background(), fakeWorld{self: shipAt(1, homeLeg.Pos)}) // → flying, Dest A
	saved, err := a.MarshalState()
	require.NoError(t, err)

	reg := ai.NewRegistry()
	passenger.Register(reg, cfg)
	rebuilt, err := reg.Build(passenger.Kind, saved)
	require.NoError(t, err)
	b, ok := rebuilt.(*passenger.Controller)
	require.True(t, ok)
	assert.Equal(t, passenger.PhaseFlying, b.CurrentPhase())

	// Rebuilt TS keeps heading to the same destination.
	sc, ok := b.Tick(context.Background(), fakeWorld{self: shipAt(1, homeLeg.Pos)}).(ai.SetCourse)
	require.True(t, ok, "rebuilt passenger still routes to dest")
	assert.Equal(t, stationA.Pos, sc.Course.Pos)
}
