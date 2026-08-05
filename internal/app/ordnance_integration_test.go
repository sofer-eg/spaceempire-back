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

	missileGoods = domain.GoodsTypeID(10) // Ракета Москит
	droneGoods   = domain.GoodsTypeID(21) // Боевой дрон
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
	//
	// cargobay is spelled out rather than left on the 0006 default of 100, for the
	// same reason installerFixture spells out 2000: the recall's capacity gate sizes
	// the credit against ships.cargobay, and a combat drone takes 290 units of space
	// (TASK-167 put the ammunition back on the real catalog). 2000 is the smallest
	// round hold that leaves room for the four-drone recalls below — the kind of ship
	// that carries drones in the first place.
	var shipID int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, hp, shield, cargobay) VALUES ($1, $2, 100, 100, 2000) RETURNING id`,
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

// usedSpace is the physical space the hold's stacks occupy — the quantity the
// capacity check compares against cargobay.
func usedSpace(t *testing.T, pool *pgxpool.Pool, hold domain.EntityRef) float64 {
	t.Helper()
	var used float64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(c.quantity * g.space), 0) FROM cargo c
		 JOIN goods_types g ON g.id = c.goods_type_id
		 WHERE c.owner_kind = $1 AND c.owner_id = $2`,
		int16(hold.Kind), hold.ID).Scan(&used), "read used space")
	return used
}

