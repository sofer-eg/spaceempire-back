// Package aistate persists the ai_state table for sector workers (phase
// 5.1): cold-start LoadAll and the periodic/shutdown BatchUpsert of every
// live controller's serialized state. There is no per-row Create/Delete —
// the row is created on first BatchUpsert and removed by the ships ON DELETE
// CASCADE when an AI ship dies.
package aistate

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Repository talks to the ai_state table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `
SELECT ship_id, sector_id, controller_kind, state_json
FROM ai_state
WHERE sector_id = $1
ORDER BY ship_id
`

// LoadAll returns the AI state of every controlled ship in the sector.
// Called once at worker startup to rebuild controllers.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.AIState, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query ai_state: %w", err)
	}
	defer rows.Close()

	var out []domain.AIState
	for rows.Next() {
		var (
			shipID, sectorIDRow int64
			kind                string
			stateJSON           []byte
		)
		if err := rows.Scan(&shipID, &sectorIDRow, &kind, &stateJSON); err != nil {
			return nil, fmt.Errorf("scan ai_state: %w", err)
		}
		out = append(out, domain.AIState{
			ShipID:         domain.ShipID(shipID),
			SectorID:       domain.SectorID(sectorIDRow),
			ControllerKind: kind,
			StateJSON:      stateJSON,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ai_state: %w", err)
	}
	return out, nil
}

const batchUpsertSQL = `
INSERT INTO ai_state (ship_id, sector_id, controller_kind, state_json, updated_at)
SELECT u.ship_id, u.sector_id, u.controller_kind, u.state_json, NOW()
FROM unnest(
    $1::bigint[],
    $2::bigint[],
    $3::text[],
    $4::jsonb[]
) AS u(ship_id, sector_id, controller_kind, state_json)
ON CONFLICT (ship_id) DO UPDATE SET
    sector_id       = EXCLUDED.sector_id,
    controller_kind = EXCLUDED.controller_kind,
    state_json      = EXCLUDED.state_json,
    updated_at      = NOW()
`

// BatchUpsert writes every passed controller's state in a single statement
// (insert-or-update keyed by ship_id). Empty input is a no-op. An empty or
// nil StateJSON is coerced to the JSON object "{}" so the jsonb cast never
// fails on an empty string.
func (r *Repository) BatchUpsert(ctx context.Context, states []domain.AIState) error {
	if len(states) == 0 {
		return nil
	}
	ids := make([]int64, len(states))
	sectors := make([]int64, len(states))
	kinds := make([]string, len(states))
	jsons := make([]string, len(states))
	for i, st := range states {
		ids[i] = int64(st.ShipID)
		sectors[i] = int64(st.SectorID)
		kinds[i] = st.ControllerKind
		if len(st.StateJSON) == 0 {
			jsons[i] = "{}"
		} else {
			jsons[i] = string(st.StateJSON)
		}
	}
	if _, err := r.exec.Exec(ctx, batchUpsertSQL, ids, sectors, kinds, jsons); err != nil {
		return fmt.Errorf("batch upsert ai_state: %w", err)
	}
	return nil
}
