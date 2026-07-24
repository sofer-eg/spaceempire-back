package quests

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// CounterRepository persists the per-player quest pacer state
// (player_quest_counters, TASK-89): dock/jump counts and the randomised
// thresholds. One row per player. See docs/specs/quest.md and the TASK-89 SRS.
type CounterRepository struct {
	exec database.Executor
}

// NewCounters wires a CounterRepository to the given executor.
func NewCounters(exec database.Executor) *CounterRepository {
	return &CounterRepository{exec: exec}
}

// WithExecutor returns a CounterRepository bound to a different executor (a tx).
func (r *CounterRepository) WithExecutor(exec database.Executor) *CounterRepository {
	return &CounterRepository{exec: exec}
}

const getCountersSQL = `
SELECT player_id, docks, jumps, next_docks, next_jumps
FROM player_quest_counters WHERE player_id = $1`

// GetCounters returns the player's pacer counters, ok=false when absent.
func (r *CounterRepository) GetCounters(ctx context.Context, player domain.PlayerID) (domain.QuestCounters, bool, error) {
	var (
		c        domain.QuestCounters
		playerID int64
	)
	err := r.exec.QueryRow(ctx, getCountersSQL, int64(player)).
		Scan(&playerID, &c.Docks, &c.Jumps, &c.NextDocks, &c.NextJumps)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.QuestCounters{}, false, nil
	}
	if err != nil {
		return domain.QuestCounters{}, false, fmt.Errorf("get quest counters: %w", err)
	}
	c.Player = domain.PlayerID(playerID)
	return c, true, nil
}

const upsertCountersSQL = `
INSERT INTO player_quest_counters (player_id, docks, jumps, next_docks, next_jumps)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (player_id) DO UPDATE SET
    docks = EXCLUDED.docks,
    jumps = EXCLUDED.jumps,
    next_docks = EXCLUDED.next_docks,
    next_jumps = EXCLUDED.next_jumps`

// UpsertCounters inserts or overwrites the player's pacer counters.
func (r *CounterRepository) UpsertCounters(ctx context.Context, c domain.QuestCounters) error {
	if _, err := r.exec.Exec(ctx, upsertCountersSQL,
		int64(c.Player), c.Docks, c.Jumps, c.NextDocks, c.NextJumps); err != nil {
		return fmt.Errorf("upsert quest counters: %w", err)
	}
	return nil
}
