package app

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/trade"
)

// These tests exercise the real hackRaider against Postgres, because the
// invariant it implements (stock deduction + loot container in ONE transaction,
// TASK-160) is invisible to the unit suite: app's fakeHackMarket and sector's
// fakeRobber are atomic by construction.
//
// Production station 2 (owner_kind 2) carries real station_goods from migration
// 0042; good 1 (batteries) is driven to 47% of a 10000 cap so the 30% gate passes
// and the same fractions the balance uses (0.15 / 0.05) yield 705 robbed + 235
// damaged — the numbers TestIntegration_TradeService_Rob_ProductionStation_RoundTrip
// pins for the market half.
const (
	raiderStationID  = int64(2)
	raiderGoods      = int32(1)
	raiderStock      = int64(4700)
	raiderRobbed     = int64(705)
	raiderDamaged    = int64(235)
	raiderSectorID   = domain.SectorID(1)
	raiderRobFrac    = 0.15
	raiderDamageFrac = 0.05
	raiderMinFrac    = 0.3
)

// raiderFixture spins up a schema-migrated Postgres and returns the real raider
// over it, plus the player that owns the hacking ships.
//
// The pool is capped at ONE connection on purpose, exactly as installerFixture
// does: the raider must do all of its work on its own transaction, which holds
// that single connection for its whole lifetime. A container insert issued
// against the pool instead of the transaction would have to acquire a second
// connection and would block until the context expires — so "the loot container
// rides the rob's transaction" is an observable property here, not a claim.
//
// Called ONCE for the whole group (the cases are sequential subtests): testdb.Setup
// starts a container and runs every migration, and this package already pays that
// cost twice.
func raiderFixture(t *testing.T) (*pgxpool.Pool, hackRaider, domain.PlayerID) {
	t.Helper()

	dsn := testdb.Setup(t).Config().ConnString()
	poolCfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse dsn")
	poolCfg.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	require.NoError(t, err, "single-connection pool")
	t.Cleanup(pool.Close)

	var playerID int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ('raider', 'x') RETURNING id`,
	).Scan(&playerID), "seed player")

	raider := hackRaider{
		tx: database.NewTxManager(pool),
		trade: trade.NewPoolRepo(
			traderepo.New(pool), playersrepo.New(pool), cargorepo.New(pool)),
	}
	return pool, raider, domain.PlayerID(playerID)
}

// resetRaiderWorld gives each subtest the clean slate its absolute row counts
// need: no containers, no cargo, and the target good back at its 47% fill with
// every other good on the station too poor to be picked instead.
func resetRaiderWorld(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `TRUNCATE containers, cargo`)
	require.NoError(t, err, "reset containers/cargo")
	_, err = pool.Exec(ctx, `UPDATE station_goods SET stock = 100 WHERE owner_kind = 2 AND owner_id = $1`,
		raiderStationID)
	require.NoError(t, err, "impoverish the other goods")
	_, err = pool.Exec(ctx,
		`UPDATE station_goods SET stock = $1, max_stock = 10000
		 WHERE owner_kind = 2 AND owner_id = $2 AND goods_type_id = $3`,
		raiderStock, raiderStationID, raiderGoods)
	require.NoError(t, err, "seed the target good")
}

// newHackerShip inserts a ship with the given hold size and returns its ref.
func newHackerShip(t *testing.T, pool *pgxpool.Pool, owner domain.PlayerID, cargobay int64) domain.EntityRef {
	t.Helper()
	var shipID int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, cargobay) VALUES ($1, $2, $3) RETURNING id`,
		int64(owner), int64(raiderSectorID), cargobay).Scan(&shipID), "seed hacker ship")
	return domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}
}

func raiderStationStock(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var stock int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT stock FROM station_goods WHERE owner_kind = 2 AND owner_id = $1 AND goods_type_id = $2`,
		raiderStationID, raiderGoods).Scan(&stock), "read station stock")
	return stock
}

// containerCargo reports how many units the container holds.
func containerCargo(t *testing.T, pool *pgxpool.Pool, id domain.ContainerID) int64 {
	t.Helper()
	var qty int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(quantity), 0) FROM cargo WHERE owner_kind = $1 AND owner_id = $2`,
		int16(domain.EntityKindContainer), int64(id)).Scan(&qty), "read container cargo")
	return qty
}

