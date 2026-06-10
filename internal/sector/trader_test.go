package sector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/trader"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeLogistics is an in-memory sector.TraderLogistics: it tracks a single
// goods count per owner and records each haul, so the trader end-to-end test
// can assert cargo actually moved between stations without a database.
// Ticks run on one goroutine, so no locking is needed.
type fakeLogistics struct {
	store map[domain.EntityRef]int64
	loads int // hauls into a ship
	drops int // hauls out of a ship
}

func newFakeLogistics() *fakeLogistics {
	return &fakeLogistics{store: make(map[domain.EntityRef]int64)}
}

func (f *fakeLogistics) Haul(_ context.Context, from, to domain.EntityRef, _ domain.GoodsTypeID, maxUnits int64) error {
	qty := f.store[from]
	if qty > maxUnits {
		qty = maxUnits
	}
	if qty <= 0 {
		return nil
	}
	f.store[from] -= qty
	f.store[to] += qty
	if to.Kind == domain.EntityKindShip {
		f.loads++
	}
	if from.Kind == domain.EntityKindShip {
		f.drops++
	}
	return nil
}

// AddOre satisfies sector.MinerLogistics: it deposits drilled ore into the
// ship's hold (phase 5.4). It shares the same store as Haul, so a miner test
// can assert that mined ore later unloads to its home factory.
func (f *fakeLogistics) AddOre(_ context.Context, ship domain.EntityRef, _ domain.GoodsTypeID, qty int64) error {
	f.store[ship] += qty
	return nil
}

// TestUnit_Worker_Trader_HaulsCargo is the phase 5.3 acceptance proof: a
// trader NPC parked at its home factory loads goods, flies to the destination
// trade station, unloads them, and the cargo counts at both ends change —
// driven entirely by the in-tick trader controller + TraderLogistics, with no
// player input.
func TestUnit_Worker_Trader_HaulsCargo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const goods = domain.GoodsTypeID(42)
	home := trader.Leg{Sector: testSector, Pos: domain.Vec2{X: 100, Y: 100}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}}
	dest := trader.Leg{Sector: testSector, Pos: domain.Vec2{X: -100, Y: -100}, Ref: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 7}}

	logistics := newFakeLogistics()
	logistics.store[home.Ref] = 100 // the factory starts with stock to ship

	state, err := trader.NewInitialState(home, dest, goods, 20)
	require.NoError(t, err)

	registry := ai.NewRegistry()
	trader.Register(registry, trader.Config{ArriveRadius: 6})

	// Trader ship parked at the home factory (Vel zero), fast enough to cross
	// the sector in a handful of ticks.
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
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: 1, SectorID: testSector, ControllerKind: trader.Kind, StateJSON: state},
		}}),
	)

	// 40 ticks ≈ one full load→fly→unload→fly-home loop with margin.
	for i := 0; i < 40; i++ {
		w.Tick(ctx)
	}

	assert.GreaterOrEqual(t, logistics.loads, 1, "trader must load at the home factory")
	assert.GreaterOrEqual(t, logistics.drops, 1, "trader must unload at the destination")
	assert.Less(t, logistics.store[home.Ref], int64(100), "home factory stock must drop")
	assert.Greater(t, logistics.store[dest.Ref], int64(0), "destination must receive cargo")
}
