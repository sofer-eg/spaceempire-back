package quest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/quest"
)

// procDef is a deliver-shaped procedural definition (ID left as the template
// kind, as the generator emits it — accept stamps the real proc:<id>).
func procDef() quest.Def {
	return quest.Def{
		ID:    "deliver",
		Title: "Доставка груза",
		Steps: []quest.Step{
			{Kind: quest.StepAcquireCargo, Qty: 7, Goods: 40, Desc: "Купи 7 Космотоплива"},
			{Kind: quest.StepDeliver, Goods: 40, Count: 7, RewardCash: 3500,
				Target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 2}, Desc: "Доставь 7 Космотоплива"},
		},
	}
}

// TestUnit_Quest_AcceptProcOffer materialises a procedural offer: the active
// player_quests row carries the frozen definition with its id stamped to the
// accept handle (proc:<id>), the offer is consumed, and steps/reward project
// exactly like a static quest.
func TestUnit_Quest_AcceptProcOffer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, clk := newService(store)

	def := procDef()
	def.Deadline = 24 * time.Hour
	id := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "deliver", Definition: mustDefJSON(t, def),
		Source: "dock", ExpiresAt: clk.Now().Add(time.Hour),
	})
	questID := fmt.Sprintf("proc:%d", id)

	require.NoError(t, svc.Accept(ctx, 10, questID))

	// Offer consumed.
	_, ok, err := store.GetOffer(ctx, id, 10)
	require.NoError(t, err)
	assert.False(t, ok, "offer deleted on accept")

	// player_quests row exists with a definition whose ID is the accept handle.
	prog, ok, err := store.Get(ctx, 10, questID)
	require.NoError(t, err)
	require.True(t, ok, "active proc quest materialised")
	require.NotNil(t, prog.Definition)
	var stored quest.Def
	require.NoError(t, json.Unmarshal(prog.Definition, &stored))
	assert.Equal(t, questID, stored.ID, "definition ID stamped to proc:<id> (no PK collision, W3)")
	assert.Equal(t, clk.Now().Add(24*time.Hour), prog.DeadlineAt, "deadline from def.Deadline")

	// Steps/reward project from the frozen definition, same as a static quest.
	views, err := svc.ActiveList(ctx, 10)
	require.NoError(t, err)
	var v *quest.ActiveView
	for _, x := range views {
		if x.QuestID == questID {
			v = x
		}
	}
	require.NotNil(t, v)
	assert.Equal(t, "Доставка груза", v.Title)
	assert.Equal(t, 2, v.TotalSteps)
}

// TestUnit_Quest_AcceptStoryOffer accepts a story offer (nil definition): the
// active quest is materialised under the static template id (resolves via
// Lookup thereafter, definition NULL) and the offer is consumed.
func TestUnit_Quest_AcceptStoryOffer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, clk := newService(store)

	id := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "patrol", Definition: nil,
		Source: "space", ExpiresAt: clk.Now().Add(time.Hour),
	})

	require.NoError(t, svc.Accept(ctx, 10, fmt.Sprintf("proc:%d", id)))

	_, ok, _ := store.GetOffer(ctx, id, 10)
	assert.False(t, ok, "offer consumed")

	prog, ok, err := store.Get(ctx, 10, "patrol")
	require.NoError(t, err)
	require.True(t, ok, "story quest materialised under its static id")
	assert.Equal(t, domain.QuestActive, prog.Status)
	assert.Nil(t, prog.Definition, "story quest resolves via Lookup, no frozen definition")
}

// TestUnit_Quest_AcceptStoryOffer_Spawns — a story offer whose static def spawns
// NPCs (KillFugitive) triggers the spawn on accept, like a direct static accept.
func TestUnit_Quest_AcceptStoryOffer_Spawns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	sp := &fakeSpawner{}
	svc, clk := newSpawnService(store, sp)

	id := store.putOffer(domain.QuestOffer{
		Player: questPlayer, TemplateID: "xt_6008000", Definition: nil,
		Source: "dock", ExpiresAt: clk.Now().Add(time.Hour),
	})

	require.NoError(t, svc.Accept(ctx, questPlayer, fmt.Sprintf("proc:%d", id)))
	assert.Equal(t, domain.QuestActive, store.status(questPlayer, "xt_6008000"))
	assert.NotEmpty(t, sp.spawns, "story offer with spawns spawns on accept")
}

// TestUnit_Quest_AcceptOffer_ForeignAndMissing — accepting a nonexistent offer,
// another player's offer, or a malformed proc id all map to ErrNotOfferable
// (404), never creating a quest (STRIDE spoofing).
func TestUnit_Quest_AcceptOffer_ForeignAndMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, clk := newService(store)

	// owner's offer
	id := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "deliver", Definition: mustDefJSON(t, procDef()),
		Source: "dock", ExpiresAt: clk.Now().Add(time.Hour),
	})

	// nonexistent
	require.ErrorIs(t, svc.Accept(ctx, 10, "proc:99999"), quest.ErrNotOfferable)
	// foreign player
	require.ErrorIs(t, svc.Accept(ctx, 11, fmt.Sprintf("proc:%d", id)), quest.ErrNotOfferable)
	// malformed proc id
	require.ErrorIs(t, svc.Accept(ctx, 10, "proc:notanumber"), quest.ErrNotOfferable)
	// still present, untouched
	_, ok, _ := store.GetOffer(ctx, id, 10)
	assert.True(t, ok, "failed accepts do not consume the offer")
}

