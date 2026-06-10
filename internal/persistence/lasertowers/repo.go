// Package lasertowers persists the laser_towers table for sector workers:
// cold-start LoadAll (seeds SectorStatics.LaserTowers), Create (DB-assigned
// id, for the future build path and tests) and Delete (immediate write for
// the future 4.6 destruction path). Towers do not mutate this phase, so
// there is no BatchUpdate. See back/docs/specs/lasertowers.md §4.
package lasertowers

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrNotFound is returned by Delete when no row with the given id exists.
var ErrNotFound = errors.New("lasertowers: not found")

// Repository talks to the laser_towers table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `
SELECT id, owner_id, sector_id, pos_x, pos_y, hp, shield, race, built, max_shield, shield_recharge
FROM laser_towers
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every laser tower in the given sector. Called once at
// worker cold start to seed SectorStatics.LaserTowers.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.LaserTower, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query laser_towers: %w", err)
	}
	defer rows.Close()

	var out []domain.LaserTower
	for rows.Next() {
		var (
			id, sectorIDRow           int64
			ownerID                   *int64
			posX, posY                float64
			hp, shield                int
			maxShield, shieldRecharge int
			race                      int
			built                     bool
		)
		if err := rows.Scan(&id, &ownerID, &sectorIDRow, &posX, &posY, &hp, &shield, &race, &built, &maxShield, &shieldRecharge); err != nil {
			return nil, fmt.Errorf("scan laser_tower: %w", err)
		}
		t := domain.LaserTower{
			ID:             domain.LaserTowerID(id),
			SectorID:       domain.SectorID(sectorIDRow),
			Pos:            domain.Vec2{X: posX, Y: posY},
			HP:             hp,
			Shield:         shield,
			MaxShield:      maxShield,
			ShieldRecharge: shieldRecharge,
			Race:           race,
			Built:          built,
		}
		if ownerID != nil {
			pid := domain.PlayerID(*ownerID)
			t.OwnerID = &pid
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate laser_towers: %w", err)
	}
	return out, nil
}

const createSQL = `
INSERT INTO laser_towers (owner_id, sector_id, pos_x, pos_y, hp, shield, race, built)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id
`

// Create inserts a tower row and returns its DB-assigned id.
func (r *Repository) Create(ctx context.Context, t domain.LaserTower) (domain.LaserTowerID, error) {
	var ownerID *int64
	if t.OwnerID != nil {
		o := int64(*t.OwnerID)
		ownerID = &o
	}
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		ownerID, int64(t.SectorID), t.Pos.X, t.Pos.Y, t.HP, t.Shield, t.Race, t.Built,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert laser_tower: %w", err)
	}
	return domain.LaserTowerID(id), nil
}

const deleteSQL = `DELETE FROM laser_towers WHERE id = $1`

// Delete removes a tower row. Missing rows return ErrNotFound so callers
// can tell a race (already gone) from a real removal.
func (r *Repository) Delete(ctx context.Context, id domain.LaserTowerID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete laser_tower: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
