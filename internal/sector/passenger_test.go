package sector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/passenger"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_Worker_Passenger_FerriesAndBoards is the phase 5.5 acceptance
// proof: a passenger TS parked at its home station boards a batch of
// passengers, flies to the next station, drops them on arrival, and the
// immediate Save trail shows passengers going up on departure and back to zero
// at the far station — all driven by the in-tick passenger controller, no
// player input.
func TestUnit_Worker_Passenger_FerriesAndBoards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	home := passenger.Leg{Sector: testSector, Pos: domain.Vec2{X: 0, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}}
	stationA := passenger.Leg{Sector: testSector, Pos: domain.Vec2{X: 100, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 10}}

	state, err := passenger.NewInitialState(home, []passenger.Leg{home, stationA}, 0)
	require.NoError(t, err)

	registry := ai.NewRegistry()
	passenger.Register(registry, passenger.Config{ArriveRadius: 6, DockWaitTicks: 2, MaxPassengers: 33})

	repo := &fakeShipRepo{}
	ship := domain.Ship{
		ID: 1, PlayerID: 0, SectorID: testSector,
		Pos: home.Pos, Direction: domain.Vec2{X: 1, Y: 0},
		MaxSpeed: 50, Acceleration: 50, TurnRate: math.Pi, HP: 100, MaxHP: 100,
	}

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, DockRange: 3, AOIRadius: 5000},
		clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithRouter(&stubRouter{}),
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: 1, SectorID: testSector, ControllerKind: passenger.Kind, StateJSON: state},
		}}),
	)

	// ~30 ticks ≈ board@home → fly → drop@A → wait → board@A → fly back.
	for i := 0; i < 30; i++ {
		w.Tick(ctx)
	}

	// Board wrote a positive passenger count (immediate Save on departure).
	boarded := false
	// Drop at the far station wrote zero passengers there (immediate Save on arrival).
	droppedAtA := false
	for _, s := range repo.saves {
		if s.Passengers > 0 {
			boarded = true
		}
		if s.Passengers == 0 && s.Pos.X > 50 {
			droppedAtA = true
		}
	}
	assert.True(t, boarded, "passenger TS must board (passengers > 0) on departure")
	assert.True(t, droppedAtA, "passengers must be dropped (0) on arrival at the far station")
}
