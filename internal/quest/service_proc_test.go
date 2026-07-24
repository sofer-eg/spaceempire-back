package quest_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// mustDefJSON marshals a Def to the JSONB bytes a procedural instance persists
// in player_quests.definition.
func mustDefJSON(t *testing.T, def quest.Def) []byte {
	t.Helper()
	b, err := json.Marshal(def)
	require.NoError(t, err)
	return b
}

// TestUnit_Quest_DefJSONRoundTrip proves a Def survives a JSONB round-trip
// equal, so a frozen procedural instance (proc:*) resolves back to exactly the
// definition that generated it: time.Duration deadlines, typed ids, EntityRef
// targets, and spawns are all preserved.
func TestUnit_Quest_DefJSONRoundTrip(t *testing.T) {
	t.Parallel()
	for _, def := range []quest.Def{
		quest.Patrol,         // Deadline (time.Duration)
		quest.Saga2,          // Sector + Prerequisite
		quest.Siege,          // Spawns + FromGate
		quest.DeliverPackage, // multi-step, Goods, spawns in another sector
		quest.ComplexTrade,   // trade Side
	} {
		b, err := json.Marshal(def)
		require.NoError(t, err)
		var got quest.Def
		require.NoError(t, json.Unmarshal(b, &got))
		assert.Equal(t, def, got, "def %s round-trips byte-equal", def.ID)
	}
}

// TestUnit_Quest_ProcInstance_ViewProjectsFromDefinition — a proc:* row whose
// quest id is absent from the static registry projects into the API view purely
// from its persisted definition (title, steps, reward, event-step goal).
func TestUnit_Quest_ProcInstance_ViewProjectsFromDefinition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newService(store)

	def := quest.Def{
		ID:    "proc:42",
		Title: "Доставка топлива",
		Steps: []quest.Step{
			{Kind: quest.StepDeliver, Goods: 40, Count: 5, RewardCash: 3000, Desc: "Доставь 5 ед. Космотоплива"},
		},
	}
	store.put(domain.QuestProgress{
		Player: 20, QuestID: def.ID, Status: domain.QuestActive,
		Definition: mustDefJSON(t, def),
	})

	views, err := svc.ActiveList(ctx, 20)
	require.NoError(t, err)

	var v *quest.ActiveView
	for _, x := range views {
		if x.QuestID == def.ID {
			v = x
		}
	}
	require.NotNil(t, v, "procedural instance projected from its definition")
	assert.Equal(t, "Доставка топлива", v.Title)
	assert.Equal(t, 1, v.TotalSteps)
	assert.Equal(t, "Доставь 5 ед. Космотоплива", v.StepDesc)
	assert.Equal(t, int64(3000), v.StepReward)
	assert.Equal(t, int64(5), v.StepGoal, "deliver goal comes from the definition Count")
}

// TestUnit_Quest_StaticInstance_ResolvesViaLookup — a row with a nil Definition
// still resolves through the static code registry (regression: static quests
// keep working unchanged).
func TestUnit_Quest_StaticInstance_ResolvesViaLookup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newService(store)
	require.NoError(t, store.Ensure(ctx, 20, "patrol", nil))

	views, err := svc.ActiveList(ctx, 20)
	require.NoError(t, err)
	var v *quest.ActiveView
	for _, x := range views {
		if x.QuestID == "patrol" {
			v = x
		}
	}
	require.NotNil(t, v, "static quest with nil Definition resolves via Lookup")
	assert.Equal(t, quest.Patrol.Title, v.Title)
}

// TestUnit_Quest_ProcInstance_BrokenDefinitionDegrades — a malformed definition
// resolves to not-found, so the engine drops the quest from the view exactly
// like an unknown id (soft degradation, not a crash).
func TestUnit_Quest_ProcInstance_BrokenDefinitionDegrades(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newService(store)

	store.put(domain.QuestProgress{
		Player: 20, QuestID: "proc:bad", Status: domain.QuestActive,
		Definition: []byte("{not valid json"),
	})

	views, err := svc.ActiveList(ctx, 20)
	require.NoError(t, err)
	for _, v := range views {
		assert.NotEqual(t, "proc:bad", v.QuestID, "malformed definition is dropped like an unknown id")
	}
}

// TestUnit_Quest_ProcInstance_OnEventAdvancesAndRewards drives a procedural kill
// instance (Count 2) via two kill events: the first bumps the counter, the
// second completes it and grants the reward exactly once — the definition-driven
// counterpart of the static patrol test.
func TestUnit_Quest_ProcInstance_OnEventAdvancesAndRewards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newService(store)

	def := quest.Def{
		ID: "proc:kill", Title: "Процедурный патруль",
		Steps: []quest.Step{
			{Kind: quest.StepKill, Count: 2, RewardCash: 4000, Desc: "Уничтожь 2 корабля"},
		},
	}
	store.put(domain.QuestProgress{
		Player: 10, QuestID: def.ID, Status: domain.QuestActive,
		Definition: mustDefJSON(t, def),
	})

	kill := quest.Event{Player: 10, Kind: quest.EventKill, Amount: 1}
	require.NoError(t, svc.OnEvent(ctx, kill))
	assert.Equal(t, domain.QuestActive, store.status(10, def.ID), "1/2 kills — still active")
	assert.Equal(t, int64(0), store.cash[10])

	require.NoError(t, svc.OnEvent(ctx, kill))
	assert.Equal(t, domain.QuestCompleted, store.status(10, def.ID))
	assert.Equal(t, int64(4000), store.cash[10], "reward once on completion")
}

// TestUnit_Quest_ProcInstance_OnShipDestroyedAdvances drives a procedural
// target-bound kill instance via a ship death: an unrelated victim is ignored,
// the bound target completes the quest and pays.
func TestUnit_Quest_ProcInstance_OnShipDestroyedAdvances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newService(store)

	def := quest.Def{
		ID: "proc:hunt", Title: "Процедурная охота",
		Steps: []quest.Step{
			{Kind: quest.StepKill, TargetRole: "target", RewardCash: 5000, Desc: "Уничтожь цель"},
		},
	}
	store.put(domain.QuestProgress{
		Player: 10, QuestID: def.ID, Status: domain.QuestActive,
		State:      []byte(`{"spawned":{"target":[1]}}`),
		Definition: mustDefJSON(t, def),
	})

	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(999)))
	assert.Equal(t, domain.QuestActive, store.status(10, def.ID), "unrelated victim does not advance")

	require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(1)))
	assert.Equal(t, domain.QuestCompleted, store.status(10, def.ID))
	assert.Equal(t, int64(5000), store.cash[10], "bound target kill completes and pays")
}
