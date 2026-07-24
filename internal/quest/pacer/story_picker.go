package pacer

import (
	"context"
	"log/slog"
	"math/rand"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// QuestHistory reads a player's progress on a quest — the pacer's story picker
// uses it to skip quests already started and to gate prerequisites. Satisfied
// by *quests.Repository.
type QuestHistory interface {
	Get(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error)
}

// StaticStoryPicker draws a story quest from the static offerable catalogue
// (FR-10): a quest the player has not started yet and whose prerequisite (if
// any) is completed. tutorial is not offerable, so it never surfaces here.
type StaticStoryPicker struct {
	history QuestHistory
	rng     *rand.Rand
	logger  *slog.Logger
}

// NewStaticStoryPicker wires the picker over the quest-progress store. rng
// chooses among the eligible candidates; inject a fixed-seed one in tests.
func NewStaticStoryPicker(history QuestHistory, rng *rand.Rand, logger *slog.Logger) *StaticStoryPicker {
	return &StaticStoryPicker{
		history: history,
		rng:     rng,
		logger:  logger.With("component", "quest.story_picker"),
	}
}

// Pick returns a random eligible story quest, ok=false when none qualify. A
// candidate is eligible when the player has no progress row for it and its
// prerequisite (if set) is completed. A history read error skips the candidate
// (fail-closed) rather than failing the pacer trigger.
func (s *StaticStoryPicker) Pick(ctx context.Context, player domain.PlayerID) (string, quest.Def, bool) {
	var eligible []quest.Def
	for _, def := range quest.Offerable() {
		if s.eligible(ctx, player, def) {
			eligible = append(eligible, def)
		}
	}
	if len(eligible) == 0 {
		return "", quest.Def{}, false
	}
	def := eligible[s.rng.Intn(len(eligible))]
	return def.ID, def, true
}

// eligible reports whether the player can be offered this story quest.
func (s *StaticStoryPicker) eligible(ctx context.Context, player domain.PlayerID, def quest.Def) bool {
	if _, started, err := s.history.Get(ctx, player, def.ID); err != nil {
		s.logger.ErrorContext(ctx, "story pick: read progress", "err", err, "player", int64(player), "quest", def.ID)
		return false
	} else if started {
		return false // already accepted / in progress / finished
	}
	if def.Prerequisite == "" {
		return true
	}
	prog, done, err := s.history.Get(ctx, player, def.Prerequisite)
	if err != nil {
		s.logger.ErrorContext(ctx, "story pick: read prerequisite", "err", err, "player", int64(player), "quest", def.Prerequisite)
		return false
	}
	return done && prog.Status == domain.QuestCompleted
}
