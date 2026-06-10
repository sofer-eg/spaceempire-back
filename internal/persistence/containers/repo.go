// Package containers persists loot containers (phase 4.6): cold-start
// LoadAll, the transactional ship-kill drop (RecordKill), pickup (move a
// container's cargo into a ship), and TTL/expiry Delete. A container's
// cargo lives in the cargo table under owner_kind = EntityKindContainer;
// this repo owns the containers table plus the compound writes that span
// containers + cargo + ships. See back/docs/specs/kill_object.md §4.
package containers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrNoSpace is returned by Pickup when the ship cannot fit the whole
// container (pickup is all-or-nothing).
var ErrNoSpace = errors.New("containers: not enough cargo space")

// ErrContainerNotFound is returned by Pickup when the container row is
// gone (already picked up / expired between the worker check and the tx).
var ErrContainerNotFound = errors.New("containers: not found")

// Repository talks to the containers table (reads via exec, compound
// writes via tm). The kill/pickup/delete operations span containers,
// cargo and ships, so they run inside a single TxManager transaction.
type Repository struct {
	exec database.Executor
	tm   *database.TxManager
}

// New wires a Repository. exec backs the reads (LoadAll, ShipCargo); tm
// backs the transactional writes (RecordKill, Pickup, Delete).
func New(exec database.Executor, tm *database.TxManager) *Repository {
	return &Repository{exec: exec, tm: tm}
}

const loadAllSQL = `
SELECT id, sector_id, pos_x, pos_y, expires_at
FROM containers
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every container in the sector — cold-start seed.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Container, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query containers: %w", err)
	}
	defer rows.Close()

	var out []domain.Container
	for rows.Next() {
		var (
			id, sectorIDRow int64
			posX, posY      float64
			expiresAt       time.Time
		)
		if err := rows.Scan(&id, &sectorIDRow, &posX, &posY, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		out = append(out, domain.Container{
			ID:        domain.ContainerID(id),
			SectorID:  domain.SectorID(sectorIDRow),
			Pos:       domain.Vec2{X: posX, Y: posY},
			ExpiresAt: expiresAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate containers: %w", err)
	}
	return out, nil
}

const shipCargoSQL = `
SELECT goods_type_id, quantity
FROM cargo
WHERE owner_kind = $1 AND owner_id = $2
ORDER BY goods_type_id
`

// ShipCargo lists a ship's cargo so the kill handler can plan the drop.
func (r *Repository) ShipCargo(ctx context.Context, ship domain.ShipID) ([]domain.CargoItem, error) {
	rows, err := r.exec.Query(ctx, shipCargoSQL, int16(domain.EntityKindShip), int64(ship))
	if err != nil {
		return nil, fmt.Errorf("query ship cargo: %w", err)
	}
	defer rows.Close()

	var out []domain.CargoItem
	for rows.Next() {
		var (
			gid int32
			qty int64
		)
		if err := rows.Scan(&gid, &qty); err != nil {
			return nil, fmt.Errorf("scan ship cargo: %w", err)
		}
		out = append(out, domain.CargoItem{GoodsType: domain.GoodsTypeID(gid), Quantity: qty})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ship cargo: %w", err)
	}
	return out, nil
}

const (
	insertContainerSQL = `
