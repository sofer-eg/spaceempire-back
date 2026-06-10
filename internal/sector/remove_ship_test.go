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

// TestUnit_Worker_RemoveShipCommand_DespawnsAndDeletes verifies the quest-NPC
// despawn path (phase 8.18): RemoveShipCommand drops the ship (and its AI
// controller) from RAM and deletes the DB row.
func TestUnit_Worker_RemoveShipCommand_DespawnsAndDeletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repo := &fakeShipRepo{}
	registry := ai.NewRegistry()
	race.Register(registry, alwaysHostile{}, race.Config{})
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {}},
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: nil}),
	)

	// Spawn an AI-controlled NPC, then despawn it.
	state, err := race.NewInitialState(7, domain.Vec2{})
	require.NoError(t, err)
	addReply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AddShipCommand{
		Ship: domain.Ship{ID: 5, Race: 7, HP: 10, MaxHP: 10}, ControllerKind: race.Kind, StateJSON: state, Reply: addReply,
	}))
	w.Tick(ctx)
	require.NoError(t, (<-addReply).Err)
	_, ok := snapshotShipByID(w.Snapshot(testSector), 5)
	require.True(t, ok, "NPC present after spawn")

	rmReply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.RemoveShipCommand{ShipID: 5, Reply: rmReply}))
	w.Tick(ctx)
	require.NoError(t, (<-rmReply).Err)

	_, ok = snapshotShipByID(w.Snapshot(testSector), 5)
	require.False(t, ok, "NPC despawned from RAM")
	require.Contains(t, repo.deletes, domain.ShipID(5), "DB row deleted")

	// Despawning an absent ship is an idempotent no-op.
	rmReply2 := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.RemoveShipCommand{ShipID: 5, Reply: rmReply2}))
	w.Tick(ctx)
	require.NoError(t, (<-rmReply2).Err)
}
