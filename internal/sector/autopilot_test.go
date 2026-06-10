package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// stubRouter is a tiny PathRouter implementation that returns canned
// answers so autopilot tests don't have to stand up a full Topology.
type stubRouter struct {
	next       map[domain.SectorID]domain.SectorID // from → next-hop on path to target
	gate       *domain.Gate                        // returned by GateBetween
	unreached  bool                                // forces NextSector to return false
	noGateLink bool                                // forces GateBetween to return nil
}

func (s *stubRouter) NextSector(from, _ domain.SectorID) (domain.SectorID, bool) {
	if s.unreached {
		return 0, false
	}
	n, ok := s.next[from]
	return n, ok
}

func (s *stubRouter) GateBetween(_, _ domain.SectorID) *domain.Gate {
	if s.noGateLink {
		return nil
	}
	return s.gate
}

func TestUnit_Autopilot_SameSector_SetsTargetToFinalPos(t *testing.T) {
	t.Parallel()

	router := &stubRouter{}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 1, SectorID: 1, MaxSpeed: 5,
			FinalTarget: &domain.Course{Sector: 1, Pos: domain.Vec2{X: 200, Y: 0}},
		}}},
		sector.WithRouter(router),
	)

	w.Tick(context.Background())

	snap := w.Snapshot(1)
	require.Len(t, snap.Ships, 1)
	require.NotNil(t, snap.Ships[0].Target)
	assert.Equal(t, domain.Vec2{X: 200, Y: 0}, *snap.Ships[0].Target)
}

func TestUnit_Autopilot_OtherSector_TargetsGateExit(t *testing.T) {
	t.Parallel()

	gate := &domain.Gate{
		ID:      10,
		SectorA: 1, PosA: domain.Vec2{X: 100, Y: 0},
		SectorB: 2, PosB: domain.Vec2{X: -100, Y: 0},
	}
	router := &stubRouter{
		next: map[domain.SectorID]domain.SectorID{1: 2},
		gate: gate,
	}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 1, SectorID: 1, MaxSpeed: 5,
			FinalTarget: &domain.Course{Sector: 3, Pos: domain.Vec2{X: 0, Y: 0}},
		}}},
		sector.WithRouter(router),
	)

	w.Tick(context.Background())

	require.NotNil(t, w.Snapshot(1).Ships[0].Target)
	assert.Equal(t, domain.Vec2{X: 100, Y: 0}, *w.Snapshot(1).Ships[0].Target,
		"target must be the gate exit on the current side")
}

func TestUnit_Autopilot_UnreachableDropsAutopilot(t *testing.T) {
	t.Parallel()

	router := &stubRouter{unreached: true}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 1, SectorID: 1,
			FinalTarget: &domain.Course{Sector: 99, Pos: domain.Vec2{}},
		}}},
		sector.WithRouter(router),
	)

	w.Tick(context.Background())

	snap := w.Snapshot(1)
	require.Len(t, snap.Ships, 1)
	assert.Nil(t, snap.Ships[0].FinalTarget, "FinalTarget must be cleared")
	assert.Nil(t, snap.Ships[0].Target)
}

func TestUnit_Autopilot_AutoJumpAtGate(t *testing.T) {
	t.Parallel()

	// Real router over a 2-sector topology — exercises both resolveAutopilot
	// and tryAutoJump end-to-end.
	sectors := []domain.Sector{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}
	gates := []domain.Gate{{
		ID:      10,
		SectorA: 1, PosA: domain.Vec2{X: 100, Y: 0},
		SectorB: 2, PosB: domain.Vec2{X: -100, Y: 0},
	}}
	topo := world.New(sectors, gates)
	pathRouter := world.NewPathRouter(topo, nil)
	jumpBus := bus.NewInMemory(16)
	t.Cleanup(jumpBus.Close)

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{
			1: {{
				ID: 1, PlayerID: 1, SectorID: 1,
				Pos:         domain.Vec2{X: 90, Y: 0}, // already within GateRange (50) of gate at (100,0)
				FinalTarget: &domain.Course{Sector: 2, Pos: domain.Vec2{X: 0, Y: 0}},
			}},
			2: {},
		},
		sector.WithHandoff(topo, jumpBus),
		sector.WithRouter(pathRouter),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.EnsureSubscriptions(ctx)
	w.Tick(ctx)

	assert.Empty(t, w.Snapshot(1).Ships, "ship must leave sector 1 via auto-jump")
	// Auto-jump publishes onto the bus; the same worker's intake handler
	// enqueues a JumpIntakeCommand asynchronously. Poll the inbox by
	// ticking until it has been picked up — Eventually wakes Tick on
	// every probe so the intake doesn't sit there indefinitely.
	require.Eventually(t, func() bool {
		w.Tick(ctx)
		return len(w.Snapshot(2).Ships) == 1
	}, time.Second, 10*time.Millisecond, "ship must materialise in sector 2")

	got := w.Snapshot(2).Ships[0]
	assert.Equal(t, domain.SectorID(2), got.SectorID)
	assert.Equal(t, domain.Vec2{X: -100, Y: 0}, got.Pos, "ship enters at the B-side gate exit")
	require.NotNil(t, got.FinalTarget, "FinalTarget survives the jump")
	assert.Equal(t, domain.SectorID(2), got.FinalTarget.Sector)
}

