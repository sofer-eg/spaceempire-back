// Package drones persists the drones table for sector workers: cold-start
// LoadAll, immediate Create/Delete for launch and death, and BatchUpdate
// for the periodic snapshot of the mutable fields. Drones (unlike
// missiles) are persistent state — see back/docs/specs/drones.md §3.
package drones

import (
	"context"
	"errors"
	"fmt"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrDroneNotFound is returned by Delete when no row with the given id
// exists. LoadAll never returns it (empty result is just an empty slice).
var ErrDroneNotFound = errors.New("drones: not found")

// Repository talks to the drones table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `
SELECT id, sector_id, owner_ship_id, player_id,
       pos_x, pos_y, vel_x, vel_y, direction_x, direction_y,
       target_kind, target_id, hp, damage, expires_at
FROM drones
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every drone that currently belongs to the given sector.
// Called once at worker startup to seed the in-memory state.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Drone, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query drones: %w", err)
	}
	defer rows.Close()

	var out []domain.Drone
	for rows.Next() {
		var (
			id, sectorIDRow, ownerShipID, playerID int64
			posX, posY, velX, velY                 float64
			dirX, dirY                             float64
			targetKind                             int16
			targetID                               int64
			hp, damage                             int
			expiresAt                              time.Time
		)
		if err := rows.Scan(
			&id, &sectorIDRow, &ownerShipID, &playerID,
			&posX, &posY, &velX, &velY, &dirX, &dirY,
			&targetKind, &targetID, &hp, &damage, &expiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan drone: %w", err)
		}
		out = append(out, domain.Drone{
			ID:          domain.DroneID(id),
			SectorID:    domain.SectorID(sectorIDRow),
			OwnerShipID: domain.ShipID(ownerShipID),
			PlayerID:    domain.PlayerID(playerID),
			Pos:         domain.Vec2{X: posX, Y: posY},
			Vel:         domain.Vec2{X: velX, Y: velY},
			Direction:   domain.Vec2{X: dirX, Y: dirY},
			Target:      domain.EntityRef{Kind: domain.EntityKind(targetKind), ID: targetID},
			HP:          hp,
			Damage:      damage,
			ExpiresAt:   expiresAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate drones: %w", err)
	}
	return out, nil
}

const createSQL = `
INSERT INTO drones (
    sector_id, owner_ship_id, player_id,
    pos_x, pos_y, vel_x, vel_y, direction_x, direction_y,
    target_kind, target_id, hp, damage, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
RETURNING id
`

// Create inserts a new drone row and returns its database-assigned id —
// the authoritative DroneID that survives restarts. Immediate write at
// launch.
func (r *Repository) Create(ctx context.Context, d domain.Drone) (domain.DroneID, error) {
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		int64(d.SectorID), int64(d.OwnerShipID), int64(d.PlayerID),
		d.Pos.X, d.Pos.Y, d.Vel.X, d.Vel.Y, d.Direction.X, d.Direction.Y,
		int16(d.Target.Kind), d.Target.ID, d.HP, d.Damage, d.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert drone: %w", err)
	}
	return domain.DroneID(id), nil
}

const batchUpdateSQL = `
UPDATE drones AS d
SET
    pos_x       = u.pos_x,
    pos_y       = u.pos_y,
    vel_x       = u.vel_x,
    vel_y       = u.vel_y,
    direction_x = u.direction_x,
    direction_y = u.direction_y,
    hp          = u.hp,
    expires_at  = u.expires_at,
    updated_at  = NOW()
FROM unnest(
    $1::bigint[],
    $2::float8[],
    $3::float8[],
    $4::float8[],
    $5::float8[],
    $6::float8[],
    $7::float8[],
    $8::integer[],
    $9::timestamptz[]
) AS u(id, pos_x, pos_y, vel_x, vel_y, direction_x, direction_y, hp, expires_at)
WHERE d.id = u.id
`

// BatchUpdate writes the periodic-snapshot fields (position, velocity,
// direction, hp, expires_at) of every passed drone in a single SQL
// statement. Empty input is a no-op. Missing rows are silently skipped —
// the dirty-set can race a delete. Target never changes in phase 4.4, so
// it is not batched.
func (r *Repository) BatchUpdate(ctx context.Context, ds []domain.Drone) error {
	if len(ds) == 0 {
		return nil
	}
	ids := make([]int64, len(ds))
	posX := make([]float64, len(ds))
	posY := make([]float64, len(ds))
	velX := make([]float64, len(ds))
	velY := make([]float64, len(ds))
	dirX := make([]float64, len(ds))
	dirY := make([]float64, len(ds))
	hp := make([]int32, len(ds))
	expiresAt := make([]time.Time, len(ds))
	for i, d := range ds {
		ids[i] = int64(d.ID)
		posX[i] = d.Pos.X
		posY[i] = d.Pos.Y
		velX[i] = d.Vel.X
		velY[i] = d.Vel.Y
		dirX[i] = d.Direction.X
		dirY[i] = d.Direction.Y
		hp[i] = int32(d.HP)
		expiresAt[i] = d.ExpiresAt
	}
	if _, err := r.exec.Exec(ctx, batchUpdateSQL,
		ids, posX, posY, velX, velY, dirX, dirY, hp, expiresAt,
	); err != nil {
		return fmt.Errorf("batch update drones: %w", err)
	}
	return nil
}

const deleteSQL = `DELETE FROM drones WHERE id = $1`

// Delete removes a drone row. Immediate write at death / expire / recall.
// Missing rows return ErrDroneNotFound so callers can tell a race (already
// gone) from a real removal.
func (r *Repository) Delete(ctx context.Context, id domain.DroneID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete drone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDroneNotFound
	}
	return nil
}
