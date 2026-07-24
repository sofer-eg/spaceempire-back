package pacer_test

import (
	"context"
	"errors"
	"math/rand"
	"testing"

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
	picker := pacer.NewStaticStoryPicker(h, rand.New(rand.NewSource(1)), discardLogger())

	id, def, ok := picker.Pick(context.Background(), 7)

	require.True(t, ok)
	assert.Equal(t, "patrol", id)
	assert.Equal(t, "patrol", def.ID)
}

func TestUnit_StoryPicker_AlreadyStartedIsExcluded(t *testing.T) {
	t.Parallel()
	h := &fakeHistory{rows: startedExcept()} // everything started
	picker := pacer.NewStaticStoryPicker(h, rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok, "no candidate when every offerable quest is already started")
}

func TestUnit_StoryPicker_PrerequisiteNotCompleted_Excluded(t *testing.T) {
	t.Parallel()
	// saga2 is the only unstarted quest, but its prerequisite saga1 is merely
	// active (started, not completed) — saga2 must not be offered.
	h := &fakeHistory{rows: startedExcept("saga2")}
	picker := pacer.NewStaticStoryPicker(h, rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok, "quest with an incomplete prerequisite is not eligible")
}

func TestUnit_StoryPicker_PrerequisiteCompleted_Eligible(t *testing.T) {
	t.Parallel()
	h := &fakeHistory{rows: startedExcept("saga2")}
	h.rows["saga1"] = domain.QuestProgress{QuestID: "saga1", Status: domain.QuestCompleted}
	picker := pacer.NewStaticStoryPicker(h, rand.New(rand.NewSource(1)), discardLogger())

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
	picker := pacer.NewStaticStoryPicker(h, rand.New(rand.NewSource(1)), discardLogger())

	_, _, ok := picker.Pick(context.Background(), 7)

	assert.False(t, ok)
}
