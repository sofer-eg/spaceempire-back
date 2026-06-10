package trade_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/pkg/database/testdb"
)

// seedShipAtStation inserts a player and a ship docked at the given station
// so tests can exercise LoadShipDock against a realistic schema state.
func seedShipAtStation(t *testing.T, pool *pgxpool.Pool, dockedKind int16, dockedID int64) (playerID int64, shipID int64) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID)
	require.NoError(t, err)
	err = pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, docked_kind, docked_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		playerID, 1, dockedKind, dockedID).Scan(&shipID)
	require.NoError(t, err)
	return playerID, shipID
}

func TestIntegration_TradeRepository_LoadShipDock_ReturnsDocked(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)

	_, shipID := seedShipAtStation(t, pool, int16(domain.EntityKindStation), 1)

	dock, err := repo.LoadShipDock(context.Background(), domain.ShipID(shipID))
	require.NoError(t, err)
	require.NotNil(t, dock.Docked)
	assert.Equal(t, domain.EntityKindStation, dock.Docked.Kind)
	assert.EqualValues(t, 1, dock.Docked.ID)
}

func TestIntegration_TradeRepository_LoadShipDock_NotFound(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)

	_, err := repo.LoadShipDock(context.Background(), domain.ShipID(999_999))
	require.ErrorIs(t, err, trade.ErrShipNotFound)
}

// seedStation1Market gives station 1 a deterministic two-row market — sells
// Microchips (good 7, 200/500), buys Iron (good 2) — independent of the game
// content seed (which varies by station type). Repo tests own their fixture.
func seedStation1Market(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`DELETE FROM station_goods WHERE owner_kind = $1 AND owner_id = 1`,
		int16(domain.EntityKindStation))
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock) VALUES
		    ($1, 1, 7, NULL, 180, 200, 500),
		    ($1, 1, 2, 40,   NULL, 0,   500)`,
		int16(domain.EntityKindStation))
	require.NoError(t, err)
}

func TestIntegration_TradeRepository_GetMarketEntry_ReturnsSeedRow(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)
	seedStation1Market(t, pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	entry, err := repo.GetMarketEntry(context.Background(), station, domain.GoodsTypeID(7))
	require.NoError(t, err)
	require.NotNil(t, entry.SellPrice)
	assert.EqualValues(t, 180, *entry.SellPrice)
	assert.Nil(t, entry.BuyPrice)
	assert.EqualValues(t, 200, entry.Stock)
}

func TestIntegration_TradeRepository_GetMarketEntry_NotFound(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	_, err := repo.GetMarketEntry(context.Background(), station, domain.GoodsTypeID(999))
	require.ErrorIs(t, err, trade.ErrMarketEntryNotFound)
}

func TestIntegration_TradeRepository_GetMarketEntry_UnsupportedKind(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)

	_, err := repo.GetMarketEntry(
		context.Background(),
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 1},
		1,
	)
	require.ErrorIs(t, err, trade.ErrUnsupportedStationKind)
}

func TestIntegration_TradeRepository_ListMarket_ReturnsAllRows(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)
	seedStation1Market(t, pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	entries, err := repo.ListMarket(context.Background(), station)
	require.NoError(t, err)
	require.Len(t, entries, 2) // seed: sells Microchips, buys Iron
}

func TestIntegration_TradeRepository_AdjustStock_DecrementsAndIncrements(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)
	seedStation1Market(t, pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	newStock, err := repo.AdjustStock(context.Background(), station, 7, -50)
	require.NoError(t, err)
	assert.EqualValues(t, 150, newStock)

	newStock, err = repo.AdjustStock(context.Background(), station, 7, 30)
	require.NoError(t, err)
	assert.EqualValues(t, 180, newStock)
}

func TestIntegration_TradeRepository_AdjustStock_InsufficientStock(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)
	seedStation1Market(t, pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	_, err := repo.AdjustStock(context.Background(), station, 7, -9999)
	require.ErrorIs(t, err, trade.ErrInsufficientStock)
}

func TestIntegration_TradeRepository_AdjustStock_Overflow(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)
	seedStation1Market(t, pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	// Seed stock=200, max=500; adding 9999 overflows.
	_, err := repo.AdjustStock(context.Background(), station, 7, 9999)
	require.ErrorIs(t, err, trade.ErrStockOverflow)
}

func TestIntegration_TradeRepository_AdjustStock_MissingEntry(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := trade.New(pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	_, err := repo.AdjustStock(context.Background(), station, domain.GoodsTypeID(999), 1)
	require.ErrorIs(t, err, trade.ErrMarketEntryNotFound)
}
