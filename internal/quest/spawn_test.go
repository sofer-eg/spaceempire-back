package quest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/quest"
)

// fakeSpawner records Spawn/Despawn calls and hands out sequential ship ids.
type fakeSpawner struct {
	next      int64
	spawns    []quest.QuestSpawn
	despawned [][]domain.ShipID
}

func (f *fakeSpawner) Spawn(_ context.Context, spec quest.QuestSpawn) ([]domain.ShipID, error) {
	f.spawns = append(f.spawns, spec)
	ids := make([]domain.ShipID, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		f.next++
		ids = append(ids, domain.ShipID(f.next))
	}
	return ids, nil
}

func (f *fakeSpawner) Despawn(_ context.Context, ids []domain.ShipID) {
	f.despawned = append(f.despawned, ids)
}

func newSpawnService(store *memStore, sp quest.Spawner) (*quest.Service, *clock.FakeClock) {
	clk := clock.NewFakeClock(epoch)
	return quest.New(store, runner{store: store}, sp, clk, nil), clk
}

func shipRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindShip, ID: id}
}

func roles(spawns []quest.QuestSpawn) []string {
	out := make([]string, len(spawns))
	for i, s := range spawns {
		out[i] = s.Role
	}
	return out
}

const questPlayer = domain.PlayerID(7)

func TestUnit_Quest_KillFugitive_SpawnsTracksCompletesDespawns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	sp := &fakeSpawner{}
	svc, _ := newSpawnService(store, sp)

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6008000"))
	require.Equal(t, []string{"target"}, roles(sp.spawns), "spawns the target on accept")

	// Re-accept is idempotent — no second spawn.
	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6008000"))
	require.Len(t, sp.spawns, 1, "re-accept does not respawn")

	// Advance the goto_sector{1} step by polling with the player in sector 1.
	store.snap[questPlayer] = quest.Snapshot{CurrentSector: 1}
	require.NoError(t, svc.ProcessAll(ctx, 100))

	// Killing an unrelated ship does not advance the target-bound kill.
	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(999)))
	require.Equal(t, domain.QuestActive, store.status(questPlayer, "xt_6008000"))
	assert.Empty(t, sp.despawned)

	// Killing the spawned target (id 1) completes the quest, pays once, despawns.
	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(1)))
	require.Equal(t, domain.QuestCompleted, store.status(questPlayer, "xt_6008000"))
	assert.Equal(t, int64(5000), store.cash[questPlayer])
	require.Equal(t, [][]domain.ShipID{{1}}, sp.despawned)
}

func TestUnit_Quest_Siege_FromGateSpecAndKillAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	sp := &fakeSpawner{}
	svc, _ := newSpawnService(store, sp)

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6002100"))
	require.Len(t, sp.spawns, 1)
	spec := sp.spawns[0]
	assert.Equal(t, "enemy", spec.Role)
	assert.Equal(t, domain.RaceID(7), spec.Race)
	assert.Equal(t, 3, spec.Count)
	assert.True(t, spec.FromGate, "Xenon breaks through a gate")

	store.snap[questPlayer] = quest.Snapshot{CurrentSector: 1}
	require.NoError(t, svc.ProcessAll(ctx, 100)) // clear goto_sector

	// Kill 2 of 3 — still active. Even an NPC-stolen kill counts (victim-scoped).
	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(1)))
	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(2)))
	require.Equal(t, domain.QuestActive, store.status(questPlayer, "xt_6002100"))

	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(3)))
	require.Equal(t, domain.QuestCompleted, store.status(questPlayer, "xt_6002100"))
	assert.Equal(t, int64(30000), store.cash[questPlayer])
	require.Len(t, sp.despawned, 1)
}

func TestUnit_Quest_Escort_SurvivesThenCompletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	sp := &fakeSpawner{}
	svc, _ := newSpawnService(store, sp)

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6002300"))
	require.ElementsMatch(t, []string{"escortee", "enemy"}, roles(sp.spawns))

	// 12 survival ticks (the step's Count) → completes.
	for i := 0; i < 12; i++ {
		require.NoError(t, svc.ProcessAll(ctx, 100))
	}
	require.Equal(t, domain.QuestCompleted, store.status(questPlayer, "xt_6002300"))
	assert.Equal(t, int64(5000), store.cash[questPlayer])
	require.Len(t, sp.despawned, 1)
	// escortee (1) + 2 enemies (2,3) all despawned on completion.
	assert.ElementsMatch(t, []domain.ShipID{1, 2, 3}, sp.despawned[0])
}

func TestUnit_Quest_Escort_FailsWhenEscorteeKilled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	sp := &fakeSpawner{}
	svc, _ := newSpawnService(store, sp)

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6002300")) // escortee=1, enemies=2,3

	// An enemy dying does not fail the escort.
	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(2)))
	require.Equal(t, domain.QuestActive, store.status(questPlayer, "xt_6002300"))

	// The escortee (1) dying fails the quest — no reward, NPCs despawned.
	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(1)))
	require.Equal(t, domain.QuestFailed, store.status(questPlayer, "xt_6002300"))
	assert.Zero(t, store.cash[questPlayer])
	require.Len(t, sp.despawned, 1)
}

func TestUnit_Quest_Abandon_Despawns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	sp := &fakeSpawner{}
	svc, _ := newSpawnService(store, sp)

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6008000"))
	require.NoError(t, svc.Abandon(ctx, questPlayer, "xt_6008000"))
	require.Equal(t, domain.QuestAbandoned, store.status(questPlayer, "xt_6008000"))
	require.Equal(t, [][]domain.ShipID{{1}}, sp.despawned)
}
