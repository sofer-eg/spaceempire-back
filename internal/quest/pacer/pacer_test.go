package pacer_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/quest"
	"spaceempire/back/internal/quest/pacer"
)

// --- fakes ------------------------------------------------------------------

// fakeCounters is a stateful CounterStore: Get returns the last Upsert (or a
// missing row until the first write), so a test can drive many triggers.
type fakeCounters struct {
	stored domain.QuestCounters
	hasRow bool
}

func (f *fakeCounters) GetCounters(_ context.Context, _ domain.PlayerID) (domain.QuestCounters, bool, error) {
	if !f.hasRow {
		return domain.QuestCounters{}, false, nil
	}
	return f.stored, true, nil
}

func (f *fakeCounters) UpsertCounters(_ context.Context, c domain.QuestCounters) error {
	f.stored = c
	f.hasRow = true
	return nil
}

type fakeOffers struct {
	active   int
	nextID   int64
	inserted []domain.QuestOffer
}

func (f *fakeOffers) InsertOffer(_ context.Context, o domain.QuestOffer) (int64, error) {
	f.nextID++
	f.inserted = append(f.inserted, o)
	return f.nextID, nil
}

func (f *fakeOffers) CountActiveOffers(_ context.Context, _ domain.PlayerID, _ time.Time) (int, error) {
	return f.active, nil
}

type fakeGenerator struct {
	kind  string
	def   quest.Def
	ok    bool
	err   error
	calls int
}

func (f *fakeGenerator) Generate(_ context.Context, _ domain.SectorID) (string, quest.Def, bool, error) {
	f.calls++
	return f.kind, f.def, f.ok, f.err
}

type fakeStory struct {
	id    string
	def   quest.Def
	ok    bool
	calls int
}

func (f *fakeStory) Pick(_ context.Context, _ domain.PlayerID) (string, quest.Def, bool) {
	f.calls++
	return f.id, f.def, f.ok
}

type publishedMsg struct {
	topic   string
	payload []byte
}

type fakePublisher struct {
	published []publishedMsg
}

func (f *fakePublisher) Publish(_ context.Context, topic string, payload []byte) error {
	f.published = append(f.published, publishedMsg{topic: topic, payload: append([]byte(nil), payload...)})
	return nil
}

// --- helpers ----------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testCfg() pacer.Config {
	return pacer.Config{
		DocksMin: 10, DocksMax: 20,
		JumpsMin: 10, JumpsMax: 20,
		MaxPendingOffers: 3,
		StoryShare:       0,
		OfferTTL:         24 * time.Hour,
	}
}

// procDef is a two-step deliver template with a 5000 reward on its last step.
func procDef() quest.Def {
	return quest.Def{
		ID: "deliver", Title: "Доставка груза", Offerable: true,
		Steps: []quest.Step{
			{Kind: quest.StepAcquireCargo, Desc: "Купи 10 руды"},
			{Kind: quest.StepDeliver, RewardCash: 5000, Desc: "Доставь груз"},
		},
	}
}

func storyDef() quest.Def {
	return quest.Def{
		ID: "saga1", Title: "Сага: пролог", Offerable: true,
		Steps: []quest.Step{{Kind: quest.StepDock, RewardCash: 3000, Desc: "Пристыкуйся"}},
	}
}

func fixedClock() *clock.FakeClock {
	return clock.NewFakeClock(time.Unix(1_700_000_000, 0).UTC())
}

// --- tests ------------------------------------------------------------------

