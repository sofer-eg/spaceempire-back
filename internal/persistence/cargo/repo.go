// Package cargo persists inventories: per-owner cargo stacks, the
// goods_types reference table and the cargobay capacity of owning
// entities (ships, stations, trade stations). All writes are immediate —
// inventory is a critical resource, not a snapshot-driven one.
package cargo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrGoodsTypeNotFound is returned by GoodsType when no row matches.
var ErrGoodsTypeNotFound = errors.New("cargo: goods type not found")

// ErrOwnerNotFound is returned by Capacity when no row matches the owner.
var ErrOwnerNotFound = errors.New("cargo: owner not found")

// ErrInsufficientQuantity is returned by Subtract when the requested
// amount exceeds the existing stack (or no stack exists at all).
var ErrInsufficientQuantity = errors.New("cargo: insufficient quantity")

// ErrUnsupportedOwnerKind is returned by Capacity when the EntityKind has
// no cargobay column (Pirbase, Shipyard, Container, …) — those are not
// inventory owners in this milestone.
var ErrUnsupportedOwnerKind = errors.New("cargo: unsupported owner kind")

// Repository talks to cargo / goods_types / *.cargobay via an Executor.
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor. Used
// by callers running multiple cargo operations inside a single tx.
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const goodsTypeSQL = `SELECT id, name, space FROM goods_types WHERE id = $1`

// GoodsType loads one row from the goods_types reference table.
func (r *Repository) GoodsType(ctx context.Context, id domain.GoodsTypeID) (domain.GoodsType, error) {
	var (
		gid   int32
		name  string
		space float64
	)
	err := r.exec.QueryRow(ctx, goodsTypeSQL, int32(id)).Scan(&gid, &name, &space)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GoodsType{}, ErrGoodsTypeNotFound
	}
	if err != nil {
		return domain.GoodsType{}, fmt.Errorf("query goods_type: %w", err)
	}
	return domain.GoodsType{ID: domain.GoodsTypeID(gid), Name: name, Space: space}, nil
}

const listByOwnerSQL = `
SELECT goods_type_id, SUM(quantity) AS quantity
FROM cargo
WHERE owner_kind = $1 AND owner_id = $2
  AND goods_owner_id IN (0, $3)
GROUP BY goods_type_id
ORDER BY goods_type_id
`