func TestUnit_Autopilot_FarFromGate_DoesNotJump(t *testing.T) {
	t.Parallel()

	sectors := []domain.Sector{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}
	gates := []domain.Gate{{
		ID:      10,
		SectorA: 1, PosA: domain.Vec2{X: 100, Y: 0},
		SectorB: 2, PosB: domain.Vec2{X: -100, Y: 0},
	}}
	topo := world.New(sectors, gates)
	pathRouter := world.NewPathRouter(topo, nil)
	jumpBus := bus.NewInMemory(16)
	t.Cleanup(jumpBus.Close)

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{
			1: {{
				ID: 1, PlayerID: 1, SectorID: 1,
				Pos:         domain.Vec2{X: 0, Y: 0}, // 100 units from gate; outside GateRange.
				FinalTarget: &domain.Course{Sector: 2, Pos: domain.Vec2{X: 0, Y: 0}},
			}},
		},
		sector.WithHandoff(topo, jumpBus),
		sector.WithRouter(pathRouter),
	)

	w.Tick(context.Background())

	snap := w.Snapshot(1)
	require.Len(t, snap.Ships, 1, "ship must stay in sector 1 — too far from gate")
	require.NotNil(t, snap.Ships[0].Target)
	assert.Equal(t, domain.Vec2{X: 100, Y: 0}, *snap.Ships[0].Target,
		"autopilot still points the ship at the gate")
}

func TestUnit_SetCourseCommand_ArmsAutopilotAndClearsTarget(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 7, SectorID: 1,
			Target: &domain.Vec2{X: 999, Y: 999}, // leftover from MoveCommand
		}}},
	)

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(1, sector.SetCourseCommand{
		PlayerID: 7, ShipID: 1,
		Course: &domain.Course{Sector: 2, Pos: domain.Vec2{X: 50, Y: 50}},
		Reply:  reply,
	}))
	w.Tick(context.Background())
	require.NoError(t, (<-reply).Err)

	snap := w.Snapshot(1)
	require.Len(t, snap.Ships, 1)
	require.NotNil(t, snap.Ships[0].FinalTarget)
	assert.Equal(t, domain.SectorID(2), snap.Ships[0].FinalTarget.Sector)
	assert.Equal(t, domain.Vec2{X: 50, Y: 50}, snap.Ships[0].FinalTarget.Pos)
	assert.Nil(t, snap.Ships[0].Target, "previous Target must be cleared")
}

func TestUnit_SetCourseCommand_Forbidden(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{ID: 1, PlayerID: 7, SectorID: 1}}},
	)

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(1, sector.SetCourseCommand{
		PlayerID: 999, ShipID: 1,
		Course: &domain.Course{Sector: 1, Pos: domain.Vec2{}},
		Reply:  reply,
	}))
	w.Tick(context.Background())
	assert.ErrorIs(t, (<-reply).Err, sector.ErrForbidden)
}

func TestUnit_SetCourseCommand_NilCourse_ClearsAutopilot(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 7, SectorID: 1,
			FinalTarget: &domain.Course{Sector: 5, Pos: domain.Vec2{X: 1, Y: 2}},
		}}},
	)

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(1, sector.SetCourseCommand{
		PlayerID: 7, ShipID: 1, Course: nil, Reply: reply,
	}))
	w.Tick(context.Background())
	require.NoError(t, (<-reply).Err)

	assert.Nil(t, w.Snapshot(1).Ships[0].FinalTarget)
}
