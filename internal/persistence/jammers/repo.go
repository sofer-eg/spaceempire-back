// Package jammers persists the jammers table for sector workers: cold-start
// LoadAll (seeds SectorStatics.Jammers so a deployed hyper-interference
// generator survives a restart), Create (DB-assigned id, used by the
// install-jammer command) and Delete (immediate write on the 6.2b destruction
// path so a killed generator is not resurrected). Jammers do not mutate
// periodically, so there is no BatchUpdate. See back/docs/specs/jammer.md.
package jammers

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrNotFound is returned by Delete when no row with the given id exists.
var ErrNotFound = errors.New("jammers: not found")

// Repository talks to the jammers table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `
SELECT id, owner_id, sector_id, pos_x, pos_y, race, built, hp, shield, max_shield, shield_recharge
FROM jammers
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every jammer in the given sector. Called once at worker cold
// start to seed SectorStatics.Jammers.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Jammer, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query jammers: %w", err)
	}
	defer rows.Close()

	var out []domain.Jammer
	for rows.Next() {
		var (
			id, sectorIDRow           int64
			ownerID                   *int64
			posX, posY                float64
			race                      int
			built                     bool
			hp, shield                int
			maxShield, shieldRecharge int
		)
		if err := rows.Scan(&id, &ownerID, &sectorIDRow, &posX, &posY, &race, &built, &hp, &shield, &maxShield, &shieldRecharge); err != nil {
			return nil, fmt.Errorf("scan jammer: %w", err)
		}
		jam := domain.Jammer{
			ID:             domain.JammerID(id),
			SectorID:       domain.SectorID(sectorIDRow),
			Pos:            domain.Vec2{X: posX, Y: posY},
			Race:           race,
			Built:          built,
			HP:             hp,
			Shield:         shield,
			MaxShield:      maxShield,
			ShieldRecharge: shieldRecharge,
		}
		if ownerID != nil {
			pid := domain.PlayerID(*ownerID)
			jam.OwnerID = &pid
		}
		out = append(out, jam)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jammers: %w", err)
	}
	return out, nil
}

const createSQL = `
INSERT INTO jammers (owner_id, sector_id, pos_x, pos_y, race, built, hp, shield, max_shield, shield_recharge)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id
`

// Create inserts a jammer row and returns its DB-assigned id.
func (r *Repository) Create(ctx context.Context, j domain.Jammer) (domain.JammerID, error) {
	var ownerID *int64
	if j.OwnerID != nil {
		o := int64(*j.OwnerID)
		ownerID = &o
	}
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		ownerID, int64(j.SectorID), j.Pos.X, j.Pos.Y, j.Race, j.Built, j.HP, j.Shield, j.MaxShield, j.ShieldRecharge,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert jammer: %w", err)
	}
	return domain.JammerID(id), nil
}

const deleteSQL = `DELETE FROM jammers WHERE id = $1`

// Delete removes a jammer row. Missing rows return ErrNotFound so callers can
// tell a race (already gone) from a real removal.
func (r *Repository) Delete(ctx context.Context, id domain.JammerID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete jammer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
