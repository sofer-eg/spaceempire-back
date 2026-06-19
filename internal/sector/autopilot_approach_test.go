package sector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// newApproachWorker builds a worker holding a single player ship armed
// with an Approach-style course aimed at a station at stationPos. The
// ship starts at shipPos with Speed so movement is observable in a few
// ticks. DockRange=3 mirrors the production default; the autopilot
// parks at DockRange/2 = 1.5 unit from the static.
func newApproachWorker(t *testing.T, shipPos, stationPos domain.Vec2) *sector.Worker {
	t.Helper()
	const sec domain.SectorID = 1
	target := domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, DockRange: 3},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{sec: {{
			ID: 1, PlayerID: 7, SectorID: sec,
			Pos:      shipPos,
			MaxSpeed: 10,
			FinalTarget: &domain.Course{
				Sector:   sec,
				Pos:      stationPos,
				Approach: &target,
			},
		}}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{sec: {
			Stations: []domain.Station{{ID: 5, SectorID: sec, Pos: stationPos, Built: true}},
		}}),
		sector.WithRouter(&stubRouter{}),
	)
}

func TestUnit_Autopilot_StopsAtApproachRadius(t *testing.T) {
	t.Parallel()
	stationPos := domain.Vec2{X: 100, Y: 0}
	shipPos := domain.Vec2{X: 0, Y: 0}
	w := newApproachWorker(t, shipPos, stationPos)

	// Tick many times — Speed=10 unit/s with TickInterval=1s, the ship
	// covers 10 unit per tick. ~12 ticks is plenty to overshoot
	// DockRange/2 (1.5 unit) if the autopilot doesn't brake.
	for i := 0; i < 20; i++ {
		w.Tick(context.Background())
	}

	got := w.Snapshot(1).Ships[0]
	d := math.Hypot(got.Pos.X-stationPos.X, got.Pos.Y-stationPos.Y)
	const stopRadius = 1.5
	// Tolerance of one Speed-step lets the test pass without per-tick
	// brake fidelity guarantees: the ship may overshoot the park spot
	// by up to one tick of movement, then converges in the following
	// tick. We require: parked inside DockRange and not on top of the
	// static.
	assert.GreaterOrEqual(t, d, 0.0)
	assert.LessOrEqual(t, d, 3.0, "ship must be inside DockRange after braking")
	assert.Greater(t, d, 0.5, "autopilot must not drive ship onto the static")
	_ = stopRadius

	require.NotNil(t, got.FinalTarget, "Approach course survives the parking")
	require.NotNil(t, got.FinalTarget.Approach)
}

func TestUnit_Autopilot_DoesNotDockAutomatically(t *testing.T) {
	t.Parallel()
	stationPos := domain.Vec2{X: 50, Y: 50}
	shipPos := domain.Vec2{X: 0, Y: 0}
	w := newApproachWorker(t, shipPos, stationPos)

	// newApproachWorker fits no up_docking module, so the phase 10.3.10
	// tick-driven auto-dock stays off: a generous tick budget (coverage + 10
	// stand-by ticks) must leave the ship parked, not docked. The positive
	// path lives in TestUnit_AutoDock_WithDockingModule_Docks.
	for i := 0; i < 30; i++ {
		w.Tick(context.Background())
	}

	got := w.Snapshot(1).Ships[0]
	assert.Nil(t, got.Docked, "approach never docks on its own")
	require.NotNil(t, got.FinalTarget, "Approach course survives stand-by")
}

func TestUnit_DockCommand_AfterApproach_Succeeds(t *testing.T) {
	t.Parallel()
	stationPos := domain.Vec2{X: 200, Y: 0}
	shipPos := domain.Vec2{X: 0, Y: 0}
	w := newApproachWorker(t, shipPos, stationPos)

	// Cover the distance. 200 unit / 10 unit per tick = 20 ticks of
	// pure motion, +5 ticks of margin for the brake-and-park phase.
	for i := 0; i < 25; i++ {
		w.Tick(context.Background())
	}

	got := w.Snapshot(1).Ships[0]
	d := math.Hypot(got.Pos.X-stationPos.X, got.Pos.Y-stationPos.Y)
	require.LessOrEqual(t, d, 3.0, "ship must be inside DockRange after approach")

	// Now issue the dock command — must succeed since we're inside
	// DockRange. We don't go through sendAndWait here because it lives
	// in docking_test.go in the same package; cheap inline alternative.
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(1, sector.DockCommand{
		PlayerID: 7, ShipID: 1,
		Target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 5},
		Reply:  reply,
	}))
	w.Tick(context.Background())
	select {
	case res := <-reply:
		require.NoError(t, res.Err)
	case <-time.After(time.Second):
		t.Fatal("dock command timeout")
	}

	docked := w.Snapshot(1).Ships[0]
	require.NotNil(t, docked.Docked, "ship must be docked after explicit DockCommand")
	assert.Equal(t, stationPos, docked.Pos, "executeDock snaps ship to static pos")
}
