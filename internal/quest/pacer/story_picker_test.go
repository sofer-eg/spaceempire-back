package pacer_test

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
	"spaceempire/back/internal/quest/pacer"
)

// fakeHistory answers Get from a fixed map: a present row means "started",
// absent means "never started". errOn forces a read error for a quest id.
type fakeHistory struct {
	rows  map[string]domain.QuestProgress
	errOn map[string]bool
}

func (f *fakeHistory) Get(_ context.Context, _ domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	if f.errOn[questID] {
		return domain.QuestProgress{}, false, errors.New("boom")
	}
	p, ok := f.rows[questID]
	return p, ok, nil
}

// fakeLiveOffers answers ListActiveOffersByPlayer from a fixed slice (nil = the
// player holds no live offer); err forces a read error.
type fakeLiveOffers struct {
	list []domain.QuestOffer
	err  error
}

func (f *fakeLiveOffers) ListActiveOffersByPlayer(_ context.Context, _ domain.PlayerID, _ time.Time) ([]domain.QuestOffer, error) {
	return f.list, f.err
}

// noLiveOffers is the default live-offer reader for tests: the player holds none.
func noLiveOffers() *fakeLiveOffers { return &fakeLiveOffers{} }

// startedExcept marks every offerable quest as started except the given ids.
func startedExcept(except ...string) map[string]domain.QuestProgress {
	skip := make(map[string]bool, len(except))
	for _, id := range except {
		skip[id] = true
	}
	rows := map[string]domain.QuestProgress{}
	for _, d := range quest.Offerable() {
		if skip[d.ID] {
			continue
		}
		rows[d.ID] = domain.QuestProgress{QuestID: d.ID, Status: domain.QuestActive}
	}
	return rows
}

func TestUnit_StoryPicker_PicksUnstartedQuestWithoutPrerequisite(t *testing.T) {
	t.Parallel()
	h := &fakeHistory{rows: startedExcept("patrol")}
	picker := pacer.NewStaticStoryPicker(h, noLiveOffers(), fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	id, def, ok := picker.Pick(context.Background(), 7)

	require.True(t, ok)
	assert.Equal(t, "patrol", id)
	assert.Equal(t, "patrol", def.ID)
}

func TestUnit_StoryPicker_AlreadyStartedIsExcluded(t *testing.T) {
	t.Parallel()
	h := &fakeHistory{rows: startedExcept()} // everything started
	picker := pacer.NewStaticStoryPicker(h, noLiveOffers(), fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok, "no candidate when every offerable quest is already started")
}

func TestUnit_StoryPicker_PrerequisiteNotCompleted_Excluded(t *testing.T) {
	t.Parallel()
	// saga2 is the only unstarted quest, but its prerequisite saga1 is merely
	// active (started, not completed) — saga2 must not be offered.
	h := &fakeHistory{rows: startedExcept("saga2")}
	picker := pacer.NewStaticStoryPicker(h, noLiveOffers(), fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok, "quest with an incomplete prerequisite is not eligible")
}

func TestUnit_StoryPicker_PrerequisiteCompleted_Eligible(t *testing.T) {
	t.Parallel()
	h := &fakeHistory{rows: startedExcept("saga2")}
	h.rows["saga1"] = domain.QuestProgress{QuestID: "saga1", Status: domain.QuestCompleted}
	picker := pacer.NewStaticStoryPicker(h, noLiveOffers(), fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	id, def, ok := picker.Pick(context.Background(), 7)

	require.True(t, ok)
	assert.Equal(t, "saga2", id)
	assert.Equal(t, "saga2", def.ID)
}

func TestUnit_StoryPicker_HistoryError_SkipsCandidate(t *testing.T) {
	t.Parallel()
	// patrol would be eligible, but its history read errors — fail-closed skip
	// leaves no candidate rather than surfacing a broken pick.
	h := &fakeHistory{rows: startedExcept("patrol"), errOn: map[string]bool{"patrol": true}}
	picker := pacer.NewStaticStoryPicker(h, noLiveOffers(), fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok)
}

func TestUnit_StoryPicker_LiveOffer_ExcludesDuplicateStaticQuest(t *testing.T) {
	t.Parallel()
	// xt_6008000 is the only unstarted (eligible) static quest, but the player
	// already holds a live offer of it — Pick must not surface it again (TASK-137).
	h := &fakeHistory{rows: startedExcept("xt_6008000")}
	offers := &fakeLiveOffers{list: []domain.QuestOffer{{TemplateID: "xt_6008000"}}}
	picker := pacer.NewStaticStoryPicker(h, offers, fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok, "a static quest with a live offer is not offered again")
}

func TestUnit_StoryPicker_LiveOffer_ForeignTemplateDoesNotBlock(t *testing.T) {
	t.Parallel()
	// A live procedural offer (template_id = kind, never a static quest id) must
	// not exclude the eligible static quest — dedup matches template_id exactly.
	h := &fakeHistory{rows: startedExcept("xt_6008000")}
	offers := &fakeLiveOffers{list: []domain.QuestOffer{{TemplateID: "trade"}}} // real procedural kind
	picker := pacer.NewStaticStoryPicker(h, offers, fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	id, def, ok := picker.Pick(context.Background(), 7)

	require.True(t, ok)
	assert.Equal(t, "xt_6008000", id)
	assert.Equal(t, "xt_6008000", def.ID)
}

func TestUnit_StoryPicker_LiveOffersError_FailClosed(t *testing.T) {
	t.Parallel()
	// xt_6008000 would be eligible, but the live-offer read errors — fail-closed
	// yields no candidate (no dedup map, so Pick cannot safely proceed) rather
	// than surfacing a possibly-duplicate pick. Mirrors the history-read guard.
	h := &fakeHistory{rows: startedExcept("xt_6008000")}
	offers := &fakeLiveOffers{err: errors.New("boom")}
	picker := pacer.NewStaticStoryPicker(h, offers, fixedClock(), rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok, "an offers read error is fail-closed")
}