// AC-1: the dock counter fires exactly on the N-th dock (threshold injected via
// counters), then resets and re-rolls; the offer + event carry the right shape.
func TestUnit_Pacer_OnDock_FiresExactlyOnThreshold(t *testing.T) {
	t.Parallel()
	counters := &fakeCounters{stored: domain.QuestCounters{Player: 7, NextDocks: 12}, hasRow: true}
	offers := &fakeOffers{}
	gen := &fakeGenerator{kind: "deliver", def: procDef(), ok: true}
	clk := fixedClock()
	pub := &fakePublisher{}
	p := pacer.New(counters, offers, gen, &fakeStory{}, pub, clk,
		rand.New(rand.NewSource(1)), testCfg(), discardLogger())

	for i := 0; i < 11; i++ {
		require.NoError(t, p.OnDock(context.Background(), 7, 3))
	}
	assert.Empty(t, offers.inserted, "no offer before the threshold")
	assert.Empty(t, pub.published)
	assert.Equal(t, 11, counters.stored.Docks)
	assert.Equal(t, 0, gen.calls)

	require.NoError(t, p.OnDock(context.Background(), 7, 3))
	require.Len(t, offers.inserted, 1)
	require.Len(t, pub.published, 1)

	// counter reset, threshold re-rolled into the config range
	assert.Equal(t, 0, counters.stored.Docks)
	assert.GreaterOrEqual(t, counters.stored.NextDocks, 10)
	assert.LessOrEqual(t, counters.stored.NextDocks, 20)
	assert.Zero(t, counters.stored.Jumps, "jumps untouched by a dock")

	off := offers.inserted[0]
	assert.Equal(t, domain.PlayerID(7), off.Player)
	assert.Equal(t, "deliver", off.TemplateID)
	assert.NotNil(t, off.Definition, "procedural offer freezes its definition")
	assert.Equal(t, "dock", off.Source)
	assert.Equal(t, clk.Now(), off.CreatedAt)
	assert.Equal(t, clk.Now().Add(24*time.Hour), off.ExpiresAt)

	assert.Equal(t, quest.OfferTopic(7), pub.published[0].topic)
	var ev quest.OfferEvent
	require.NoError(t, json.Unmarshal(pub.published[0].payload, &ev))
	assert.Equal(t, "proc:1", ev.OfferID)
	assert.Equal(t, "dock", ev.Source)
	assert.Equal(t, clk.Now().Add(24*time.Hour).Unix(), ev.ExpiresUnix)
	assert.Equal(t, int64(5000), ev.RewardCash)
	assert.Equal(t, "Доставка груза", ev.Title)
	assert.Equal(t, "Купи 10 руды", ev.Desc)
}

// AC-1b: at the live-offer cap the pacer skips generation but still consumes
// the threshold (reset + re-roll).
func TestUnit_Pacer_OnDock_AtPendingLimit_SkipsButResets(t *testing.T) {
	t.Parallel()
	counters := &fakeCounters{stored: domain.QuestCounters{Player: 7, Docks: 11, NextDocks: 12}, hasRow: true}
	offers := &fakeOffers{active: 3}
	gen := &fakeGenerator{kind: "deliver", def: procDef(), ok: true}
	pub := &fakePublisher{}
	p := pacer.New(counters, offers, gen, &fakeStory{}, pub, fixedClock(),
		rand.New(rand.NewSource(1)), testCfg(), discardLogger())

	require.NoError(t, p.OnDock(context.Background(), 7, 3))

	assert.Empty(t, offers.inserted, "no offer at the pending limit")
	assert.Empty(t, pub.published)
	assert.Zero(t, gen.calls, "generator not consulted at the pending limit")
	assert.Equal(t, 0, counters.stored.Docks, "counter reset")
	assert.GreaterOrEqual(t, counters.stored.NextDocks, 10)
	assert.LessOrEqual(t, counters.stored.NextDocks, 20)
}

// AC-3b: fail-closed — Generate reports nothing solvable, the attempt burns.
func TestUnit_Pacer_OnDock_GeneratorNothingToOffer_BurnsAttempt(t *testing.T) {
	t.Parallel()
	counters := &fakeCounters{stored: domain.QuestCounters{Player: 7, Docks: 11, NextDocks: 12}, hasRow: true}
	offers := &fakeOffers{}
	gen := &fakeGenerator{ok: false}
	pub := &fakePublisher{}
	p := pacer.New(counters, offers, gen, &fakeStory{}, pub, fixedClock(),
		rand.New(rand.NewSource(1)), testCfg(), discardLogger())

	require.NoError(t, p.OnDock(context.Background(), 7, 3))

	assert.Empty(t, offers.inserted)
	assert.Empty(t, pub.published)
	assert.Equal(t, 1, gen.calls)
	assert.Equal(t, 0, counters.stored.Docks, "burned: counter reset")
	assert.GreaterOrEqual(t, counters.stored.NextDocks, 10)
}

