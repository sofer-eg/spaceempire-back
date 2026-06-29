// Package torpedos persists the torpedos table for sector workers:
// cold-start LoadAll, immediate Create/Delete for launch and death, and
// BatchUpdate for the periodic snapshot of the mutable fields. Torpedoes
// (like drones, unlike missiles) are persistent state — see ЧТЗ doc-1 §3
// FR-001, NFR-002.
package torpedos

import (
	"context"
	"errors"
	"fmt"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrTorpedoNotFound is returned by Delete when no row with the given id
// exists. LoadAll never returns it (empty result is just an empty slice).
var ErrTorpedoNotFound = errors.New("torpedos: not found")

// Repository talks to the torpedos table via an Executor (pool or tx).
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
       target_kind, target_id, last_target_x, last_target_y,
       class, damage, speed, accel, turn_rate, hit_radius, splash_radius,
       hp, expires_at
FROM torpedos
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every torpedo that currently belongs to the given sector.
// Called once at worker startup to seed the in-memory state.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Torpedo, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query torpedos: %w", err)
	}
	defer rows.Close()

	var out []domain.Torpedo
	for rows.Next() {
		var (
			id, sectorIDRow, ownerShipID, playerID int64
			posX, posY, velX, velY                 float64
			dirX, dirY                             float64
			targetKind                             int16
			targetID                               int64
			lastTargetX, lastTargetY               float64
			class                                  int16
			damage                                 int
			speed, accel, turnRate                 float64
			hitRadius, splashRadius                float64
			hp                                     int
			expiresAt                              time.Time
		)
		if err := rows.Scan(
			&id, &sectorIDRow, &ownerShipID, &playerID,
			&posX, &posY, &velX, &velY, &dirX, &dirY,
			&targetKind, &targetID, &lastTargetX, &lastTargetY,
			&class, &damage, &speed, &accel, &turnRate, &hitRadius, &splashRadius,
			&hp, &expiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan torpedo: %w", err)
		}
		out = append(out, domain.Torpedo{
			ID:            domain.TorpedoID(id),
			SectorID:      domain.SectorID(sectorIDRow),
			OwnerShipID:   domain.ShipID(ownerShipID),
			PlayerID:      domain.PlayerID(playerID),
			Pos:           domain.Vec2{X: posX, Y: posY},
			Vel:           domain.Vec2{X: velX, Y: velY},
			Direction:     domain.Vec2{X: dirX, Y: dirY},
			Target:        domain.EntityRef{Kind: domain.EntityKind(targetKind), ID: targetID},
			LastTargetPos: domain.Vec2{X: lastTargetX, Y: lastTargetY},
			Class:         int(class),
			Damage:        damage,
			Speed:         speed,
			Accel:         accel,
			TurnRate:      turnRate,
			HitRadius:     hitRadius,
			SplashRadius:  splashRadius,
			HP:            hp,
			ExpiresAt:     expiresAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate torpedos: %w", err)
	}
	return out, nil
}

const createSQL = `
INSERT INTO torpedos (
    sector_id, owner_ship_id, player_id,
    pos_x, pos_y, vel_x, vel_y, direction_x, direction_y,
    target_kind, target_id, last_target_x, last_target_y,
    class, damage, speed, accel, turn_rate, hit_radius, splash_radius,
    hp, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
        $14, $15, $16, $17, $18, $19, $20, $21, $22)
RETURNING id
`

// Create inserts a new torpedo row and returns its database-assigned id —
// the authoritative TorpedoID that survives restarts. Immediate write at
// launch.
func (r *Repository) Create(ctx context.Context, t domain.Torpedo) (domain.TorpedoID, error) {
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		int64(t.SectorID), int64(t.OwnerShipID), int64(t.PlayerID),
		t.Pos.X, t.Pos.Y, t.Vel.X, t.Vel.Y, t.Direction.X, t.Direction.Y,
		int16(t.Target.Kind), t.Target.ID, t.LastTargetPos.X, t.LastTargetPos.Y,
		int16(t.Class), t.Damage, t.Speed, t.Accel, t.TurnRate, t.HitRadius, t.SplashRadius,
		t.HP, t.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert torpedo: %w", err)
	}
	return domain.TorpedoID(id), nil
}

const batchUpdateSQL = `
UPDATE torpedos AS t
SET
    pos_x         = u.pos_x,
    pos_y         = u.pos_y,
    vel_x         = u.vel_x,
    vel_y         = u.vel_y,
    direction_x   = u.direction_x,
    direction_y   = u.direction_y,
    last_target_x = u.last_target_x,
    last_target_y = u.last_target_y,
    hp            = u.hp,
    expires_at    = u.expires_at,
    updated_at    = NOW()
FROM unnest(
    $1::bigint[],
    $2::float8[],
    $3::float8[],
    $4::float8[],
    $5::float8[],
    $6::float8[],
    $7::float8[],
    $8::float8[],
    $9::float8[],
    $10::integer[],
    $11::timestamptz[]
) AS u(id, pos_x, pos_y, vel_x, vel_y, direction_x, direction_y,
       last_target_x, last_target_y, hp, expires_at)
WHERE t.id = u.id
`

// BatchUpdate writes the periodic-snapshot fields (position, velocity,
// direction, last-seen target position, hp, expires_at) of every passed
// torpedo in a single SQL statement. Empty input is a no-op. Missing rows
// are silently skipped — the dirty-set can race a delete. The static profile
// (class, damage, speed, accel, turn_rate, radii, target) never changes, so
// it is not batched.
func (r *Repository) BatchUpdate(ctx context.Context, ts []domain.Torpedo) error {
	if len(ts) == 0 {
		return nil
	}
	ids := make([]int64, len(ts))
	posX := make([]float64, len(ts))
	posY := make([]float64, len(ts))
	velX := make([]float64, len(ts))
	velY := make([]float64, len(ts))
	dirX := make([]float64, len(ts))
	dirY := make([]float64, len(ts))
	lastX := make([]float64, len(ts))
	lastY := make([]float64, len(ts))
	hp := make([]int32, len(ts))
	expiresAt := make([]time.Time, len(ts))
	for i, t := range ts {
		ids[i] = int64(t.ID)
		posX[i] = t.Pos.X
		posY[i] = t.Pos.Y
		velX[i] = t.Vel.X
		velY[i] = t.Vel.Y
		dirX[i] = t.Direction.X
		dirY[i] = t.Direction.Y
		lastX[i] = t.LastTargetPos.X
		lastY[i] = t.LastTargetPos.Y
		hp[i] = int32(t.HP)
		expiresAt[i] = t.ExpiresAt
	}
	if _, err := r.exec.Exec(ctx, batchUpdateSQL,
		ids, posX, posY, velX, velY, dirX, dirY, lastX, lastY, hp, expiresAt,
	); err != nil {
		return fmt.Errorf("batch update torpedos: %w", err)
	}
	return nil
}

const deleteSQL = `DELETE FROM torpedos WHERE id = $1`

// Delete removes a torpedo row. Immediate write at death / expire /
// detonation. Missing rows return ErrTorpedoNotFound so callers can tell a
// race (already gone) from a real removal.
func (r *Repository) Delete(ctx context.Context, id domain.TorpedoID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete torpedo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTorpedoNotFound
	}
	return nil
}
