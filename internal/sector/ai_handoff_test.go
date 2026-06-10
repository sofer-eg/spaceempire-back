package sector_test

import (
	"context"
	"encoding/json"
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

// traderRegistry builds a registry with the "trader" controller registered,
// matching production wiring (phase 5.3).
func traderRegistry() *ai.Registry {
	reg := ai.NewRegistry()
	trader.Register(reg, trader.Config{ArriveRadius: 6})
	return reg
}

// TestUnit_Handoff_OutboundCarriesController is the phase 5.3 AI-controller
// handoff (deferred from 5.1): when an AI-driven ship jumps a gate, the
// outbound JumpEvent carries the controller's kind + state, and its ai_state
// row is re-homed to the target sector.
func TestUnit_Handoff_OutboundCarriesController(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	busInstance := &fakeBus{}
	aiRepo := &fakeAIStateRepo{}

	state, err := trader.NewInitialState(
		trader.Leg{Sector: 1, Pos: domain.Vec2{X: 100, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}},
		trader.Leg{Sector: 2, Pos: domain.Vec2{X: -100, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 7}},
		42, 20,
	)
	require.NoError(t, err)

	// NPC ship (PlayerID 0) parked at the gate exit, ready to jump 1→2.
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		repo, nil,
		map[domain.SectorID][]domain.Ship{1: {{ID: 1, PlayerID: 0, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}, MaxSpeed: 10}}},
		sector.WithHandoff(handoffTopology(), busInstance),
		sector.WithAI(traderRegistry(), aiRepo, map[domain.SectorID][]domain.AIState{1: {
			{ShipID: 1, SectorID: 1, ControllerKind: trader.Kind, StateJSON: state},
		}}),
	)

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 0, ShipID: 1, GateID: 10})
	require.NoError(t, res.Err)
	assert.Empty(t, w.Snapshot(1).Ships, "ship must leave the source sector")

	// The intake event carries the controller so the destination can rebuild it.
	msgs := busInstance.snapshot()
	require.NotEmpty(t, msgs)
	require.Equal(t, "sector.2.intake", msgs[0].topic)
	var ev sector.JumpEvent
	require.NoError(t, json.Unmarshal(msgs[0].payload, &ev))
	assert.Equal(t, trader.Kind, ev.ControllerKind, "JumpEvent must carry the controller kind")
	assert.NotEmpty(t, ev.ControllerState, "JumpEvent must carry the controller state")

	// The ai_state row was re-homed to the target sector.
	saved, ok := aiRepo.snapshot(1)
	require.True(t, ok, "ai_state must be re-homed on jump")
	assert.Equal(t, domain.SectorID(2), saved.SectorID)
	assert.Equal(t, trader.Kind, saved.ControllerKind)
}

// TestUnit_Handoff_IntakeRebuildsController proves the inbound side: a
// JumpEvent carrying a controller makes the destination worker rebuild it and
// resume driving the ship. The rebuilt trader (phase dest, destination in a
// far sector) immediately sets a course toward that destination.
func TestUnit_Handoff_IntakeRebuildsController(t *testing.T) {
	t.Parallel()

	// Controller state mid-route: phase dest, destination in sector 5.
	a := traderRegistry()
	built, err := a.Build(trader.Kind, mustTraderState(t,
		trader.Leg{Sector: 1, Pos: domain.Vec2{X: 100, Y: 0}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}},
		trader.Leg{Sector: 5, Pos: domain.Vec2{X: 180, Y: 50}, Ref: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 9}},
	))
	require.NoError(t, err)
	tc, ok := built.(*trader.Controller)
	require.True(t, ok)
	// Advance past the home-load so the controller is in the dest phase.
	tc.Tick(context.Background(), destPhaseWorld{})
	require.Equal(t, trader.PhaseDest, tc.CurrentPhase())
	ctrlState, err := tc.MarshalState()
	require.NoError(t, err)

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{2: {}},
		sector.WithHandoff(handoffTopology(), &fakeBus{}),
		sector.WithAI(traderRegistry(), nil, nil),
	)

	ship := domain.Ship{ID: 1, PlayerID: 0, SectorID: 2, Pos: domain.Vec2{X: -100, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0}, HP: 100}
	require.NoError(t, w.Send(2, sector.JumpIntakeCommand{Event: sector.JumpEvent{
		Ship: ship, SourceSector: 1, TargetSector: 2, ExitPos: ship.Pos,
		ControllerKind: trader.Kind, ControllerState: ctrlState,
	}}))
	// One tick: the intake adds the ship + rebuilds the controller, then tickAI
	// runs it. With no router wired, SetCourse's FinalTarget survives untouched.
	w.Tick(context.Background())

	snap := w.Snapshot(2)
	got, ok := snapshotShipByID(snap, 1)
	require.True(t, ok, "ship must be present in the destination sector")
	require.NotNil(t, got.FinalTarget, "rebuilt controller must have set a course")
	assert.Equal(t, domain.SectorID(5), got.FinalTarget.Sector,
		"rebuilt trader keeps heading to its destination sector")
}

// destPhaseWorld is a WorldView that reports a ship parked at the home leg
// (sector 1, pos 100,0) so the trader's first tick loads and advances to the
// dest phase.
type destPhaseWorld struct{}

func (destPhaseWorld) Self() domain.Ship {
	return domain.Ship{ID: 1, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}}
}
func (destPhaseWorld) Ships() []domain.Ship          { return nil }
func (destPhaseWorld) Statics() domain.SectorStatics { return domain.SectorStatics{} }
func (destPhaseWorld) Asteroids() []domain.Asteroid  { return nil }

func mustTraderState(t *testing.T, home, dest trader.Leg) []byte {
	t.Helper()
	b, err := trader.NewInitialState(home, dest, 42, 20)
	require.NoError(t, err)
	return b
}
