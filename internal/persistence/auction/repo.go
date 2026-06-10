// Package auction persists the auction_lots and auction_bids tables. The
// economy/auction service composes this repository with persistence/players
// and persistence/cargo inside a single transaction for Create / Bid / Close.
package auction

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Status mirrors the SMALLINT status column in auction_lots. The integer
// codes are stable and shared with the migration's CHECK constraint.
type Status int16

const (
	StatusActive    Status = 0
	StatusClosed    Status = 1
	StatusCancelled Status = 2
)

// ErrLotNotFound is returned when no row matches the lot id.
var ErrLotNotFound = errors.New("auction: lot not found")

// ErrLotNotActive is returned by ForUpdate when the lot exists but is no
// longer in StatusActive (closed or cancelled). Use it to surface 410 Gone
// to API callers and to make Close idempotent for the closer.
var ErrLotNotActive = errors.New("auction: lot is not active")

// ErrShipNotFound is returned by LoadShipDock when the ship row is missing.
var ErrShipNotFound = errors.New("auction: ship not found")

// Lot is the full auction_lots row mapped to Go types.
type Lot struct {
	ID              int64
	SellerID        domain.PlayerID
	GoodsType       domain.GoodsTypeID
	Quantity        int64
	Source          domain.EntityRef
	StartPrice      int64
	CurrentPrice    int64
	CurrentBidderID *domain.PlayerID
	EndsAt          time.Time
	Status          Status
	CreatedAt       time.Time
}

// CreateLotParams carries the values Service.Create needs to insert a new
// row. start_price doubles as current_price on creation.
type CreateLotParams struct {
	SellerID   domain.PlayerID
	GoodsType  domain.GoodsTypeID
	Quantity   int64
	Source     domain.EntityRef
	StartPrice int64
	EndsAt     time.Time
}

// Repository talks to auction_lots and auction_bids through an Executor.
type Repository struct {
	exec database.Executor
}

// New wires a Repository bound to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor. Used by
// the auction service when running Create / Bid / Close inside a single tx.
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// ShipDock captures only the columns the auction service needs to authorize
// a Create/Bid against the ship's owner and dock state. Mirrors
// persistence/trade.ShipDock — the auction module keeps its own copy rather
// than importing the trade package.
type ShipDock struct {
	PlayerID domain.PlayerID
	Docked   *domain.EntityRef
}

const loadShipDockSQL = `
SELECT player_id, docked_kind, docked_id
FROM ships
WHERE id = $1
`

// LoadShipDock returns the ownership/dock view of one ship. Docked is nil
// when the ship is free in space.
func (r *Repository) LoadShipDock(ctx context.Context, shipID domain.ShipID) (ShipDock, error) {
	var (
		playerID   int64
		dockedKind *int16
		dockedID   *int64
	)
	err := r.exec.QueryRow(ctx, loadShipDockSQL, int64(shipID)).
		Scan(&playerID, &dockedKind, &dockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShipDock{}, ErrShipNotFound
	}
	if err != nil {
		return ShipDock{}, fmt.Errorf("query ship dock: %w", err)
	}
	out := ShipDock{PlayerID: domain.PlayerID(playerID)}
	if dockedKind != nil && dockedID != nil {
		out.Docked = &domain.EntityRef{Kind: domain.EntityKind(*dockedKind), ID: *dockedID}
	}
	return out, nil
}

const createLotSQL = `
INSERT INTO auction_lots (
    seller_id, goods_type_id, quantity,
    source_owner_kind, source_owner_id,
    start_price, current_price, ends_at, status
) VALUES ($1, $2, $3, $4, $5, $6, $6, $7, 0)
RETURNING id, created_at
`

// CreateLot inserts a new active lot. current_price is set to start_price.
func (r *Repository) CreateLot(ctx context.Context, p CreateLotParams) (Lot, error) {
	var (
		id        int64
		createdAt time.Time
	)
	err := r.exec.QueryRow(ctx, createLotSQL,
		int64(p.SellerID),
		int32(p.GoodsType),
		p.Quantity,
		int16(p.Source.Kind),
		p.Source.ID,
		p.StartPrice,
		p.EndsAt,
	).Scan(&id, &createdAt)
	if err != nil {
		return Lot{}, fmt.Errorf("insert auction_lot: %w", err)
	}
	return Lot{
		ID:           id,
		SellerID:     p.SellerID,
		GoodsType:    p.GoodsType,
		Quantity:     p.Quantity,
		Source:       p.Source,
		StartPrice:   p.StartPrice,
		CurrentPrice: p.StartPrice,
		EndsAt:       p.EndsAt,
		Status:       StatusActive,
		CreatedAt:    createdAt,
	}, nil
}