func raiderLoot() sector.LootDrop {
	return sector.LootDrop{
		SectorID:  raiderSectorID,
		Pos:       domain.Vec2{X: 120, Y: -40},
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
}

// TestIntegration_HackRaider drives the real raider against Postgres. The cases
// are sequential subtests over ONE container (see raiderFixture); each resets the
// world first so its absolute row counts mean what they say.
func TestIntegration_HackRaider(t *testing.T) {
	pool, raider, owner := raiderFixture(t)
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: raiderStationID}

	// The level-1 raid: the loot never touches the hold, so the container is
	// created — and it is created on the raid's own transaction (single-connection
	// pool), together with the stock deduction.
	t.Run("StockAndLootContainerShareOneCommit", func(t *testing.T) {
		ctx := installCtx(t)
		resetRaiderWorld(t, pool)
		ship := newHackerShip(t, pool, owner, 0)

		out, container, err := raider.Rob(ctx, station, ship,
			raiderRobFrac, raiderDamageFrac, raiderMinFrac, false, raiderLoot())
		require.NoError(t, err)
		assert.EqualValues(t, raiderGoods, out.GoodsType)
		assert.Equal(t, raiderRobbed, out.Robbed)
		assert.Equal(t, raiderDamaged, out.Damaged)
		assert.False(t, out.Delivered, "up_hack level 1 never deposits to the hold")

		require.NotNil(t, container, "undelivered loot must come back as a container")
		assert.Equal(t, raiderSectorID, container.SectorID)
		assert.Equal(t, domain.Vec2{X: 120, Y: -40}, container.Pos)

		// Both halves are committed and consistent: the goods that left the shelf
		// are the goods in the container.
		assert.Equal(t, raiderStock-raiderRobbed-raiderDamaged, raiderStationStock(t, pool))
		assert.Equal(t, 1, rowCount(t, pool, "containers"), "exactly one loot container")
		assert.Equal(t, raiderRobbed, containerCargo(t, pool, container.ID))
	})

	// The ordering the two-transaction design used to lose goods on: the container
	// INSERT fails AFTER the stock deduction already ran. The rollback must put the
	// goods back on the shelf.
	t.Run("FailedContainerInsertKeepsStock", func(t *testing.T) {
		ctx := installCtx(t)
		resetRaiderWorld(t, pool)
		ship := newHackerShip(t, pool, owner, 0)
		plantContainerIDCollision(t, pool)

		_, container, err := raider.Rob(ctx, station, ship,
			raiderRobFrac, raiderDamageFrac, raiderMinFrac, false, raiderLoot())
		require.Error(t, err, "the planted id makes the container INSERT violate the primary key")
		assert.ErrorContains(t, err, "spawn loot container",
			"the failure must land in the container step, i.e. after the stock was already deducted")
		assert.Nil(t, container)

		assert.Equal(t, raiderStock, raiderStationStock(t, pool),
			"the stock deduction rolled back with the container that could not hold it")
		assert.Equal(t, 1, rowCount(t, pool, "containers"), "only the planted row is there")
	})

	// up_hack level 2 with a hold that fits the loot: the goods go straight to the
	// ship and NO container is created.
	t.Run("DeliveredLootCreatesNoContainer", func(t *testing.T) {
		ctx := installCtx(t)
		resetRaiderWorld(t, pool)
		ship := newHackerShip(t, pool, owner, 1_000_000)

		out, container, err := raider.Rob(ctx, station, ship,
			raiderRobFrac, raiderDamageFrac, raiderMinFrac, true, raiderLoot())
		require.NoError(t, err)
		assert.True(t, out.Delivered, "a roomy hold takes the loot")
		assert.Nil(t, container)
		assert.Equal(t, 0, rowCount(t, pool, "containers"), "delivered loot drops nothing")
		assert.Equal(t, raiderRobbed, heldQty(t, pool, ship, domain.GoodsTypeID(raiderGoods)))
	})
}

// plantContainerIDCollision makes the NEXT containers INSERT fail with a
// duplicate primary key: it inserts a row and rewinds the BIGSERIAL sequence so
// the next nextval hands out that same id again. containers has no foreign key to
// lean on (migration 0020 keeps sector_id plain), so this is how the test reaches
// a failure that lands *after* the stock deduction inside the same transaction —
// the same role installerBadSectorID plays for the installs.
func plantContainerIDCollision(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var planted int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO containers (sector_id, expires_at) VALUES ($1, NOW() + INTERVAL '1 hour') RETURNING id`,
		int64(raiderSectorID)).Scan(&planted), "plant container")
	_, err := pool.Exec(ctx, `SELECT setval('containers_id_seq', $1, false)`, planted)
	require.NoError(t, err, "rewind container sequence")
}
