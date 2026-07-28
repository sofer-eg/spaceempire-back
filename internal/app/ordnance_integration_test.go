package app

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	dronesrepo "spaceempire/back/internal/persistence/drones"
	torpedosrepo "spaceempire/back/internal/persistence/torpedos"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
)

// These tests exercise the real ordnance against Postgres, for the same reason
// TestIntegration_StaticInstaller exists: the atomicity invariant (ammunition
// debit + projectile INSERTs in ONE transaction) is invisible to the unit suite,
// where sector's fakeOrdnance is atomic by construction — swapping
// WithExecutor(tx) for a pool-bound repository keeps every TestUnit_ green.
//
// ordnanceBadShipID is not a ships row, so a drones/torpedos INSERT naming it
// violates owner_ship_id's foreign key. That is how we force a failure *after*
// the debit has already been applied inside the transaction. (sector_id carries
// no FK on those two tables, unlike jammers/satellites.)
const (
	ordnanceSectorID  = domain.SectorID(2)
	ordnanceBadShipID = domain.ShipID(999999)

	missileGoods = domain.GoodsTypeID(50) // Ракета
	droneGoods   = domain.GoodsTypeID(51) // Боевой дрон
	torpedoGoods = domain.GoodsTypeID(23) // Огненная Буря (class 2)
)