const selectLotColumns = `
id, seller_id, goods_type_id, quantity,
source_owner_kind, source_owner_id,
start_price, current_price, current_bidder_id,
ends_at, status, created_at
`

const getLotSQL = `SELECT ` + selectLotColumns + ` FROM auction_lots WHERE id = $1`

// GetLot reads one lot by id without locking.
func (r *Repository) GetLot(ctx context.Context, id int64) (Lot, error) {
	return r.scanLot(r.exec.QueryRow(ctx, getLotSQL, id))
}

const lockActiveLotSQL = `SELECT ` + selectLotColumns + `
FROM auction_lots
WHERE id = $1
FOR UPDATE
`

// LockLot reads one lot with FOR UPDATE so Bid/Close can mutate it without
// racing other transactions. Returns ErrLotNotActive when the row exists
// but is not StatusActive.
func (r *Repository) LockLot(ctx context.Context, id int64) (Lot, error) {
	lot, err := r.scanLot(r.exec.QueryRow(ctx, lockActiveLotSQL, id))
	if err != nil {
		return Lot{}, err
	}
	if lot.Status != StatusActive {
		return lot, ErrLotNotActive
	}
	return lot, nil
}

const listActiveSQL = `SELECT ` + selectLotColumns + `
FROM auction_lots
WHERE status = 0
ORDER BY ends_at, id
`

const listByParticipantSQL = `SELECT ` + selectLotColumns + `
FROM auction_lots
WHERE seller_id = $1 OR current_bidder_id = $1
ORDER BY created_at DESC
LIMIT $2
`

// ListByParticipant returns every lot the player is involved in — as seller or
// as the current high bidder — across all statuses, newest first. Backs
// GET /api/auction/mine ("my lots/bids" visibility).
func (r *Repository) ListByParticipant(ctx context.Context, player domain.PlayerID, limit int) ([]Lot, error) {
	rows, err := r.exec.Query(ctx, listByParticipantSQL, int64(player), limit)
	if err != nil {
		return nil, fmt.Errorf("query lots by participant: %w", err)
	}
	defer rows.Close()
	var out []Lot
	for rows.Next() {
		lot, err := r.scanLotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate participant lots: %w", err)
	}
	return out, nil
}

// ListActive returns every active lot ordered by ends_at. Used by GET /api/auction.
func (r *Repository) ListActive(ctx context.Context) ([]Lot, error) {
	rows, err := r.exec.Query(ctx, listActiveSQL)
	if err != nil {
		return nil, fmt.Errorf("query active lots: %w", err)
	}
	defer rows.Close()
	var out []Lot
	for rows.Next() {
		lot, err := r.scanLotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active lots: %w", err)
	}
	return out, nil
}

const listDueSQL = `
SELECT id FROM auction_lots
WHERE status = 0 AND ends_at <= $1
ORDER BY ends_at, id
LIMIT $2
`

// ListDue returns the ids of every active lot whose timer has already
// fired. The closer iterates the slice and calls Service.Close on each.
func (r *Repository) ListDue(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	rows, err := r.exec.Query(ctx, listDueSQL, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query due lots: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan due lot id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due lots: %w", err)
	}
	return ids, nil
}

const updateBidSQL = `
UPDATE auction_lots
SET current_price = $2, current_bidder_id = $3
WHERE id = $1
`