// ListByOwner returns every stack physically held by the given entity that
// the viewer is allowed to see: unowned stacks (goods_owner_id = 0 — ship
// holds, container loot, NPC goods) plus the viewer's own deposits. Stacks of
// the same goods type are summed into one row, so a player sees a single
// quantity merging their deposit with any unowned pool. viewer = 0 (no player
// in context) yields only the unowned stacks, which is exactly a ship hold or
// an NPC inventory read.
func (r *Repository) ListByOwner(ctx context.Context, owner domain.EntityRef, viewer domain.PlayerID) ([]domain.CargoItem, error) {
	rows, err := r.exec.Query(ctx, listByOwnerSQL, int16(owner.Kind), owner.ID, int64(viewer))
	if err != nil {
		return nil, fmt.Errorf("query cargo: %w", err)
	}
	defer rows.Close()

	var out []domain.CargoItem
	for rows.Next() {
		var (
			gid int32
			qty int64
		)
		if err := rows.Scan(&gid, &qty); err != nil {
			return nil, fmt.Errorf("scan cargo: %w", err)
		}
		out = append(out, domain.CargoItem{
			GoodsType: domain.GoodsTypeID(gid),
			Quantity:  qty,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cargo: %w", err)
	}
	return out, nil
}

const quantitySQL = `
SELECT COALESCE(quantity, 0)
FROM cargo
WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3 AND goods_owner_id = $4
`

// Quantity returns the size of one specific stack (physical owner + goods type
// + depositor). Returns 0 when no such stack exists. Used by Move to split a
// station withdrawal across the player's own deposit and the unowned pool.
func (r *Repository) Quantity(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, goodsOwner domain.PlayerID) (int64, error) {
	var qty int64
	err := r.exec.QueryRow(ctx, quantitySQL, int16(owner.Kind), owner.ID, int32(gtype), int64(goodsOwner)).Scan(&qty)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("query cargo quantity: %w", err)
	}
	return qty, nil
}

const hasOthersGoodsSQL = `
SELECT EXISTS (
    SELECT 1 FROM cargo
    WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3
      AND goods_owner_id <> 0 AND goods_owner_id <> $4
      AND quantity > 0
)
`

// HasOthersGoods reports whether the entity holds a stack of gtype that
// belongs to a player other than viewer (and is not the unowned pool). Move
// uses it to answer a forbidden withdrawal (someone else's goods) with
// ErrForbidden instead of a generic "insufficient quantity".
func (r *Repository) HasOthersGoods(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, viewer domain.PlayerID) (bool, error) {
	var exists bool
	if err := r.exec.QueryRow(ctx, hasOthersGoodsSQL, int16(owner.Kind), owner.ID, int32(gtype), int64(viewer)).Scan(&exists); err != nil {
		return false, fmt.Errorf("query others goods: %w", err)
	}
	return exists, nil
}

const usedSpaceSQL = `
SELECT COALESCE(SUM(c.quantity * g.space), 0)
FROM cargo c
JOIN goods_types g ON g.id = c.goods_type_id
WHERE c.owner_kind = $1 AND c.owner_id = $2
`

// UsedSpace sums Quantity*Space across the owner's stacks.
func (r *Repository) UsedSpace(ctx context.Context, owner domain.EntityRef) (float64, error) {
	var used float64
	if err := r.exec.QueryRow(ctx, usedSpaceSQL, int16(owner.Kind), owner.ID).Scan(&used); err != nil {
		return 0, fmt.Errorf("query used space: %w", err)
	}
	return used, nil
}

// Capacity reads the cargobay column for the owner's table. Returns
// ErrUnsupportedOwnerKind for kinds with no cargobay column and
// ErrOwnerNotFound when no row matches the id.
func (r *Repository) Capacity(ctx context.Context, owner domain.EntityRef) (float64, error) {
	var sql string
	switch owner.Kind {
	case domain.EntityKindShip:
		sql = `SELECT cargobay FROM ships WHERE id = $1`
	case domain.EntityKindStation:
		sql = `SELECT cargobay FROM stations WHERE id = $1`
	case domain.EntityKindTradeStation:
		sql = `SELECT cargobay FROM trade_stations WHERE id = $1`
	default:
		return 0, ErrUnsupportedOwnerKind
	}
	var cap float64
	err := r.exec.QueryRow(ctx, sql, owner.ID).Scan(&cap)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrOwnerNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("query capacity: %w", err)
	}
	return cap, nil
}

const addSQL = `
INSERT INTO cargo (owner_kind, owner_id, goods_type_id, quantity, goods_owner_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner_kind, owner_id, goods_type_id, goods_owner_id)
DO UPDATE SET quantity = cargo.quantity + EXCLUDED.quantity
`

// Add inserts a new stack or increments an existing one for the owner. The
// stack is keyed by depositor (goodsOwner): 0 for ship holds / container loot
// / NPC goods, or a player id for a personal deposit into a station hold.
func (r *Repository) Add(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64, goodsOwner domain.PlayerID) error {
	if _, err := r.exec.Exec(ctx, addSQL, int16(owner.Kind), owner.ID, int32(gtype), qty, int64(goodsOwner)); err != nil {
		return fmt.Errorf("add cargo: %w", err)
	}
	return nil
}

const subtractSQL = `
UPDATE cargo
SET quantity = quantity - $4
WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3 AND goods_owner_id = $5 AND quantity >= $4
RETURNING quantity
`

const deleteEmptySQL = `
DELETE FROM cargo
WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3 AND goods_owner_id = $4 AND quantity = 0
`

// Subtract decrements one stack (physical owner + goods type + depositor) by
// qty. Returns ErrInsufficientQuantity if no such stack exists or its quantity
// is below qty. Stacks that hit zero are removed in the same call.
func (r *Repository) Subtract(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64, goodsOwner domain.PlayerID) error {
	var remaining int64
	err := r.exec.QueryRow(ctx, subtractSQL, int16(owner.Kind), owner.ID, int32(gtype), qty, int64(goodsOwner)).Scan(&remaining)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInsufficientQuantity
	}
	if err != nil {
		return fmt.Errorf("subtract cargo: %w", err)
	}
	if remaining == 0 {
		if _, err := r.exec.Exec(ctx, deleteEmptySQL, int16(owner.Kind), owner.ID, int32(gtype), int64(goodsOwner)); err != nil {
			return fmt.Errorf("delete empty cargo: %w", err)
		}
	}
	return nil
}