INSERT INTO containers (sector_id, pos_x, pos_y, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id
`
	insertCargoSQL = `
INSERT INTO cargo (owner_kind, owner_id, goods_type_id, quantity)
VALUES ($1, $2, $3, $4)
`
	deleteOwnerCargoSQL = `DELETE FROM cargo WHERE owner_kind = $1 AND owner_id = $2`
	deleteShipSQL       = `DELETE FROM ships WHERE id = $1`
	deleteContainerSQL  = `DELETE FROM containers WHERE id = $1`
)

// RecordKill deletes the victim ship and its leftover cargo, and spawns
// one container (with its cargo) per drop, all in one transaction. A ship
// with no drops still deletes cleanly and returns no containers.
func (r *Repository) RecordKill(ctx context.Context, victim domain.ShipID, sectorID domain.SectorID, drops []domain.ContainerDrop) ([]domain.Container, error) {
	var created []domain.Container
	err := r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		created = created[:0]
		for _, d := range drops {
			var id int64
			if err := tx.QueryRow(ctx, insertContainerSQL,
				int64(sectorID), d.Pos.X, d.Pos.Y, d.ExpiresAt,
			).Scan(&id); err != nil {
				return fmt.Errorf("insert container: %w", err)
			}
			if _, err := tx.Exec(ctx, insertCargoSQL,
				int16(domain.EntityKindContainer), id, int32(d.GoodsType), d.Quantity,
			); err != nil {
				return fmt.Errorf("insert container cargo: %w", err)
			}
			created = append(created, domain.Container{
				ID:        domain.ContainerID(id),
				SectorID:  sectorID,
				Pos:       d.Pos,
				ExpiresAt: d.ExpiresAt,
			})
		}
		if _, err := tx.Exec(ctx, deleteOwnerCargoSQL, int16(domain.EntityKindShip), int64(victim)); err != nil {
			return fmt.Errorf("delete victim cargo: %w", err)
		}
		if _, err := tx.Exec(ctx, deleteShipSQL, int64(victim)); err != nil {
			return fmt.Errorf("delete victim ship: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

const (
	containerExistsSQL = `SELECT EXISTS(SELECT 1 FROM containers WHERE id = $1)`
	ownerSpaceSQL      = `
SELECT COALESCE(SUM(c.quantity * g.space), 0)
FROM cargo c JOIN goods_types g ON g.id = c.goods_type_id
WHERE c.owner_kind = $1 AND c.owner_id = $2
`
	shipFreeSpaceSQL = `
SELECT s.cargobay - COALESCE((
    SELECT SUM(c.quantity * g.space)
    FROM cargo c JOIN goods_types g ON g.id = c.goods_type_id
    WHERE c.owner_kind = $2 AND c.owner_id = s.id
), 0)
FROM ships s WHERE s.id = $1
`
	moveCargoSQL = `
INSERT INTO cargo (owner_kind, owner_id, goods_type_id, quantity)
SELECT $1, $2, goods_type_id, quantity
FROM cargo WHERE owner_kind = $3 AND owner_id = $4
ON CONFLICT (owner_kind, owner_id, goods_type_id)
DO UPDATE SET quantity = cargo.quantity + EXCLUDED.quantity
`
)

// Pickup moves the whole container into the ship (capacity-checked) and
// removes the container. All-or-nothing: a ship that cannot fit the full
// container gets ErrNoSpace and nothing moves.
func (r *Repository) Pickup(ctx context.Context, container domain.ContainerID, ship domain.ShipID) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, containerExistsSQL, int64(container)).Scan(&exists); err != nil {
			return fmt.Errorf("check container: %w", err)
		}
		if !exists {
			return ErrContainerNotFound
		}

		var needed float64
		if err := tx.QueryRow(ctx, ownerSpaceSQL,
			int16(domain.EntityKindContainer), int64(container),
		).Scan(&needed); err != nil {
			return fmt.Errorf("container space: %w", err)
		}

		var free float64
		err := tx.QueryRow(ctx, shipFreeSpaceSQL, int64(ship), int16(domain.EntityKindShip)).Scan(&free)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("pickup: ship %d not found", ship)
		}
		if err != nil {
			return fmt.Errorf("ship free space: %w", err)
		}
		if needed > free {
			return ErrNoSpace
		}

		if _, err := tx.Exec(ctx, moveCargoSQL,
			int16(domain.EntityKindShip), int64(ship),
			int16(domain.EntityKindContainer), int64(container),
		); err != nil {
			return fmt.Errorf("move container cargo: %w", err)
		}
		if _, err := tx.Exec(ctx, deleteOwnerCargoSQL, int16(domain.EntityKindContainer), int64(container)); err != nil {
			return fmt.Errorf("delete container cargo: %w", err)
		}
		if _, err := tx.Exec(ctx, deleteContainerSQL, int64(container)); err != nil {
			return fmt.Errorf("delete container: %w", err)
		}
		return nil
	})
}

// Delete removes a container and its cargo (TTL expiry sweep).
func (r *Repository) Delete(ctx context.Context, id domain.ContainerID) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, deleteOwnerCargoSQL, int16(domain.EntityKindContainer), int64(id)); err != nil {
			return fmt.Errorf("delete container cargo: %w", err)
		}
		if _, err := tx.Exec(ctx, deleteContainerSQL, int64(id)); err != nil {
			return fmt.Errorf("delete container: %w", err)
		}
		return nil
	})
}
