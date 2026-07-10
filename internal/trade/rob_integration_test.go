package trade_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/trade"
)

// TestIntegration_TradeService_Rob_RoundTrip exercises the station-hack market
// path (SP UseHack, TASK-100.3.9.3) end-to-end against a real database: it robs
// the richest good of a trade station and deposits the loot into the hacker's
// hold in one transaction. Reading the committed station_goods.stock and the
// ship's cargo back proves both survive a cold-start (they are in the DB).
func TestIntegration_TradeService_Rob_RoundTrip(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	svc := newServiceForIT(t, pool)
	ctx := context.Background()

	// Trade station 1 has the universal market (migration 0044: every good at
	// stock 500 / max 1_000_000). Make good 1 the richest AND pass the 30% gate:
	// stock 1000 of a 1000 cap.
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}
	_, err := pool.Exec(ctx,
		`UPDATE station_goods SET stock = 1000, max_stock = 1000
		 WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = 1`,
		int16(station.Kind), station.ID)
	require.NoError(t, err)

	// A hacker ship with a roomy hold (loot must fit so it is delivered, not
	// spilled into a container).
	var playerID, shipID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO ships (player_id, sector_id, cargobay) VALUES ($1, 1, 1000000) RETURNING id`,
		playerID).Scan(&shipID))
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}

	out, err := svc.Rob(ctx, station, shipRef, 0.15, 0.05, 0.3, true)
	require.NoError(t, err)
	assert.EqualValues(t, 1, out.GoodsType, "richest good targeted")
	assert.EqualValues(t, 150, out.Robbed)
	assert.EqualValues(t, 50, out.Damaged)
	assert.True(t, out.Delivered)

	// station_goods.stock lost robbed+damaged (200) — committed.
	var stock int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT stock FROM station_goods WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = 1`,
		int16(station.Kind), station.ID).Scan(&stock))
	assert.EqualValues(t, 800, stock)

	// The hold gained the robbed 150 — committed.
	var qty int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT quantity FROM cargo WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = 1 AND goods_owner_id = 0`,
		int16(domain.EntityKindShip), shipID).Scan(&qty))
	assert.EqualValues(t, 150, qty)
}

// AC-4: the scope-extension fires on a realistically-filled PRODUCTION station
// (owner_kind 2). Station 2 carries real station_goods (migration 0042); with a
// good at 47% of a 10000 cap the 30% gate passes and stock is actually deducted.
func TestIntegration_TradeService_Rob_ProductionStation_RoundTrip(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	svc := newServiceForIT(t, pool)
	ctx := context.Background()

	// Production station 2 (owner_kind 2). Make good 1 the clear richest at 47%
	// fill of a 10000 cap; drop the rest so the pick is deterministic.
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 2}
	_, err := pool.Exec(ctx,
		`UPDATE station_goods SET stock = 100 WHERE owner_kind = 2 AND owner_id = 2`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE station_goods SET stock = 4700, max_stock = 10000
		 WHERE owner_kind = 2 AND owner_id = 2 AND goods_type_id = 1`)
	require.NoError(t, err)

	var playerID, shipID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO ships (player_id, sector_id, cargobay) VALUES ($1, 1, 1000000) RETURNING id`,
		playerID).Scan(&shipID))
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}

	out, err := svc.Rob(ctx, station, shipRef, 0.15, 0.05, 0.3, true)
	require.NoError(t, err, "47%-full factory passes the 30% gate — hack is NOT inert")
	assert.EqualValues(t, 1, out.GoodsType)
	assert.EqualValues(t, 705, out.Robbed)  // floor(0.15*4700)
	assert.EqualValues(t, 235, out.Damaged) // floor(0.05*4700)
	assert.True(t, out.Delivered)
	assert.EqualValues(t, 10000, out.MaxStock, "penalty denominator is realistic → non-zero")

	var stock int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT stock FROM station_goods WHERE owner_kind = 2 AND owner_id = 2 AND goods_type_id = 1`).
		Scan(&stock))
	assert.EqualValues(t, 4700-705-235, stock) // 3760

	var qty int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT quantity FROM cargo WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = 1 AND goods_owner_id = 0`,
		int16(domain.EntityKindShip), shipID).Scan(&qty))
	assert.EqualValues(t, 705, qty)
}

// A trade station left at the live universal-market seed (stock 500 / max
// 1_000_000 = 0.05%) is too depleted to hack — the known resale-market
// limitation is preserved.
func TestIntegration_TradeService_Rob_TradeStationSeed_TooLittle(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	svc := newServiceForIT(t, pool)
	ctx := context.Background()

	// Trade station 1 untouched: migration 0044 seeds every good at 500/1_000_000.
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}
	var playerID, shipID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO ships (player_id, sector_id, cargobay) VALUES ($1, 1, 1000000) RETURNING id`,
		playerID).Scan(&shipID))
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}

	_, err := svc.Rob(ctx, station, shipRef, 0.15, 0.05, 0.3, true)
	assert.ErrorIs(t, err, trade.ErrTooLittleGoods)
}