// ordnanceFixture spins up a schema-migrated Postgres and returns the real
// ordnance over it, plus the launching ship (its own cargo hold) and its player.
//
// The pool is capped at ONE connection, exactly as installerFixture does and for
// the same reason: the ordnance must do all of its work on the caller's
// transaction, which holds that single connection for its whole lifetime. A
// repository bound to the pool would have to acquire a second connection and would
// block until the context expires — making "the projectile INSERT rides the
// caller's transaction" an observable property instead of an implementation
// detail.
//
// Called ONCE for the whole group (sequential subtests); resetOrdnanceTables gives
// each subtest the clean slate its absolute row counts need.
func ordnanceFixture(t *testing.T) (*pgxpool.Pool, ordnance, domain.EntityRef, domain.ShipID, domain.PlayerID) {
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
		`INSERT INTO players (login, password_hash) VALUES ('gunner', 'x') RETURNING id`,
	).Scan(&playerID), "seed player")

	// A real ships row: drones.owner_ship_id / torpedos.owner_ship_id reference it.
	var shipID int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, hp, shield) VALUES ($1, $2, 100, 100) RETURNING id`,
		playerID, int64(ordnanceSectorID),
	).Scan(&shipID), "seed ship")

	ord := ordnance{
		tx:       database.NewTxManager(pool),
		cargo:    cargorepo.New(pool),
		drones:   dronesrepo.New(pool),
		torpedos: torpedosrepo.New(pool),
	}
	hold := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}
	return pool, ord, hold, domain.ShipID(shipID), domain.PlayerID(playerID)
}

// resetOrdnanceTables clears everything the previous subtest wrote, so each one
// starts from an empty world and can assert absolute row counts ("exactly two
// drones") — the assertions that catch a repository bound to the pool instead of
// the caller's transaction.
func resetOrdnanceTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `TRUNCATE drones, torpedos, cargo`)
	require.NoError(t, err, "reset ordnance tables")
}

// testTorpedo is a launch-shaped class-2 torpedo owned by the fixture's ship.
func testTorpedo(ship domain.ShipID, player domain.PlayerID, sectorID domain.SectorID) domain.Torpedo {
	return domain.Torpedo{
		SectorID: sectorID, OwnerShipID: ship, PlayerID: player,
		Pos:       domain.Vec2{X: 10, Y: 20},
		Direction: domain.Vec2{X: 1, Y: 0},
		Target:    domain.EntityRef{Kind: domain.EntityKindShip, ID: 4242},
		Class:     2, Damage: 150, Speed: 30, Accel: 15, TurnRate: 1,
		HitRadius: 14, SplashRadius: 40, HP: 20,
		ExpiresAt: time.Now().Add(30 * time.Second).UTC(),
	}
}

// testDrones is a salvo of n launch-shaped drones owned by the fixture's ship.
func testDrones(n int, ship domain.ShipID, player domain.PlayerID, sectorID domain.SectorID) []domain.Drone {
	ds := make([]domain.Drone, n)
	for i := range ds {
		ds[i] = domain.Drone{
			SectorID: sectorID, OwnerShipID: ship, PlayerID: player,
			Pos:       domain.Vec2{X: float64(i), Y: 0},
			Direction: domain.Vec2{X: 1, Y: 0},
			Target:    domain.EntityRef{Kind: domain.EntityKindShip, ID: 4242},
			HP:        20, Damage: 5,
			ExpiresAt: time.Now().Add(120 * time.Second).UTC(),
		}
	}
	return ds
}

// TestIntegration_Ordnance drives the real ordnance against Postgres. The cases
// are sequential subtests over ONE container (see ordnanceFixture); each starts by
// truncating so its absolute row counts mean what they say, and each gets its own
// deadline (installCtx, shared with the installer group).
func TestIntegration_Ordnance(t *testing.T) {
	pool, ord, hold, ship, player := ordnanceFixture(t)

	// EmptyHold: with nothing in the hold every launch rolls back — no projectile
	// row, no cargo movement, and the caller sees cargo.ErrInsufficientQuantity so
	// the handler can answer 400.
	t.Run("EmptyHoldLaunchesNothing", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)

		require.ErrorIs(t, ord.SpendMissile(ctx, hold, missileGoods), cargo.ErrInsufficientQuantity)

		tid, err := ord.LaunchTorpedo(ctx, hold, torpedoGoods, testTorpedo(ship, player, ordnanceSectorID))
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Zero(t, tid)

		ids, err := ord.LaunchDrones(ctx, hold, droneGoods, testDrones(2, ship, player, ordnanceSectorID))
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Empty(t, ids)

		assert.Equal(t, 0, rowCount(t, pool, "torpedos"), "no torpedo for an empty hold")
		assert.Equal(t, 0, rowCount(t, pool, "drones"), "no drones for an empty hold")
	})

	// A missile has no row of its own, so the whole observable effect is the debit
	// — and it must be exactly one unit per launch.
	t.Run("MissileChargesExactlyOne", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, missileGoods, 2)

		require.NoError(t, ord.SpendMissile(ctx, hold, missileGoods))
		assert.EqualValues(t, 1, heldQty(t, pool, hold, missileGoods), "one unit per launch")

		require.NoError(t, ord.SpendMissile(ctx, hold, missileGoods))
		assert.Zero(t, heldQty(t, pool, hold, missileGoods))

		// The magazine is empty now: the third launch is refused.
		require.ErrorIs(t, ord.SpendMissile(ctx, hold, missileGoods), cargo.ErrInsufficientQuantity)
	})

	// One unit in the hold yields exactly one committed torpedo row and an empty
	// stack, both from the same transaction.
	t.Run("TorpedoChargesExactlyOne", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, torpedoGoods, 1)

		id, err := ord.LaunchTorpedo(ctx, hold, torpedoGoods, testTorpedo(ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.NotZero(t, id, "DB-assigned id")

		assert.Equal(t, 1, rowCount(t, pool, "torpedos"), "exactly one torpedo")
		assert.Zero(t, heldQty(t, pool, hold, torpedoGoods), "the unit is paid for")

		// The row is the one we asked for, and it is committed (a plain pool read).
		got, err := torpedosrepo.New(pool).LoadAll(ctx, ordnanceSectorID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, id, got[0].ID)
		assert.Equal(t, ship, got[0].OwnerShipID)
		assert.Equal(t, 2, got[0].Class)

		// A second launch on the now-empty hold is refused and adds nothing.
		_, err = ord.LaunchTorpedo(ctx, hold, torpedoGoods, testTorpedo(ship, player, ordnanceSectorID))
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Equal(t, 1, rowCount(t, pool, "torpedos"), "no free second torpedo")
	})

	// The salvo is all-or-nothing: three drones against two units in the hold
	// leaves nothing behind, and the clamped two go through together.
	t.Run("DroneSalvoIsAllOrNothing", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, droneGoods, 2)

		ids, err := ord.LaunchDrones(ctx, hold, droneGoods, testDrones(3, ship, player, ordnanceSectorID))
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity, "3 requested, 2 in the hold")
		assert.Empty(t, ids)
		assert.Equal(t, 0, rowCount(t, pool, "drones"), "no partial salvo")
		assert.EqualValues(t, 2, heldQty(t, pool, hold, droneGoods), "hold untouched")

		ids, err = ord.LaunchDrones(ctx, hold, droneGoods, testDrones(2, ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.Len(t, ids, 2, "one id per drone, in order")
		assert.Equal(t, 2, rowCount(t, pool, "drones"), "exactly two drones")
		assert.Zero(t, heldQty(t, pool, hold, droneGoods), "both units are paid for")

		got, err := dronesrepo.New(pool).LoadAll(ctx, ordnanceSectorID)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, ids, []domain.DroneID{got[0].ID, got[1].ID}, "the returned ids are the committed rows")
	})

	// The INSERT is rejected (owner_ship_id violates the foreign key) *after* the
	// debit ran inside the same transaction. The rollback must give the ammunition
	// back — a failed insert must never swallow it.
	t.Run("FailedInsertKeepsAmmunition", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, torpedoGoods, 1)
		stockHold(t, pool, hold, droneGoods, 2)

		_, err := ord.LaunchTorpedo(ctx, hold, torpedoGoods, testTorpedo(ordnanceBadShipID, player, ordnanceSectorID))
		require.Error(t, err, "unknown ship violates torpedos.owner_ship_id")
		assert.Equal(t, 0, rowCount(t, pool, "torpedos"))
		assert.EqualValues(t, 1, heldQty(t, pool, hold, torpedoGoods), "the debit rolled back with the insert")

		_, err = ord.LaunchDrones(ctx, hold, droneGoods, testDrones(2, ordnanceBadShipID, player, ordnanceSectorID))
		require.Error(t, err)
		assert.Equal(t, 0, rowCount(t, pool, "drones"))
		assert.EqualValues(t, 2, heldQty(t, pool, hold, droneGoods))

		// The hold is intact, so a retry with a real ship still works.
		id, err := ord.LaunchTorpedo(ctx, hold, torpedoGoods, testTorpedo(ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.NotZero(t, id)
		assert.Zero(t, heldQty(t, pool, hold, torpedoGoods))
	})
}
