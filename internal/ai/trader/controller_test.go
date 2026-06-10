package trader_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/trader"
	"spaceempire/back/internal/domain"
)

// fakeWorld is a minimal ai.WorldView for controller tests.
type fakeWorld struct {
	self domain.Ship
}

func (w fakeWorld) Self() domain.Ship             { return w.self }
func (w fakeWorld) Ships() []domain.Ship          { return []domain.Ship{w.self} }
func (w fakeWorld) Statics() domain.SectorStatics { return domain.SectorStatics{} }
func (w fakeWorld) Asteroids() []domain.Asteroid  { return nil }

var (
	homeLeg = trader.Leg{
		Sector: 1,
		Pos:    domain.Vec2{X: 100, Y: 100},
		Ref:    domain.EntityRef{Kind: domain.EntityKindStation, ID: 1},
	}
	destLeg = trader.Leg{
		Sector: 2,
		Pos:    domain.Vec2{X: 0, Y: 0},
		Ref:    domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 7},
	}
)

const haulGoods = domain.GoodsTypeID(42)

func newController(t *testing.T, cfg trader.Config) *trader.Controller {
	t.Helper()
	reg := ai.NewRegistry()
	trader.Register(reg, cfg)
	stateJSON, err := trader.NewInitialState(homeLeg, destLeg, haulGoods, 20)
	require.NoError(t, err)
	c, err := reg.Build(trader.Kind, stateJSON)
	require.NoError(t, err)
	tc, ok := c.(*trader.Controller)
	require.True(t, ok)
	return tc
}

func newControllerFrom(t *testing.T, cfg trader.Config, stateJSON []byte) *trader.Controller {
	t.Helper()
	reg := ai.NewRegistry()
	trader.Register(reg, cfg)
	c, err := reg.Build(trader.Kind, stateJSON)
	require.NoError(t, err)
	tc, ok := c.(*trader.Controller)
	require.True(t, ok)
	return tc
}

func TestUnit_Trader_LoadsWhenParkedAtHome(t *testing.T) {
	t.Parallel()
	c := newController(t, trader.Config{ArriveRadius: 6})
	// Freshly spawned: parked (Vel zero) at the home factory.
	self := domain.Ship{ID: 5, SectorID: 1, Pos: homeLeg.Pos}

	act := c.Tick(context.Background(), fakeWorld{self: self})
	tr, ok := act.(ai.Transfer)
	require.True(t, ok, "expected Transfer (load), got %T", act)
	assert.Equal(t, homeLeg.Ref, tr.From, "load pulls from the home factory")
	assert.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 5}, tr.To)
	assert.Equal(t, haulGoods, tr.GoodsType)
	assert.Equal(t, int64(20), tr.MaxUnits)
	assert.Equal(t, trader.PhaseDest, c.CurrentPhase(), "phase flips to dest after loading")
}

func TestUnit_Trader_SetsCourseToDestWhenNotArrived(t *testing.T) {
	t.Parallel()
	c := newController(t, trader.Config{ArriveRadius: 6})
	// In dest phase but still parked at home (different sector) → must aim
	// the autopilot at the destination.
	c.Tick(context.Background(), fakeWorld{self: domain.Ship{ID: 5, SectorID: 1, Pos: homeLeg.Pos}}) // load, → dest

	act := c.Tick(context.Background(), fakeWorld{self: domain.Ship{ID: 5, SectorID: 1, Pos: homeLeg.Pos}})
	sc, ok := act.(ai.SetCourse)
	require.True(t, ok, "expected SetCourse to dest, got %T", act)
	assert.Equal(t, destLeg.Sector, sc.Course.Sector)
	assert.Equal(t, destLeg.Pos, sc.Course.Pos)
	require.NotNil(t, sc.Course.Approach)
	assert.Equal(t, destLeg.Ref, *sc.Course.Approach, "approach the destination station")
}

func TestUnit_Trader_IdlesWhileEnRoute(t *testing.T) {
	t.Parallel()
	c := newController(t, trader.Config{ArriveRadius: 6})
	c.Tick(context.Background(), fakeWorld{self: domain.Ship{ID: 5, SectorID: 1, Pos: homeLeg.Pos}}) // load, → dest

	// Course already set toward dest and the ship is moving in an
	// intermediate sector → leave the autopilot alone.
	enRoute := domain.Ship{
		ID:          5,
		SectorID:    3,
		Pos:         domain.Vec2{X: 400, Y: 0},
		Vel:         domain.Vec2{X: 5, Y: 0},
		FinalTarget: &domain.Course{Sector: destLeg.Sector, Pos: destLeg.Pos},
	}
	act := c.Tick(context.Background(), fakeWorld{self: enRoute})
	_, ok := act.(ai.Idle)
	assert.True(t, ok, "expected Idle while the autopilot flies, got %T", act)
}

func TestUnit_Trader_UnloadsWhenParkedAtDest(t *testing.T) {
	t.Parallel()
	c := newController(t, trader.Config{ArriveRadius: 6})
	c.Tick(context.Background(), fakeWorld{self: domain.Ship{ID: 5, SectorID: 1, Pos: homeLeg.Pos}}) // load, → dest

	// Parked at the destination station (Vel zero, within ArriveRadius).
	atDest := domain.Ship{ID: 5, SectorID: destLeg.Sector, Pos: domain.Vec2{X: 2, Y: 0}}
	act := c.Tick(context.Background(), fakeWorld{self: atDest})
	tr, ok := act.(ai.Transfer)
	require.True(t, ok, "expected Transfer (unload), got %T", act)
	assert.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 5}, tr.From, "unload pushes from the ship")
	assert.Equal(t, destLeg.Ref, tr.To)
	assert.Equal(t, haulGoods, tr.GoodsType)
	assert.Equal(t, trader.PhaseHome, c.CurrentPhase(), "phase flips back to home after unloading")
}

func TestUnit_Trader_StateSurvivesRebuild(t *testing.T) {
	t.Parallel()
	cfg := trader.Config{ArriveRadius: 6}
	a := newController(t, cfg)
	a.Tick(context.Background(), fakeWorld{self: domain.Ship{ID: 5, SectorID: 1, Pos: homeLeg.Pos}}) // → dest
	saved, err := a.MarshalState()
	require.NoError(t, err)

	// Rebuild (as a gate handoff would): phase and route must survive, so
	// the trader keeps heading to the destination rather than restarting.
	b := newControllerFrom(t, cfg, saved)
	assert.Equal(t, trader.PhaseDest, b.CurrentPhase())
	act := b.Tick(context.Background(), fakeWorld{self: domain.Ship{ID: 5, SectorID: 3, Pos: domain.Vec2{X: 400, Y: 0}}})
	sc, ok := act.(ai.SetCourse)
	require.True(t, ok, "rebuilt trader still routes to dest, got %T", act)
	assert.Equal(t, destLeg.Sector, sc.Course.Sector)
}