// TestUnit_Quest_AcceptOffer_Expired — an offer past its TTL is 404, offer left
// for the Closer to sweep.
func TestUnit_Quest_AcceptOffer_Expired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, clk := newService(store)

	id := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "deliver", Definition: mustDefJSON(t, procDef()),
		Source: "dock", ExpiresAt: clk.Now().Add(-time.Minute), // already expired
	})

	require.ErrorIs(t, svc.Accept(ctx, 10, fmt.Sprintf("proc:%d", id)), quest.ErrNotOfferable)
	_, ok, _ := store.Get(ctx, 10, fmt.Sprintf("proc:%d", id))
	assert.False(t, ok, "expired offer never materialises a quest")
}

// TestUnit_Quest_AcceptOffer_Idempotent — a second accept of the same offer is
// 404 (the offer is gone), so re-accept is a no-op by effect.
func TestUnit_Quest_AcceptOffer_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, clk := newService(store)

	id := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "deliver", Definition: mustDefJSON(t, procDef()),
		Source: "dock", ExpiresAt: clk.Now().Add(time.Hour),
	})
	questID := fmt.Sprintf("proc:%d", id)

	require.NoError(t, svc.Accept(ctx, 10, questID))
	require.ErrorIs(t, svc.Accept(ctx, 10, questID), quest.ErrNotOfferable)
}

// TestUnit_Quest_OfferableList_Personal — offerable returns only the player's
// live offers (proc + story), projected with reward/desc; empty without offers
// (AC-8), and scoped per player.
func TestUnit_Quest_OfferableList_Personal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	svc, clk := newService(store)

	// AC-8: no offers → empty list, no static catalogue.
	list, err := svc.OfferableList(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, list)

	procID := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "deliver", Definition: mustDefJSON(t, procDef()),
		Source: "dock", ExpiresAt: clk.Now().Add(time.Hour),
	})
	storyID := store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "patrol", Definition: nil,
		Source: "space", ExpiresAt: clk.Now().Add(2 * time.Hour),
	})
	// expired one is skipped
	store.putOffer(domain.QuestOffer{
		Player: 10, TemplateID: "courier", Definition: nil,
		Source: "dock", ExpiresAt: clk.Now().Add(-time.Hour),
	})
	// another player's offer is invisible
	store.putOffer(domain.QuestOffer{
		Player: 11, TemplateID: "patrol", ExpiresAt: clk.Now().Add(time.Hour),
	})

	list, err = svc.OfferableList(ctx, 10)
	require.NoError(t, err)
	require.Len(t, list, 2)

	byID := map[string]quest.OfferView{}
	for _, v := range list {
		byID[v.OfferID] = v
	}
	proc := byID[fmt.Sprintf("proc:%d", procID)]
	assert.Equal(t, "Доставка груза", proc.Title)
	assert.Equal(t, "dock", proc.Source)
	assert.Equal(t, int64(3500), proc.RewardCash, "reward = sum of step rewards")
	assert.Equal(t, "Купи 7 Космотоплива", proc.Desc, "desc = first step desc")

	story := byID[fmt.Sprintf("proc:%d", storyID)]
	assert.Equal(t, quest.Patrol.Title, story.Title, "story offer resolves via Lookup(template_id)")
	assert.Equal(t, "space", story.Source)
}

// countingHandler counts Error-level records carrying a given message.
type countingHandler struct {
	msg string
	n   *atomic.Int64
}

func (h countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelError && r.Message == h.msg {
		h.n.Add(1)
	}
	return nil
}
func (h countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingHandler) WithGroup(string) slog.Handler      { return h }

// TestUnit_Quest_ResolveDef_LogsBrokenOnce — a single zombie proc row with a
// malformed definition logs the resolve error exactly once even across many
// engine passes (OnShipDestroyed iterates every active quest per ship death),
// so one bad row cannot spam the log (MR2 review).
func TestUnit_Quest_ResolveDef_LogsBrokenOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore()
	var n atomic.Int64
	logger := slog.New(countingHandler{msg: "quest resolve definition", n: &n})
	svc := quest.New(store, runner{store: store}, store, nil, clock.NewFakeClock(epoch), logger)

	store.put(domain.QuestProgress{
		Player: 10, QuestID: "proc:bad", Status: domain.QuestActive,
		Definition: []byte("{not json"),
	})

	for i := 0; i < 5; i++ {
		require.NoError(t, svc.OnShipDestroyed(ctx, shipRef(int64(i+1))))
	}
	assert.Equal(t, int64(1), n.Load(), "broken definition logged once, not per pass")
}
