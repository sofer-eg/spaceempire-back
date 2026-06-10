// Package quests persists per-player quest progress (phase 8.12, extended in
// 8.17 with state/deadline/failed/abandoned) and reads the player-state
// snapshot the quest checker polls. Definitions live in internal/quest; this
// repo is storage + a read-only state query.
package quests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// shipOwnerKind is domain.EntityKindShip — cargo owned by a ship.
const shipOwnerKind = int16(domain.EntityKindShip)

// Repository talks to player_quests (and a read slice of ships/cargo/players)
// via an Executor.
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor (a tx).
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const ensureSQL = `
INSERT INTO player_quests (player_id, quest_id, deadline_at) VALUES ($1, $2, $3)
ON CONFLICT (player_id, quest_id) DO NOTHING`

// Ensure starts a quest at step 0 for the player (optionally with a deadline),
// or does nothing if a row already exists (idempotent).
func (r *Repository) Ensure(ctx context.Context, player domain.PlayerID, questID string, deadlineAt *time.Time) error {
	if _, err := r.exec.Exec(ctx, ensureSQL, int64(player), questID, deadlineAt); err != nil {
		return fmt.Errorf("ensure quest: %w", err)
	}
	return nil
}

const abandonSQL = `
UPDATE player_quests SET status = 'abandoned'
WHERE player_id = $1 AND quest_id = $2 AND status = 'active'`

// Abandon drops an active quest (no-op if not active).
func (r *Repository) Abandon(ctx context.Context, player domain.PlayerID, questID string) error {
	if _, err := r.exec.Exec(ctx, abandonSQL, int64(player), questID); err != nil {
		return fmt.Errorf("abandon quest: %w", err)
	}
	return nil
}

const selectCols = `player_id, quest_id, step_index, status, state, deadline_at, started_at, completed_at`

const getSQL = `SELECT ` + selectCols + ` FROM player_quests WHERE player_id = $1 AND quest_id = $2`

// Get returns the player's progress on a quest, ok=false when absent.
func (r *Repository) Get(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	p, err := scanProgress(r.exec.QueryRow(ctx, getSQL, int64(player), questID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.QuestProgress{}, false, nil
	}
	if err != nil {
		return domain.QuestProgress{}, false, fmt.Errorf("get quest: %w", err)
	}
	return p, true, nil
}

const lockSQL = getSQL + ` FOR UPDATE`

// Lock re-reads a progress row FOR UPDATE so concurrent event/poll advances
// serialise. ok=false when absent.
func (r *Repository) Lock(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	p, err := scanProgress(r.exec.QueryRow(ctx, lockSQL, int64(player), questID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.QuestProgress{}, false, nil
	}
	if err != nil {
		return domain.QuestProgress{}, false, fmt.Errorf("lock quest: %w", err)
	}
	return p, true, nil
}

const listActiveSQL = `SELECT ` + selectCols + `
FROM player_quests WHERE status = 'active' ORDER BY player_id, quest_id LIMIT $1`

// ListActive returns up to limit active quest progresses, for the poller.
func (r *Repository) ListActive(ctx context.Context, limit int) ([]domain.QuestProgress, error) {
	return r.queryProgress(ctx, listActiveSQL, limit)
}

const listActiveByPlayerSQL = `SELECT ` + selectCols + `
FROM player_quests WHERE player_id = $1 AND status = 'active' ORDER BY quest_id`

// ListActiveByPlayer returns the player's active quest progresses.
func (r *Repository) ListActiveByPlayer(ctx context.Context, player domain.PlayerID) ([]domain.QuestProgress, error) {
	return r.queryProgress(ctx, listActiveByPlayerSQL, int64(player))
}

func (r *Repository) queryProgress(ctx context.Context, sql string, args ...any) ([]domain.QuestProgress, error) {
	rows, err := r.exec.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query quests: %w", err)
	}
	defer rows.Close()
	var out []domain.QuestProgress
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("scan quest: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate quests: %w", err)
	}
	return out, nil
}

// setStepSQL advances the player's current step and resets the per-step counter.
const setStepSQL = `UPDATE player_quests SET step_index = $3, state = '{}' WHERE player_id = $1 AND quest_id = $2`

