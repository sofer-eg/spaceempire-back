package cargo_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/cargo"
	"spaceempire/back/internal/pkg/database/testdb"
)

// seedShip inserts a minimal ship row required by the cargobay capacity
// lookup. The cargo schema doesn't care about most ship fields; the rest
// of the columns rely on their DEFAULTs from migration 0001 / 0006.
func seedShip(t *testing.T, pool *pgxpool.Pool, sectorID int64) int64 {
	t.Helper()
	var playerID, shipID int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID)
	require.NoError(t, err)
	err = pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id) VALUES ($1, $2) RETURNING id`,
		playerID, sectorID).Scan(&shipID)
	require.NoError(t, err)
	return shipID
}

func TestIntegration_CargoRepository_GoodsType_ReturnsSeedRow(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()

	gt, err := repo.GoodsType(ctx, 1) // Batteries
	require.NoError(t, err)
	assert.Equal(t, domain.GoodsTypeID(1), gt.ID)
	assert.Equal(t, "Batteries", gt.Name)
	assert.InDelta(t, 1.0, gt.Space, 1e-9)
}

func TestIntegration_CargoRepository_GoodsType_NotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)

	_, err := repo.GoodsType(context.Background(), 99999)
	require.ErrorIs(t, err, cargo.ErrGoodsTypeNotFound)
}

func TestIntegration_CargoRepository_Capacity_Ship_ReturnsDefault(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()

	shipID := seedShip(t, pool, 1)

	capacity, err := repo.Capacity(ctx, domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID})
	require.NoError(t, err)
	assert.InDelta(t, 100.0, capacity, 1e-9)
}

func TestIntegration_CargoRepository_Capacity_Station_ReturnsDefault(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()

	// Migration 0004 seeded station id=1 in sector 1.
	capacity, err := repo.Capacity(ctx, domain.EntityRef{Kind: domain.EntityKindStation, ID: 1})
	require.NoError(t, err)
	assert.InDelta(t, 10000.0, capacity, 1e-9)
}

func TestIntegration_CargoRepository_Capacity_UnsupportedKind(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)

	_, err := repo.Capacity(context.Background(), domain.EntityRef{Kind: domain.EntityKindPirbase, ID: 1})
	require.ErrorIs(t, err, cargo.ErrUnsupportedOwnerKind)
}

func TestIntegration_CargoRepository_Capacity_OwnerNotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)

	_, err := repo.Capacity(context.Background(), domain.EntityRef{Kind: domain.EntityKindShip, ID: 99999})
	require.ErrorIs(t, err, cargo.ErrOwnerNotFound)
}

func TestIntegration_CargoRepository_AddListUsedSpace(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()
	shipID := seedShip(t, pool, 1)
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}

	require.NoError(t, repo.Add(ctx, owner, 1, 5, 0)) // 5 batteries (space=1) → 5
	require.NoError(t, repo.Add(ctx, owner, 1, 3, 0)) // upsert → 8 batteries
	require.NoError(t, repo.Add(ctx, owner, 2, 4, 0)) // 4 iron (space=2) → 8

	items, err := repo.ListByOwner(ctx, owner, 0)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, domain.GoodsTypeID(1), items[0].GoodsType)
	assert.EqualValues(t, 8, items[0].Quantity)
	assert.Equal(t, domain.GoodsTypeID(2), items[1].GoodsType)
	assert.EqualValues(t, 4, items[1].Quantity)

	used, err := repo.UsedSpace(ctx, owner)
	require.NoError(t, err)
	assert.InDelta(t, 8*1+4*2, used, 1e-9)
}

func TestIntegration_CargoRepository_Subtract_PartialAndDelete(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()
	shipID := seedShip(t, pool, 1)
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}

	require.NoError(t, repo.Add(ctx, owner, 1, 10, 0))
	require.NoError(t, repo.Subtract(ctx, owner, 1, 4, 0)) // 10 → 6

	items, err := repo.ListByOwner(ctx, owner, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.EqualValues(t, 6, items[0].Quantity)

	// Subtract the rest → row must be deleted.
	require.NoError(t, repo.Subtract(ctx, owner, 1, 6, 0))

	items, err = repo.ListByOwner(ctx, owner, 0)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestIntegration_CargoRepository_Subtract_Insufficient(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()
	shipID := seedShip(t, pool, 1)
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}

	// No row at all.
	err := repo.Subtract(ctx, owner, 1, 1, 0)
	require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)

	// Existing row, but qty too high.
	require.NoError(t, repo.Add(ctx, owner, 1, 3, 0))
	err = repo.Subtract(ctx, owner, 1, 4, 0)
	require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
}

// seedPlayer inserts a player row and returns its id. Used to give a station
// deposit a real depositor (goods_owner_id) in the per-player hold test.
func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		login, "x").Scan(&id))
	return domain.PlayerID(id)
}

func TestIntegration_CargoRepository_StationHold_PerPlayer(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := cargo.New(pool)
	ctx := context.Background()

	// Migration 0004 seeded station id=1 (cargobay 10000).
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	p1 := seedPlayer(t, pool, t.Name()+"-p1")
	p2 := seedPlayer(t, pool, t.Name()+"-p2")

	// Both players deposit the same goods type, plus an unowned (NPC) pool.
	// The four-column UNIQUE keeps these as three distinct rows.
	require.NoError(t, repo.Add(ctx, station, 1, 10, p1))
	require.NoError(t, repo.Add(ctx, station, 1, 5, p2))
	require.NoError(t, repo.Add(ctx, station, 1, 100, 0))

	// Each depositor's own stack is isolated.
	q1, err := repo.Quantity(ctx, station, 1, p1)
	require.NoError(t, err)
	assert.EqualValues(t, 10, q1)
	q2, err := repo.Quantity(ctx, station, 1, p2)
	require.NoError(t, err)
	assert.EqualValues(t, 5, q2)

	// p1's listing merges their 10 with the 100 unowned, never p2's 5.
	items, err := repo.ListByOwner(ctx, station, p1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, domain.GoodsTypeID(1), items[0].GoodsType)
	assert.EqualValues(t, 110, items[0].Quantity)

	// p1 sees that someone else holds this type; p2 likewise.
	others, err := repo.HasOthersGoods(ctx, station, 1, p1)
	require.NoError(t, err)
	assert.True(t, others)

	// Subtracting one depositor's stack leaves the others intact.
	require.NoError(t, repo.Subtract(ctx, station, 1, 10, p1))
	q1, err = repo.Quantity(ctx, station, 1, p1)
	require.NoError(t, err)
	assert.EqualValues(t, 0, q1, "p1 stack drained")
	q2, err = repo.Quantity(ctx, station, 1, p2)
	require.NoError(t, err)
	assert.EqualValues(t, 5, q2, "p2 stack untouched")
}
