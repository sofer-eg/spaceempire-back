// Package trade persists the station market (station_goods) and the
// minimal slice of the ships table that trade.Service needs to verify
// a player is docked at the right place. Cash lives in persistence/players;
// cargo lives in persistence/cargo. trade.Service composes all three
// repositories inside a single transaction.
package trade

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrMarketEntryNotFound is returned when the station does not offer the
// requested goods type (no row in station_goods for that pair).
var ErrMarketEntryNotFound = errors.New("trade: market entry not found")

// ErrShipNotFound is returned by LoadShipDock when the ship row is missing.
var ErrShipNotFound = errors.New("trade: ship not found")

// ErrUnsupportedStationKind is returned when the owner kind is not one of
// the three station-like kinds (station, trade_station, pirbase).
var ErrUnsupportedStationKind = errors.New("trade: unsupported station kind")

// ErrInsufficientStock is returned when AdjustStock would drive the stock
// below zero. Use it to surface "not enough goods at the station" to the
// caller without a follow-up SELECT.
var ErrInsufficientStock = errors.New("trade: insufficient stock at station")

// ErrStockOverflow is returned when AdjustStock would drive the stock past
// max_stock. The selling player tried to dump more than the station can hold.
var ErrStockOverflow = errors.New("trade: station stock would exceed max_stock")

// MarketEntry mirrors one station_goods row. Nil price means "not on offer
// in this direction": NULL buy_price = station does not buy, NULL sell_price
// = station does not sell.
type MarketEntry struct {
	Owner     domain.EntityRef
	GoodsType domain.GoodsTypeID
	BuyPrice  *int64
	SellPrice *int64
	Stock     int64
	MaxStock  int64
}

// ShipDock captures only the columns trade.Service needs to authorize a
// trade: who owns the ship, which sector it sits in, and whether it is
// parked at a static object.
type ShipDock struct {
	PlayerID domain.PlayerID
	SectorID domain.SectorID
	Docked   *domain.EntityRef
}

// Repository talks to station_goods and the dock-aware slice of ships
// through an Executor (pool or pgx.Tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor. Used
// by trade.Service when running Buy/Sell inside a single transaction.
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadShipDockSQL = `
SELECT player_id, sector_id, docked_kind, docked_id
FROM ships
WHERE id = $1
`

// LoadShipDock returns the ownership/dock view of one ship.
func (r *Repository) LoadShipDock(ctx context.Context, shipID domain.ShipID) (ShipDock, error) {
	var (
		playerID, sectorID int64
		dockedKind         *int16
		dockedID           *int64
	)
	err := r.exec.QueryRow(ctx, loadShipDockSQL, int64(shipID)).
		Scan(&playerID, &sectorID, &dockedKind, &dockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShipDock{}, ErrShipNotFound
	}
	if err != nil {
		return ShipDock{}, fmt.Errorf("query ship dock: %w", err)
	}
	out := ShipDock{
		PlayerID: domain.PlayerID(playerID),
		SectorID: domain.SectorID(sectorID),
	}
	if dockedKind != nil && dockedID != nil {
		out.Docked = &domain.EntityRef{Kind: domain.EntityKind(*dockedKind), ID: *dockedID}
	}
	return out, nil
}

const listMarketSQL = `
SELECT goods_type_id, buy_price, sell_price, stock, max_stock
FROM station_goods
WHERE owner_kind = $1 AND owner_id = $2
ORDER BY goods_type_id
`

