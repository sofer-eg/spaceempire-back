package trade_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/trade"
)

// newServiceForIT wires a full trade.Service against the given pool, using
// the project transaction manager. The price catalog carries good 1's real
// [16, 96] band so dynamic pricing is exercised end-to-end.
func newServiceForIT(t *testing.T, pool *pgxpool.Pool) *trade.Service {
	t.Helper()
	tm := database.NewTxManager(pool)
	tradeRepo := traderepo.New(pool)
	playersRepo := playersrepo.New(pool)
	cargoRepo := cargorepo.New(pool)
	base := trade.NewPoolRepo(tradeRepo, playersRepo, cargoRepo)
	bal, err := balance.New([]balance.Goods{
		{ID: 1, Name: "Batteries", AvgPrice: 16, MaxPrice: 96, Space: 1},
	}, nil)
	require.NoError(t, err)
	return trade.New(base, trade.NewRepoTxRunner(tm, base), bal)
}

// seedShipDockedAt inserts a player (starting cash from migration default)
// and a ship pre-docked at the requested static so tests can call Buy / Sell
// straight away.
func seedShipDockedAt(t *testing.T, pool *pgxpool.Pool, dockedKind int16, dockedID int64) (domain.PlayerID, domain.ShipID) {
	t.Helper()
	var playerID, shipID int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID)
	require.NoError(t, err)
	err = pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, docked_kind, docked_id) VALUES ($1, $2, $3, $4) RETURNING id`,
		playerID, 1, dockedKind, dockedID).Scan(&shipID)
	require.NoError(t, err)
	return domain.PlayerID(playerID), domain.ShipID(shipID)
}

func TestIntegration_TradeService_Buy_Then_Sell_RoundTrip(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	svc := newServiceForIT(t, pool)
	ctx := context.Background()

	// Dynamic pricing is exclusive to production factories (phase 10.19
	// follow-up — trade stations / pirbases price flat). Give production
	// station 1 a two-way Batteries (good 1) market with a full shelf, so the
	// dynamic price sits at the band floor (avg_price = 16). UPSERT because the
	// station's recipe market need not already carry good 1.
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	_, err := pool.Exec(ctx,
		`INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock)
		 VALUES ($1, $2, 1, 1, 1, 800, 800)
		 ON CONFLICT (owner_kind, owner_id, goods_type_id)
		 DO UPDATE SET buy_price = 1, sell_price = 1, stock = 800, max_stock = 800`,
		int16(station.Kind), station.ID)
	require.NoError(t, err)

	playerID, shipID := seedShipDockedAt(t, pool, int16(station.Kind), station.ID)

	buy, err := svc.Buy(ctx, playerID, shipID, station, 1, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 16, buy.UnitPrice, "full shelf → avg_price")
	assert.EqualValues(t, 10000-16*5, buy.NewCash)
	assert.EqualValues(t, 80, buy.TotalAmount)

	// Selling back (now stock 795 of 800, still ~full) stays at the floor.
	sell, err := svc.Sell(ctx, playerID, shipID, station, 1, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 16, sell.UnitPrice)
	assert.EqualValues(t, 10000-16*5+16*5, sell.NewCash)
}

func TestIntegration_TradeService_Buy_RejectsNotDocked(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	svc := newServiceForIT(t, pool)

	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	// Ship inserted without docked_* fields → not docked.
	var playerID, shipID int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&playerID)
	require.NoError(t, err)
	err = pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id) VALUES ($1, $2) RETURNING id`,
		playerID, 1).Scan(&shipID)
	require.NoError(t, err)

	_, err = svc.Buy(context.Background(), domain.PlayerID(playerID), domain.ShipID(shipID), station, 7, 5)
	require.ErrorIs(t, err, trade.ErrNotDocked)
}

func TestIntegration_TradeService_Buy_RaceOnLimitedStock(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	svc := newServiceForIT(t, pool)
	ctx := context.Background()

	// Cap the station's stock at exactly 10 so 10 racing buys of 1 unit
	// each must succeed-or-fail without ever overshooting. A trade station
	// prices flat (phase 10.19 follow-up): the unit price is the seeded
	// sell_price (1), well under each buyer's 1000 cash, so price never blocks
	// a buy and the race is decided purely by stock.
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}
	_, err := pool.Exec(ctx,
		`UPDATE station_goods SET stock = 10, max_stock = 10, sell_price = 1 WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = 1`,
		int16(station.Kind), station.ID)
	require.NoError(t, err)

	const buyers = 20
	players := make([]domain.PlayerID, buyers)
	ships := make([]domain.ShipID, buyers)
	for i := 0; i < buyers; i++ {
		var playerID, shipID int64
		err := pool.QueryRow(ctx,
			`INSERT INTO players (login, password_hash, cash) VALUES ($1, $2, 1000) RETURNING id`,
			t.Name()+"_p_"+itoa(i), "x").Scan(&playerID)
		require.NoError(t, err)
		err = pool.QueryRow(ctx,
			`INSERT INTO ships (player_id, sector_id, docked_kind, docked_id) VALUES ($1, $2, $3, $4) RETURNING id`,
			playerID, 1, int16(station.Kind), station.ID).Scan(&shipID)
		require.NoError(t, err)
		players[i] = domain.PlayerID(playerID)
		ships[i] = domain.ShipID(shipID)
	}

	var successes int32
	var wg sync.WaitGroup
	for i := 0; i < buyers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Buy(ctx, players[i], ships[i], station, 1, 1)
			if err == nil {
				atomic.AddInt32(&successes, 1)
				return
			}
			// Only one of two failure modes is allowed.
			if err != trade.ErrInsufficientStock && err != trade.ErrInsufficientCash {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	// Exactly 10 buyers should have succeeded; no over-sell.
	assert.EqualValues(t, 10, successes)

	var finalStock int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT stock FROM station_goods WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = 1`,
		int16(station.Kind), station.ID).Scan(&finalStock))
	assert.EqualValues(t, 0, finalStock)
}

// itoa is a tiny stand-in for strconv.Itoa to keep imports minimal in the
// test file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