// SetStep advances the player's current step.
func (r *Repository) SetStep(ctx context.Context, player domain.PlayerID, questID string, step int) error {
	if _, err := r.exec.Exec(ctx, setStepSQL, int64(player), questID, step); err != nil {
		return fmt.Errorf("set quest step: %w", err)
	}
	return nil
}

const setStateSQL = `UPDATE player_quests SET state = $3 WHERE player_id = $1 AND quest_id = $2`

// SetState persists the per-step counter (JSONB).
func (r *Repository) SetState(ctx context.Context, player domain.PlayerID, questID string, state []byte) error {
	if _, err := r.exec.Exec(ctx, setStateSQL, int64(player), questID, state); err != nil {
		return fmt.Errorf("set quest state: %w", err)
	}
	return nil
}

const completeSQL = `
UPDATE player_quests SET status = 'completed', step_index = $3, completed_at = $4
WHERE player_id = $1 AND quest_id = $2`

// Complete marks the quest done at the final step.
func (r *Repository) Complete(ctx context.Context, player domain.PlayerID, questID string, finalStep int, at time.Time) error {
	if _, err := r.exec.Exec(ctx, completeSQL, int64(player), questID, finalStep, at); err != nil {
		return fmt.Errorf("complete quest: %w", err)
	}
	return nil
}

const failSQL = `
UPDATE player_quests SET status = 'failed', completed_at = $3
WHERE player_id = $1 AND quest_id = $2 AND status = 'active'`

// Fail marks an active quest failed (deadline expired).
func (r *Repository) Fail(ctx context.Context, player domain.PlayerID, questID string, at time.Time) error {
	if _, err := r.exec.Exec(ctx, failSQL, int64(player), questID, at); err != nil {
		return fmt.Errorf("fail quest: %w", err)
	}
	return nil
}

const shipStateSQL = `SELECT id, sector_id, docked_kind, docked_id FROM ships WHERE player_id = $1 ORDER BY id LIMIT 1`
const cargoUnitsSQL = `SELECT COALESCE(SUM(quantity), 0) FROM cargo WHERE owner_kind = $1 AND owner_id = $2`
const cashSQL = `SELECT cash FROM players WHERE id = $1`

// PlayerState reads the snapshot the checker needs: whether/where the player's
// ship is docked, its current sector, how many cargo units it holds, and the
// player's cash. A player with no ship reads as undocked / empty hold.
func (r *Repository) PlayerState(ctx context.Context, player domain.PlayerID) (
	docked bool, cargoUnits, cash, sector int64, dockedKind int16, dockedID int64, err error) {
	var shipID int64
	var dKind *int16
	var dID *int64
	scanErr := r.exec.QueryRow(ctx, shipStateSQL, int64(player)).Scan(&shipID, &sector, &dKind, &dID)
	hasShip := !errors.Is(scanErr, pgx.ErrNoRows)
	if scanErr != nil && hasShip {
		return false, 0, 0, 0, 0, 0, fmt.Errorf("query ship: %w", scanErr)
	}
	if hasShip {
		docked = dKind != nil
		if dKind != nil {
			dockedKind = *dKind
		}
		if dID != nil {
			dockedID = *dID
		}
		if e := r.exec.QueryRow(ctx, cargoUnitsSQL, shipOwnerKind, shipID).Scan(&cargoUnits); e != nil {
			return false, 0, 0, 0, 0, 0, fmt.Errorf("query cargo units: %w", e)
		}
	}
	if e := r.exec.QueryRow(ctx, cashSQL, int64(player)).Scan(&cash); e != nil {
		return false, 0, 0, 0, 0, 0, fmt.Errorf("query cash: %w", e)
	}
	return docked, cargoUnits, cash, sector, dockedKind, dockedID, nil
}

func scanProgress(row pgx.Row) (domain.QuestProgress, error) {
	var (
		p           domain.QuestProgress
		status      string
		stepIndex   int16
		state       []byte
		deadlineAt  *time.Time
		completedAt *time.Time
	)
	if err := row.Scan(&p.Player, &p.QuestID, &stepIndex, &status, &state, &deadlineAt, &p.StartedAt, &completedAt); err != nil {
		return domain.QuestProgress{}, err
	}
	p.StepIndex = int(stepIndex)
	p.Status = domain.QuestStatus(status)
	p.State = state
	if deadlineAt != nil {
		p.DeadlineAt = *deadlineAt
	}
	if completedAt != nil {
		p.CompletedAt = *completedAt
	}
	return p, nil
}