// ListMarket returns every station_goods row for the owner.
func (r *Repository) ListMarket(ctx context.Context, owner domain.EntityRef) ([]MarketEntry, error) {
	if !isStationKind(owner.Kind) {
		return nil, ErrUnsupportedStationKind
	}
	rows, err := r.exec.Query(ctx, listMarketSQL, int16(owner.Kind), owner.ID)
	if err != nil {
		return nil, fmt.Errorf("query station_goods: %w", err)
	}
	defer rows.Close()

	var out []MarketEntry
	for rows.Next() {
		var (
			gid                 int32
			buyPrice, sellPrice *int64
			stock, maxStock     int64
		)
		if err := rows.Scan(&gid, &buyPrice, &sellPrice, &stock, &maxStock); err != nil {
			return nil, fmt.Errorf("scan station_goods: %w", err)
		}
		out = append(out, MarketEntry{
			Owner:     owner,
			GoodsType: domain.GoodsTypeID(gid),
			BuyPrice:  buyPrice,
			SellPrice: sellPrice,
			Stock:     stock,
			MaxStock:  maxStock,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate station_goods: %w", err)
	}
	return out, nil
}

const getMarketEntrySQL = `
SELECT buy_price, sell_price, stock, max_stock
FROM station_goods
WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3
`

// GetMarketEntry returns one row. ErrMarketEntryNotFound when the station
// does not offer the goods type at all.
func (r *Repository) GetMarketEntry(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) (MarketEntry, error) {
	if !isStationKind(owner.Kind) {
		return MarketEntry{}, ErrUnsupportedStationKind
	}
	var (
		buyPrice, sellPrice *int64
		stock, maxStock     int64
	)
	err := r.exec.QueryRow(ctx, getMarketEntrySQL, int16(owner.Kind), owner.ID, int32(gtype)).
		Scan(&buyPrice, &sellPrice, &stock, &maxStock)
	if errors.Is(err, pgx.ErrNoRows) {
		return MarketEntry{}, ErrMarketEntryNotFound
	}
	if err != nil {
		return MarketEntry{}, fmt.Errorf("query station_goods row: %w", err)
	}
	return MarketEntry{
		Owner:     owner,
		GoodsType: gtype,
		BuyPrice:  buyPrice,
		SellPrice: sellPrice,
		Stock:     stock,
		MaxStock:  maxStock,
	}, nil
}

const adjustStockSQL = `
UPDATE station_goods
SET stock = stock + $4
WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3
  AND stock + $4 >= 0
  AND stock + $4 <= max_stock
RETURNING stock
`

const stockBoundsSQL = `
SELECT stock, max_stock
FROM station_goods
WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3
`

// AdjustStock applies delta to the row's stock atomically and returns the
// new value. Negative delta = sale (stock leaves the station), positive
// delta = buy from player (stock enters). The conditional UPDATE refuses
// to drop below zero or rise past max_stock. When the UPDATE matches no
// rows we re-read to tell the caller exactly why.
func (r *Repository) AdjustStock(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error) {
	if !isStationKind(owner.Kind) {
		return 0, ErrUnsupportedStationKind
	}
	var newStock int64
	err := r.exec.QueryRow(ctx, adjustStockSQL, int16(owner.Kind), owner.ID, int32(gtype), delta).
		Scan(&newStock)
	if err == nil {
		return newStock, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("adjust stock: %w", err)
	}
	// UPDATE matched nothing — either the row is missing or the delta would
	// violate one of the bounds. Re-read to surface the precise reason.
	var stock, maxStock int64
	if err := r.exec.QueryRow(ctx, stockBoundsSQL, int16(owner.Kind), owner.ID, int32(gtype)).
		Scan(&stock, &maxStock); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrMarketEntryNotFound
		}
		return 0, fmt.Errorf("read stock bounds: %w", err)
	}
	switch {
	case stock+delta < 0:
		return 0, ErrInsufficientStock
	case stock+delta > maxStock:
		return 0, ErrStockOverflow
	default:
		return 0, fmt.Errorf("adjust stock: unexpected no-rows update for stock=%d delta=%d max=%d", stock, delta, maxStock)
	}
}

func isStationKind(k domain.EntityKind) bool {
	switch k {
	case domain.EntityKindStation, domain.EntityKindTradeStation, domain.EntityKindPirbase:
		return true
	}
	return false
}
