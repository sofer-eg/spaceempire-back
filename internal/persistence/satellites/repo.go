// Package satellites persists the satellites table for sector workers:
// cold-start LoadAll (seeds SectorStatics.Satellites so a deployed satellite
// survives a restart), Create (DB-assigned id, used by the install-satellite
// command) and Delete (immediate write on the 6.2b destruction path so a
// killed satellite is not resurrected). Satellites do not mutate periodically,
// so there is no BatchUpdate. See back/docs/specs/satellite.md.
package satellites

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrNotFound is returned by Delete when no row with the given id exists.
var ErrNotFound = errors.New("satellites: not found")

// Repository talks to the satellites table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `
SELECT id, owner_id, sector_id, pos_x, pos_y, race, built, hp, shield, max_shield, shield_recharge
FROM satellites
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every satellite in the given sector. Called once at worker
// cold start to seed SectorStatics.Satellites.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Satellite, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query satellites: %w", err)
	}
	defer rows.Close()

	var out []domain.Satellite
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
			return nil, fmt.Errorf("scan satellite: %w", err)
		}
		sat := domain.Satellite{
			ID:             domain.SatelliteID(id),
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
			sat.OwnerID = &pid
		}
		out = append(out, sat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate satellites: %w", err)
	}
	return out, nil
}

const createSQL = `
INSERT INTO satellites (owner_id, sector_id, pos_x, pos_y, race, built, hp, shield, max_shield, shield_recharge)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id
`

// Create inserts a satellite row and returns its DB-assigned id.
func (r *Repository) Create(ctx context.Context, s domain.Satellite) (domain.SatelliteID, error) {
	var ownerID *int64
	if s.OwnerID != nil {
		o := int64(*s.OwnerID)
		ownerID = &o
	}
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		ownerID, int64(s.SectorID), s.Pos.X, s.Pos.Y, s.Race, s.Built, s.HP, s.Shield, s.MaxShield, s.ShieldRecharge,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert satellite: %w", err)
	}
	return domain.SatelliteID(id), nil
}

const deleteSQL = `DELETE FROM satellites WHERE id = $1`

// Delete removes a satellite row. Missing rows return ErrNotFound so callers
// can tell a race (already gone) from a real removal.
func (r *Repository) Delete(ctx context.Context, id domain.SatelliteID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete satellite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