// UpdateBid writes the new leader for an already-locked lot.
func (r *Repository) UpdateBid(ctx context.Context, lotID int64, newPrice int64, bidder domain.PlayerID) error {
	tag, err := r.exec.Exec(ctx, updateBidSQL, lotID, newPrice, int64(bidder))
	if err != nil {
		return fmt.Errorf("update bid: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLotNotFound
	}
	return nil
}

const insertBidSQL = `
INSERT INTO auction_bids (lot_id, bidder_id, amount)
VALUES ($1, $2, $3)
`

// InsertBid appends one row to the audit trail.
func (r *Repository) InsertBid(ctx context.Context, lotID int64, bidder domain.PlayerID, amount int64) error {
	if _, err := r.exec.Exec(ctx, insertBidSQL, lotID, int64(bidder), amount); err != nil {
		return fmt.Errorf("insert bid: %w", err)
	}
	return nil
}

const findDeliveryShipSQL = `
SELECT s.id
FROM ships s
WHERE s.player_id = $1
  AND s.docked_kind IS NOT NULL
  AND s.cargobay - COALESCE((
        SELECT SUM(c.quantity * g.space)
        FROM cargo c
        JOIN goods_types g ON g.id = c.goods_type_id
        WHERE c.owner_kind = 1 AND c.owner_id = s.id
      ), 0) >= $2
ORDER BY s.id
LIMIT 1
`

// FindDeliveryShip returns the id of a docked ship owned by buyer whose
// remaining cargobay can fit requiredSpace units. Returns (0, false) when
// no such ship exists. Used by Service.Close to deliver an auction win;
// when this returns no ship the surplus is logged and dropped.
func (r *Repository) FindDeliveryShip(ctx context.Context, buyer domain.PlayerID, requiredSpace float64) (domain.ShipID, bool, error) {
	var id int64
	err := r.exec.QueryRow(ctx, findDeliveryShipSQL, int64(buyer), requiredSpace).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find delivery ship: %w", err)
	}
	return domain.ShipID(id), true, nil
}

const setStatusSQL = `UPDATE auction_lots SET status = $2 WHERE id = $1`

// SetStatus moves the lot to closed or cancelled. Called by Service.Close.
func (r *Repository) SetStatus(ctx context.Context, lotID int64, status Status) error {
	tag, err := r.exec.Exec(ctx, setStatusSQL, lotID, int16(status))
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLotNotFound
	}
	return nil
}

// scanLot adapts a single-row pgx.Row scan and translates pgx.ErrNoRows.
func (r *Repository) scanLot(row pgx.Row) (Lot, error) {
	var (
		id, sellerID, sourceID, quantity, startPrice, currentPrice int64
		goodsType                                                  int32
		sourceKind, status                                         int16
		bidderID                                                   *int64
		endsAt, createdAt                                          time.Time
	)
	err := row.Scan(
		&id, &sellerID, &goodsType, &quantity,
		&sourceKind, &sourceID,
		&startPrice, &currentPrice, &bidderID,
		&endsAt, &status, &createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lot{}, ErrLotNotFound
	}
	if err != nil {
		return Lot{}, fmt.Errorf("scan lot: %w", err)
	}
	lot := Lot{
		ID:           id,
		SellerID:     domain.PlayerID(sellerID),
		GoodsType:    domain.GoodsTypeID(goodsType),
		Quantity:     quantity,
		Source:       domain.EntityRef{Kind: domain.EntityKind(sourceKind), ID: sourceID},
		StartPrice:   startPrice,
		CurrentPrice: currentPrice,
		EndsAt:       endsAt,
		Status:       Status(status),
		CreatedAt:    createdAt,
	}
	if bidderID != nil {
		p := domain.PlayerID(*bidderID)
		lot.CurrentBidderID = &p
	}
	return lot, nil
}

// scanLotRow is the rows.Scan variant used by ListActive.
func (r *Repository) scanLotRow(rows pgx.Rows) (Lot, error) {
	var (
		id, sellerID, sourceID, quantity, startPrice, currentPrice int64
		goodsType                                                  int32
		sourceKind, status                                         int16
		bidderID                                                   *int64
		endsAt, createdAt                                          time.Time
	)
	err := rows.Scan(
		&id, &sellerID, &goodsType, &quantity,
		&sourceKind, &sourceID,
		&startPrice, &currentPrice, &bidderID,
		&endsAt, &status, &createdAt,
	)
	if err != nil {
		return Lot{}, fmt.Errorf("scan lot row: %w", err)
	}
	lot := Lot{
		ID:           id,
		SellerID:     domain.PlayerID(sellerID),
		GoodsType:    domain.GoodsTypeID(goodsType),
		Quantity:     quantity,
		Source:       domain.EntityRef{Kind: domain.EntityKind(sourceKind), ID: sourceID},
		StartPrice:   startPrice,
		CurrentPrice: currentPrice,
		EndsAt:       endsAt,
		Status:       Status(status),
		CreatedAt:    createdAt,
	}
	if bidderID != nil {
		p := domain.PlayerID(*bidderID)
		lot.CurrentBidderID = &p
	}
	return lot, nil
}