// StoryShare=1.0 always picks a story quest; the procedural generator is never
// consulted and the offer stores no frozen definition.
func TestUnit_Pacer_StoryShareOne_AlwaysStory(t *testing.T) {
	t.Parallel()
	cfg := testCfg()
	cfg.StoryShare = 1.0
	counters := &fakeCounters{stored: domain.QuestCounters{Player: 7, Docks: 11, NextDocks: 12}, hasRow: true}
	offers := &fakeOffers{}
	gen := &fakeGenerator{kind: "deliver", def: procDef(), ok: true}
	story := &fakeStory{id: "saga1", def: storyDef(), ok: true}
	pub := &fakePublisher{}
	p := pacer.New(counters, offers, gen, story, pub, fixedClock(),
		rand.New(rand.NewSource(1)), cfg, discardLogger())

	require.NoError(t, p.OnDock(context.Background(), 7, 3))

	require.Len(t, offers.inserted, 1)
	assert.Zero(t, gen.calls, "procedural generator not consulted under StoryShare=1")
	assert.Equal(t, 1, story.calls)
	off := offers.inserted[0]
	assert.Equal(t, "saga1", off.TemplateID)
	assert.Nil(t, off.Definition, "story offer resolves through template_id, no frozen definition")

	var ev quest.OfferEvent
	require.NoError(t, json.Unmarshal(pub.published[0].payload, &ev))
	assert.Equal(t, "proc:1", ev.OfferID)
	assert.Equal(t, int64(3000), ev.RewardCash)
	assert.Equal(t, "Сага: пролог", ev.Title)
}

// StoryShare=0.0 always picks a procedural template; the story picker is never
// consulted.
func TestUnit_Pacer_StoryShareZero_AlwaysProcedural(t *testing.T) {
	t.Parallel()
	cfg := testCfg() // StoryShare 0
	counters := &fakeCounters{stored: domain.QuestCounters{Player: 7, Docks: 11, NextDocks: 12}, hasRow: true}
	offers := &fakeOffers{}
	gen := &fakeGenerator{kind: "deliver", def: procDef(), ok: true}
	story := &fakeStory{id: "saga1", def: storyDef(), ok: true}
	p := pacer.New(counters, offers, gen, story, &fakePublisher{}, fixedClock(),
		rand.New(rand.NewSource(1)), cfg, discardLogger())

	require.NoError(t, p.OnDock(context.Background(), 7, 3))

	require.Len(t, offers.inserted, 1)
	assert.Zero(t, story.calls, "story picker not consulted under StoryShare=0")
	assert.Equal(t, 1, gen.calls)
	assert.Equal(t, "deliver", offers.inserted[0].TemplateID)
	assert.NotNil(t, offers.inserted[0].Definition)
}

// AC-6 + independence: the jump counter fires on its own threshold, tags the
// offer source="space", and leaves the dock counter untouched.
func TestUnit_Pacer_OnJump_IndependentCounterAndSpaceSource(t *testing.T) {
	t.Parallel()
	counters := &fakeCounters{
		stored: domain.QuestCounters{Player: 7, Docks: 11, NextDocks: 12, Jumps: 4, NextJumps: 5},
		hasRow: true,
	}
	offers := &fakeOffers{}
	gen := &fakeGenerator{kind: "kill", def: procDef(), ok: true}
	pub := &fakePublisher{}
	p := pacer.New(counters, offers, gen, &fakeStory{}, pub, fixedClock(),
		rand.New(rand.NewSource(1)), testCfg(), discardLogger())

	require.NoError(t, p.OnJump(context.Background(), 7, 9))

	require.Len(t, offers.inserted, 1)
	assert.Equal(t, "space", offers.inserted[0].Source)
	assert.Equal(t, 0, counters.stored.Jumps, "jump counter reset")
	assert.GreaterOrEqual(t, counters.stored.NextJumps, 10)
	assert.Equal(t, 11, counters.stored.Docks, "dock counter untouched by a jump")
	assert.Equal(t, 12, counters.stored.NextDocks, "dock threshold untouched by a jump")

	var ev quest.OfferEvent
	require.NoError(t, json.Unmarshal(pub.published[0].payload, &ev))
	assert.Equal(t, "space", ev.Source)
}

// First-ever trigger with no counters row: roll the threshold, stay silent,
// persist (Player set).
func TestUnit_Pacer_OnDock_FirstTrigger_RollsThresholdAndStaysSilent(t *testing.T) {
	t.Parallel()
	counters := &fakeCounters{} // no row
	offers := &fakeOffers{}
	p := pacer.New(counters, offers, &fakeGenerator{ok: true, def: procDef()}, &fakeStory{},
		&fakePublisher{}, fixedClock(), rand.New(rand.NewSource(1)), testCfg(), discardLogger())

	require.NoError(t, p.OnDock(context.Background(), 7, 3))

	assert.Empty(t, offers.inserted)
	assert.True(t, counters.hasRow, "counter persisted")
	assert.Equal(t, domain.PlayerID(7), counters.stored.Player)
	assert.Equal(t, 1, counters.stored.Docks)
	assert.GreaterOrEqual(t, counters.stored.NextDocks, 10)
	assert.LessOrEqual(t, counters.stored.NextDocks, 20)
}
