package sector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/miner"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeAsteroidRepo is an in-memory sector.AsteroidRepo: it records the last
// persisted mass and which asteroids were deleted, so the miner end-to-end
// test can assert depletion without a database.
type fakeAsteroidRepo struct {
	deleted  map[domain.AsteroidID]bool
	lastMass map[domain.AsteroidID]int64
}

func newFakeAsteroidRepo() *fakeAsteroidRepo {
	return &fakeAsteroidRepo{
		deleted:  make(map[domain.AsteroidID]bool),
		lastMass: make(map[domain.AsteroidID]int64),
	}
}

func (r *fakeAsteroidRepo) BatchUpdate(_ context.Context, as []domain.Asteroid) error {
	for _, a := range as {
		r.lastMass[a.ID] = a.Mass
	}
	return nil
}

func (r *fakeAsteroidRepo) Delete(_ context.Context, id domain.AsteroidID) error {
	r.deleted[id] = true
	return nil
}

// TestUnit_Worker_Miner_MinesAndUnloads is the phase 5.4 acceptance proof: a
// miner NPC parked at its home factory flies to an asteroid, drills it down to
// nothing (the asteroid is depleted and deleted), then hauls the ore back and
// unloads it at the factory — all driven by the in-tick miner controller +
// Mine handler + logistics, with no player input.
func TestUnit_Worker_Miner_MinesAndUnloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const ore = domain.GoodsTypeID(2)
	const astID = domain.AsteroidID(7)
	home := miner.Leg{Sector: testSector, Pos: domain.Vec2{X: 100, Y: 100}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}}
	target := miner.Target{ID: astID, Sector: testSector, Pos: domain.Vec2{X: 60, Y: 140}}

	// LoadTarget exceeds the asteroid's mass, so the miner drills it to zero
	// (proving depletion) before its hold target is met.
	state, err := miner.NewInitialState(home, ore, target)
	require.NoError(t, err)

	registry := ai.NewRegistry()
	miner.Register(registry, miner.Config{ArriveRadius: 6, MineRange: 12, DrillRate: 5, LoadTarget: 40})

	logistics := newFakeLogistics()
	astRepo := newFakeAsteroidRepo()
	asteroid := domain.Asteroid{ID: astID, SectorID: testSector, Pos: target.Pos, Mass: 20, OreType: ore}

	// Miner ship parked at the home factory (Vel zero), fast enough to reach
	// the asteroid and return in a handful of ticks.
	ship := domain.Ship{
		ID: 1, PlayerID: 0, SectorID: testSector,
		Pos: home.Pos, Direction: domain.Vec2{X: 1, Y: 0},
		MaxSpeed: 50, Acceleration: 50, TurnRate: math.Pi, HP: 100, MaxHP: 100,
	}

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, DockRange: 3, AOIRadius: 5000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithRouter(&stubRouter{}),
		sector.WithTraderLogistics(logistics),
		sector.WithMinerLogistics(logistics),
		sector.WithAsteroids(astRepo, map[domain.SectorID][]domain.Asteroid{testSector: {asteroid}}),
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: 1, SectorID: testSector, ControllerKind: miner.Kind, StateJSON: state},
		}}),
	)

	// 40 ticks ≈ fly-out + drill + fly-home + unload with margin.
	for i := 0; i < 40; i++ {
		w.Tick(ctx)
	}

	assert.True(t, astRepo.deleted[astID], "asteroid must be depleted and deleted")
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: 1}
	assert.Zero(t, logistics.store[shipRef], "ship hold must be empty after unloading")
	assert.Equal(t, int64(20), logistics.store[home.Ref], "all 20 mined ore must end up at the home factory")
}
