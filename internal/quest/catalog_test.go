package quest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// the 13 X-Tension microquests (phase 8.18: slice + second increment).
var microquestIDs = []string{
	"xt_6008000", "xt_6002100", "xt_6002300", // slice
	"xt_6000200", "xt_6000300", "xt_6000400", "xt_6002500", "xt_6004900",
	"xt_6006200", "xt_6006300", "xt_6008500", "xt_6009000", "xt_6009700",
}

func TestUnit_QuestCatalog_All13Present(t *testing.T) {
	t.Parallel()
	require.Len(t, microquestIDs, 13)
	for _, id := range microquestIDs {
		d, ok := quest.Lookup(id)
		require.True(t, ok, "quest %s missing from registry", id)
		assert.True(t, d.Offerable, "quest %s must be offerable", id)
	}
}

// TestUnit_QuestCatalog_WellFormed pins catalogue invariants for every
// offerable quest: ids resolve, prerequisites exist, a TargetRole step has a
// matching spawn role, and deliver/trade steps name a goods type.
func TestUnit_QuestCatalog_WellFormed(t *testing.T) {
	t.Parallel()
	offerable := quest.Offerable()
	require.NotEmpty(t, offerable)
	for _, d := range offerable {
		require.NotEmpty(t, d.ID)
		_, ok := quest.Lookup(d.ID)
		require.True(t, ok, "offerable quest %s not in registry", d.ID)

		if d.Prerequisite != "" {
			_, ok := quest.Lookup(d.Prerequisite)
			assert.True(t, ok, "quest %s prerequisite %s unknown", d.ID, d.Prerequisite)
		}

		roles := map[string]bool{}
		for _, sp := range d.Spawns {
			assert.NotEmpty(t, sp.Role, "quest %s spawn role empty", d.ID)
			assert.Positive(t, sp.Count, "quest %s spawn %s count", d.ID, sp.Role)
			roles[sp.Role] = true
		}
		require.NotEmpty(t, d.Steps, "quest %s has no steps", d.ID)
		for i, st := range d.Steps {
			if st.TargetRole != "" {
				assert.True(t, roles[st.TargetRole],
					"quest %s step %d targets role %q with no matching spawn", d.ID, i, st.TargetRole)
			}
			if st.Kind == quest.StepDeliver || st.Kind == quest.StepTrade {
				assert.NotZero(t, st.Goods, "quest %s step %d (%s) needs a goods type", d.ID, i, st.Kind)
			}
		}
	}
}

func TestUnit_Quest_SickPrincess_DeliverWithinDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newSpawnService(store, &fakeSpawner{})

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6000300"))
	// acquire step: have 13 units in the hold (polled).
	store.snap[questPlayer] = quest.Snapshot{CargoUnits: 13}
	require.NoError(t, svc.ProcessAll(ctx, 100))
	// deliver step: deliver 13 Space Fuel (goods 40).
	require.NoError(t, svc.OnEvent(ctx, quest.Event{
		Player: questPlayer, Kind: quest.EventDeliver, Goods: 40, Amount: 13,
	}))
	require.Equal(t, domain.QuestCompleted, store.status(questPlayer, "xt_6000300"))
	assert.Equal(t, int64(25000), store.cash[questPlayer])
}

func TestUnit_Quest_SabotageChain_Part2BlockedUntilPart1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newSpawnService(store, &fakeSpawner{})

	// Part 2 cannot be accepted before part 1 completes.
	require.ErrorIs(t, svc.Accept(ctx, questPlayer, "xt_6006300"), quest.ErrPrerequisiteNotMet)

	// Complete part 1: goto #1 + dock (polled) then plant the device (deliver).
	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6006200"))
	store.snap[questPlayer] = quest.Snapshot{CurrentSector: 1, Docked: true}
	require.NoError(t, svc.ProcessAll(ctx, 100))
	require.NoError(t, svc.OnEvent(ctx, quest.Event{
		Player: questPlayer, Kind: quest.EventDeliver, Goods: 5, Amount: 1,
	}))
	require.Equal(t, domain.QuestCompleted, store.status(questPlayer, "xt_6006200"))

	// Now part 2 accepts.
	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6006300"))
	require.Equal(t, domain.QuestActive, store.status(questPlayer, "xt_6006300"))
}

func TestUnit_Quest_ComplexTrade_BuyThenSell(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, _ := newSpawnService(store, &fakeSpawner{})

	require.NoError(t, svc.Accept(ctx, questPlayer, "xt_6009700"))
	// step 0: buy 5 Crystals (goods 4).
	require.NoError(t, svc.OnEvent(ctx, quest.Event{
		Player: questPlayer, Kind: quest.EventTrade, Side: "buy", Goods: 4, Amount: 5,
	}))
	// step 1: reach sector 5 (polled).
	store.snap[questPlayer] = quest.Snapshot{CurrentSector: 5}
	require.NoError(t, svc.ProcessAll(ctx, 100))
	// a sell before arriving wouldn't have counted; now sell 5 Crystals.
	require.NoError(t, svc.OnEvent(ctx, quest.Event{
		Player: questPlayer, Kind: quest.EventTrade, Side: "sell", Goods: 4, Amount: 5,
	}))
	require.Equal(t, domain.QuestCompleted, store.status(questPlayer, "xt_6009700"))
	assert.Equal(t, int64(6000), store.cash[questPlayer])
}
