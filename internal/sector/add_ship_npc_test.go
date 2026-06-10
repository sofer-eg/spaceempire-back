package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_Worker_AddShipCommand_SpawnsControlledNPC verifies the runtime NPC
// spawn path (phase 9.5): AddShipCommand with a ControllerKind hydrates an AI
// controller so the freshly-injected ship acts on the same tick — here a
// race-7 (Xenon) warship engages an in-range hostile.
func TestUnit_Worker_AddShipCommand_SpawnsControlledNPC(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	registry := ai.NewRegistry()
	race.Register(registry, alwaysHostile{}, race.Config{DetectionRange: 600, PatrolRadius: 150})

	// A pre-existing target the spawned invader will engage.
	target := domain.Ship{ID: 1, PlayerID: 100, Pos: domain.Vec2{X: 50, Y: 0}, HP: 50, MaxHP: 50}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {target}},
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: nil}),
	)

	state, err := race.NewInitialState(7, domain.Vec2{})
	require.NoError(t, err)
	npc := domain.Ship{
		ID: 2, Race: 7, Pos: domain.Vec2{X: 0, Y: 0}, HP: 60, MaxHP: 60,
		MaxSpeed: 20, Acceleration: 10, TurnRate: 1, Direction: domain.Vec2{X: 1, Y: 0},
		LaserDamage: 5, LaserRange: 400, Energy: 100, MaxEnergy: 100,
	}
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AddShipCommand{
		Ship: npc, ControllerKind: race.Kind, StateJSON: state, Reply: reply,
	}))

	w.Tick(ctx)
	require.NoError(t, (<-reply).Err)

	// The injected NPC got a controller and engaged the hostile this tick.
	snap := w.Snapshot(testSector)
	got, ok := snapshotShipByID(snap, 2)
	require.True(t, ok, "spawned NPC present in the sector")
	require.NotNil(t, got.AttackTarget, "controller engaged a hostile")
	require.Equal(t, int64(1), got.AttackTarget.ID)
}
