// Package asteroids persists the asteroids table for sector workers:
// cold-start LoadAll, BatchUpdate for the periodic mass snapshot, and an
// immediate Delete when an asteroid is mined out (phase 5.4). Pos and
// ore_type never change after creation, so only mass is batched.
package asteroids

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrAsteroidNotFound is returned by Delete when no row with the given id
// exists. LoadAll never returns it (an empty result is just an empty slice).
var ErrAsteroidNotFound = errors.New("asteroids: not found")

// Repository talks to the asteroids table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `
SELECT id, sector_id, pos_x, pos_y, mass, ore_type
FROM asteroids
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every asteroid currently in the given sector. Called once
// at worker startup (and by the NPC spawner, to pick a miner's target).
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Asteroid, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query asteroids: %w", err)
	}
	defer rows.Close()

	var out []domain.Asteroid
	for rows.Next() {
		var (
			id, sectorIDRow, mass, oreType int64
			posX, posY                     float64
		)
		if err := rows.Scan(&id, &sectorIDRow, &posX, &posY, &mass, &oreType); err != nil {
			return nil, fmt.Errorf("scan asteroid: %w", err)
		}
		out = append(out, domain.Asteroid{
			ID:       domain.AsteroidID(id),
			SectorID: domain.SectorID(sectorIDRow),
			Pos:      domain.Vec2{X: posX, Y: posY},
			Mass:     mass,
			OreType:  domain.GoodsTypeID(oreType),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate asteroids: %w", err)
	}
	return out, nil
}

const batchUpdateSQL = `
UPDATE asteroids AS a
SET mass = u.mass, updated_at = NOW()
FROM unnest($1::bigint[], $2::bigint[]) AS u(id, mass)
WHERE a.id = u.id
`

// BatchUpdate writes the remaining mass of every passed asteroid in a single
// statement (the periodic snapshot). Empty input is a no-op; rows missing
// from the table are silently skipped — the snapshot can race a Delete.
func (r *Repository) BatchUpdate(ctx context.Context, as []domain.Asteroid) error {
	if len(as) == 0 {
		return nil
	}
	ids := make([]int64, len(as))
	mass := make([]int64, len(as))
	for i, a := range as {
		ids[i] = int64(a.ID)
		mass[i] = a.Mass
	}
	if _, err := r.exec.Exec(ctx, batchUpdateSQL, ids, mass); err != nil {
		return fmt.Errorf("batch update asteroids: %w", err)
	}
	return nil
}

const deleteSQL = `DELETE FROM asteroids WHERE id = $1`

// Delete removes a depleted asteroid. Immediate write when mass hits zero.
// Returns ErrAsteroidNotFound when no row matched.
func (r *Repository) Delete(ctx context.Context, id domain.AsteroidID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete asteroid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAsteroidNotFound
	}
	return nil
}