// clearHold drops one goods stack, standing in for the player selling it.
func clearHold(t *testing.T, pool *pgxpool.Pool, hold domain.EntityRef, gtype domain.GoodsTypeID) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`DELETE FROM cargo WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3`,
		int16(hold.Kind), hold.ID, int32(gtype))
	require.NoError(t, err, "clear hold")
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

	// The salvo is SIZED BY THE HOLD inside the transaction (TASK-176) and what it
	// launches is all-or-nothing. Three drones against two units in the hold used to
	// fail outright with ErrInsufficientQuantity — one all-or-nothing debit of the
	// whole clamped salvo (TASK-147) — which at space 290 refused the ordinary case:
	// a hull that can carry drones carries single digits of them, so the canvas menu
	// (which sends the full salvo, unlike the HUD) answered 400 for a launch the
	// player could see they could make.
	//
	// Reformulated invariant: the debit and the INSERTs still commit together, for
	// exactly the number of drones that flew — charged units, committed rows and
	// returned ids are the same number, and nothing beyond it is charged.
	t.Run("DroneSalvoIsSizedByTheHold", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, droneGoods, 2)

		ids, err := ord.LaunchDrones(ctx, hold, droneGoods, testDrones(3, ship, player, ordnanceSectorID))
		require.NoError(t, err, "3 requested, 2 in the hold: launch what the hold pays for")
		require.Len(t, ids, 2, "one id per drone that actually flew")
		assert.Equal(t, 2, rowCount(t, pool, "drones"), "exactly the launched drones exist")
		assert.Zero(t, heldQty(t, pool, hold, droneGoods), "both units are paid for, the third was never there")

		got, err := dronesrepo.New(pool).LoadAll(ctx, ordnanceSectorID)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, ids, []domain.DroneID{got[0].ID, got[1].ID}, "the returned ids are the committed rows")

		// A single unit aboard launches exactly one drone and charges exactly one —
		// the case the SPA used to have to pre-clamp for (and only did in one of its
		// two entry points).
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, droneGoods, 1)

		ids, err = ord.LaunchDrones(ctx, hold, droneGoods, testDrones(3, ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.Len(t, ids, 1, "the hold is the salvo")
		assert.Equal(t, 1, rowCount(t, pool, "drones"))
		assert.Zero(t, heldQty(t, pool, hold, droneGoods), "one unit charged, not three")

		// An EMPTY hold is still a refusal, so the handler still answers 400 rather
		// than a cheerful "spawned 0" (see also EmptyHoldLaunchesNothing).
		ids, err = ord.LaunchDrones(ctx, hold, droneGoods, testDrones(3, ship, player, ordnanceSectorID))
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity, "nothing aboard is not a partial launch")
		assert.Empty(t, ids)
		assert.Equal(t, 1, rowCount(t, pool, "drones"), "no free drone")
	})

	// The recall is the launch run backwards (TASK-152): the DELETEs and the credit
	// commit together, and the credit counts rows actually deleted. An id whose row
	// is already gone is a no-op worth nothing — that is what stops a retry after an
	// ambiguous COMMIT-in-flight deadline from paying the player twice.
	t.Run("RecallCreditsDeletedRowsOnly", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, droneGoods, 2)

		ids, err := ord.LaunchDrones(ctx, hold, droneGoods, testDrones(2, ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.Len(t, ids, 2)
		require.Zero(t, heldQty(t, pool, hold, droneGoods))

		out, err := ord.RecallDrones(ctx, hold, droneGoods, ids)
		require.NoError(t, err)
		assert.Equal(t, 2, out.Credited, "one unit per deleted row")
		assert.ElementsMatch(t, ids, out.Removed, "both rows settled")
		assert.Equal(t, 0, rowCount(t, pool, "drones"), "both rows deleted")
		assert.EqualValues(t, 2, heldQty(t, pool, hold, droneGoods), "both units credited")

		// The same recall again: the rows are gone, so it credits nothing and does
		// not fail — the ship would otherwise be permanently unable to recall. The
		// ids are still reported as settled so the worker can clear the ghosts.
		out, err = ord.RecallDrones(ctx, hold, droneGoods, ids)
		require.NoError(t, err)
		assert.Zero(t, out.Credited)
		assert.ElementsMatch(t, ids, out.Removed, "ghost rows still leave RAM")
		assert.EqualValues(t, 2, heldQty(t, pool, hold, droneGoods), "no double credit")

		// Mixed: one live row among stale ids credits exactly one.
		live, err := ord.LaunchDrones(ctx, hold, droneGoods, testDrones(1, ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.EqualValues(t, 1, heldQty(t, pool, hold, droneGoods))

		out, err = ord.RecallDrones(ctx, hold, droneGoods, append(ids, live...))
		require.NoError(t, err)
		assert.Equal(t, 1, out.Credited)
		assert.Equal(t, 0, rowCount(t, pool, "drones"))
		assert.EqualValues(t, 2, heldQty(t, pool, hold, droneGoods))
	})

	// TASK-156, the exploit and its fix: launch drones, fill the freed space with
	// other goods, recall. The credit used to be unconditional, so those units
	// landed on top of a full hold and the ship carried more than its cargobay —
	// repeatable up to the drone count. Now the recall is sized by what fits: one
	// drone comes home, the other keeps flying, and the hold never goes over.
	//
	// The fixture ship's cargobay is 2000 and a drone unit takes 290, so 1710 missiles
	// (space 1 each) leave room for exactly one drone.
	t.Run("RecallStopsAtHoldCapacity", func(t *testing.T) {
		ctx := installCtx(t)
		resetOrdnanceTables(t, pool)
		stockHold(t, pool, hold, droneGoods, 2)

		ids, err := ord.LaunchDrones(ctx, hold, droneGoods, testDrones(2, ship, player, ordnanceSectorID))
		require.NoError(t, err)
		require.Len(t, ids, 2)
		require.Zero(t, heldQty(t, pool, hold, droneGoods), "the salvo emptied the drone stack")

		// The ship docks and fills the space its drones left behind.
		stockHold(t, pool, hold, missileGoods, 1710)
		require.EqualValues(t, 1710, usedSpace(t, pool, hold))

		out, err := ord.RecallDrones(ctx, hold, droneGoods, ids)
		require.NoError(t, err)
		assert.Equal(t, 1, out.Credited, "only what fits is credited")
		require.Len(t, out.Removed, 1, "only the credited drone's row is deleted")
		assert.Equal(t, 1, rowCount(t, pool, "drones"), "the other drone keeps flying")
		assert.EqualValues(t, 1, heldQty(t, pool, hold, droneGoods))
		assert.EqualValues(t, 2000, usedSpace(t, pool, hold), "at capacity, not over it")

		// A completely full hold recalls nothing and still does not fail: the drone
		// is not stranded, it is waiting.
		stockHold(t, pool, hold, torpedoGoods, 1)
		require.Greater(t, usedSpace(t, pool, hold), float64(2000), "the hold is now overfull by other means")
		left := []domain.DroneID{ids[0], ids[1]}
		out, err = ord.RecallDrones(ctx, hold, droneGoods, left)
		require.NoError(t, err, "a full hold is not an error")
		assert.Zero(t, out.Credited)
		assert.Empty(t, out.Removed, "nothing settled → the drone stays on the radar")
		assert.Equal(t, 1, rowCount(t, pool, "drones"))

		// The player sells the cargo and recalls again: the last drone comes home,
		// so no state leaves a ship permanently unable to recall (AC#3).
		clearHold(t, pool, hold, missileGoods)
		clearHold(t, pool, hold, torpedoGoods)
		out, err = ord.RecallDrones(ctx, hold, droneGoods, left)
		require.NoError(t, err)
		assert.Equal(t, 1, out.Credited, "the freed space brings the last drone back")
		assert.Equal(t, 0, rowCount(t, pool, "drones"))
		assert.EqualValues(t, 2, heldQty(t, pool, hold, droneGoods), "both units are home, exactly once each")
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
