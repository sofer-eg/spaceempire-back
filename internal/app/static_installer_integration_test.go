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
	jammersrepo "spaceempire/back/internal/persistence/jammers"
	satellitesrepo "spaceempire/back/internal/persistence/satellites"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
)

// These tests exercise the real staticInstaller against Postgres, because the
// atomicity invariant it implements (goods debit + object INSERT in ONE
// transaction) is invisible to the unit suite: sector's fakeStaticInstaller is
// atomic by construction, so swapping WithExecutor(tx) for the pool-bound
// repository keeps every TestUnit_ green.
//
// installerSectorID / installerBadSectorID: sector 2 exists (migration 0034) and
// carries no seeded statics; 999999 does not exist, so a jammers/satellites
// INSERT naming it violates the sector_id foreign key — that is how we force a
// failure *after* the debit has already been applied inside the transaction.
const (
	installerSectorID    = domain.SectorID(2)
	installerBadSectorID = domain.SectorID(999999)

	jammerGoods    = domain.GoodsTypeID(27) // Генератор гипер-помех
	satelliteGoods = domain.GoodsTypeID(26) // Навигационный спутник
	// batteryGoods is the filler the dismantle-without-room case uses: space 1,
	// so a round number of them fills a hold exactly.
	batteryGoods = domain.GoodsTypeID(1)
)

// installerFixture spins up a schema-migrated Postgres and returns the real
// staticInstaller over it, plus the installing ship's cargo hold and the owning
// player.
//
// The pool is capped at ONE connection on purpose. The installer must do all of
// its work on the caller's transaction, which holds that single connection for
// its whole lifetime; a repository bound to the pool instead of the tx would
// have to acquire a second connection and would block until the context expires.
// That makes "the object INSERT rides the caller's transaction" an observable
// property rather than an implementation detail — the mutation the unit suite
// cannot see fails here.
//
// Called ONCE for the whole group (the cases below are sequential subtests, not
// top-level tests). testdb.Setup starts a container and runs every migration, so
// a fixture per case meant four container starts and four goose runs in this
// package — load that also slows the cold start
// TestIntegration_App_StartsAndShutsDownGracefully waits out (see healthzBudget
// there). One shared container makes this group ~4x faster; resetInstallerTables
// gives each subtest the clean slate the absolute row counts need.
func installerFixture(t *testing.T) (*pgxpool.Pool, staticInstaller, domain.EntityRef, domain.PlayerID) {
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
		`INSERT INTO players (login, password_hash) VALUES ('installer', 'x') RETURNING id`,
	).Scan(&playerID), "seed player")

	inst := staticInstaller{
		tx:         database.NewTxManager(pool),
		cargo:      cargorepo.New(pool),
		jammers:    jammersrepo.New(pool),
		satellites: satellitesrepo.New(pool),
	}
	// A real ships row, with a hold big enough to carry either object: the install
	// path never reads the ship (cargo.owner_id carries no foreign key), but the
	// dismantle does — it checks the hold has room for the object coming back, and
	// that capacity is ships.cargobay. 2000 fits a generator (space 535) and a
	// satellite (375) with room to spare, which is what a ship deploying them would
	// need in the first place.
	var shipID int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, cargobay) VALUES ($1, $2, 2000) RETURNING id`,
		playerID, int64(installerSectorID),
	).Scan(&shipID), "seed installing ship")
	hold := domain.EntityRef{Kind: domain.EntityKindShip, ID: shipID}
	return pool, inst, hold, domain.PlayerID(playerID)
}

// stockHold seeds qty units of gtype into the hold as unowned goods
// (goods_owner_id 0), which is how ship holds are always recorded.
func stockHold(t *testing.T, pool *pgxpool.Pool, hold domain.EntityRef, gtype domain.GoodsTypeID, qty int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO cargo (owner_kind, owner_id, goods_type_id, quantity, goods_owner_id)
		 VALUES ($1, $2, $3, $4, 0)`,
		int16(hold.Kind), hold.ID, int32(gtype), qty)
	require.NoError(t, err, "seed hold")
}

// heldQty reports how many units of gtype the hold still carries (0 when the
// stack was consumed away or never existed).
func heldQty(t *testing.T, pool *pgxpool.Pool, hold domain.EntityRef, gtype domain.GoodsTypeID) int64 {
	t.Helper()
	var qty int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(quantity), 0) FROM cargo
		 WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3`,
		int16(hold.Kind), hold.ID, int32(gtype)).Scan(&qty), "read hold")
	return qty
}

// clearInstallerHold drops one goods stack, standing in for the player selling it.
func clearInstallerHold(t *testing.T, pool *pgxpool.Pool, hold domain.EntityRef, gtype domain.GoodsTypeID) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`DELETE FROM cargo WHERE owner_kind = $1 AND owner_id = $2 AND goods_type_id = $3`,
		int16(hold.Kind), hold.ID, int32(gtype))
	require.NoError(t, err, "clear hold")
}

func rowCount(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	//nolint:gosec // table is a test-local literal, not user input
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n))
	return n
}

// resetInstallerTables clears everything the previous subtest wrote, so each one
// starts from an empty world and can assert absolute row counts ("exactly one
// generator") — the assertions that catch a repository bound to the pool instead
// of the caller's transaction.
func resetInstallerTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `TRUNCATE jammers, satellites, cargo`)
	require.NoError(t, err, "reset installer tables")
}

// installCtx bounds one subtest's DB work. Called per subtest, never once for the
// group: the deadline is absolute, so a shared context would let the case that
// spends it fail the ones after it with a stale ctx error instead of their own
// signal — and would put every case under one budget for no reason.
//
// Mirrors the worker's Config.RepoTimeout in spirit: the install always runs under
// a deadline, and a mutation that needs a second connection hits it instead of
// hanging the test.
func installCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestIntegration_StaticInstaller drives the real installer against Postgres.
// The cases are sequential subtests over ONE container (see installerFixture);
// each starts by truncating so its absolute row counts mean what they say, and
// each gets its own deadline (see installCtx).
func TestIntegration_StaticInstaller(t *testing.T) {
	pool, inst, hold, owner := installerFixture(t)

	// EmptyHold: with nothing in the hold the transaction rolls back — no
	// jammer/satellite row, no cargo movement, and the caller sees
	// cargo.ErrInsufficientQuantity so the handler can answer 400.
	t.Run("EmptyHoldInstallsNothing", func(t *testing.T) {
		ctx := installCtx(t)
		resetInstallerTables(t, pool)

		jamID, err := inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Zero(t, jamID)

		satID, err := inst.InstallSatellite(ctx, hold, satelliteGoods, domain.Satellite{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Zero(t, satID)

		assert.Equal(t, 0, rowCount(t, pool, "jammers"), "no generator for an empty hold")
		assert.Equal(t, 0, rowCount(t, pool, "satellites"), "no satellite for an empty hold")
		assert.Zero(t, heldQty(t, pool, hold, jammerGoods))
		assert.Zero(t, heldQty(t, pool, hold, satelliteGoods))
	})

	// One unit in the hold yields exactly one row and an empty stack, both
	// committed together.
	t.Run("JammerChargesExactlyOne", func(t *testing.T) {
		ctx := installCtx(t)
		resetInstallerTables(t, pool)
		stockHold(t, pool, hold, jammerGoods, 1)

		id, err := inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID:  &owner,
			SectorID: installerSectorID,
			Pos:      domain.Vec2{X: 30, Y: -40},
			Race:     2,
			Built:    true,
			HP:       7500, Shield: 4000, MaxShield: 4000, ShieldRecharge: 20,
		})
		require.NoError(t, err)
		require.NotZero(t, id, "DB-assigned id")

		assert.Equal(t, 1, rowCount(t, pool, "jammers"), "exactly one generator")
		assert.Zero(t, heldQty(t, pool, hold, jammerGoods), "the unit is paid for")

		// The row is the one we asked for, and it is committed (a plain pool read).
		got, err := jammersrepo.New(pool).LoadAll(ctx, installerSectorID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, id, got[0].ID)
		assert.Equal(t, domain.Vec2{X: 30, Y: -40}, got[0].Pos)
		require.NotNil(t, got[0].OwnerID)
		assert.Equal(t, owner, *got[0].OwnerID)

		// A second install on the now-empty hold is refused and adds nothing.
		_, err = inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Equal(t, 1, rowCount(t, pool, "jammers"), "no free second generator")
	})

	// Mirrors the jammer case for install-satellite (same code path, same
	// invariant).
	t.Run("SatelliteChargesExactlyOne", func(t *testing.T) {
		ctx := installCtx(t)
		resetInstallerTables(t, pool)
		stockHold(t, pool, hold, satelliteGoods, 1)

		id, err := inst.InstallSatellite(ctx, hold, satelliteGoods, domain.Satellite{
			OwnerID:  &owner,
			SectorID: installerSectorID,
			Pos:      domain.Vec2{X: -12, Y: 8},
			Race:     2,
			Built:    true,
			HP:       5000, Shield: 2000, MaxShield: 2000, ShieldRecharge: 20,
		})
		require.NoError(t, err)
		require.NotZero(t, id)

		assert.Equal(t, 1, rowCount(t, pool, "satellites"), "exactly one satellite")
		assert.Zero(t, heldQty(t, pool, hold, satelliteGoods), "the unit is paid for")

		got, err := satellitesrepo.New(pool).LoadAll(ctx, installerSectorID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, id, got[0].ID)
		assert.Equal(t, domain.Vec2{X: -12, Y: 8}, got[0].Pos)

		_, err = inst.InstallSatellite(ctx, hold, satelliteGoods, domain.Satellite{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
		assert.Equal(t, 1, rowCount(t, pool, "satellites"), "no free second satellite")
	})

	// TASK-146: the dismantle is the install read backwards. The row goes away and
	// the goods unit comes back in ONE transaction, on the caller's connection (the
	// single-connection pool proves the ride-along here too).
	t.Run("DismantleReturnsTheUnitAndDeletesTheRow", func(t *testing.T) {
		ctx := installCtx(t)
		resetInstallerTables(t, pool)
		stockHold(t, pool, hold, jammerGoods, 1)
		stockHold(t, pool, hold, satelliteGoods, 1)

		jamID, err := inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.NoError(t, err)
		satID, err := inst.InstallSatellite(ctx, hold, satelliteGoods, domain.Satellite{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.NoError(t, err)
		require.Zero(t, heldQty(t, pool, hold, jammerGoods), "both units are paid for")
		require.Zero(t, heldQty(t, pool, hold, satelliteGoods))

		require.NoError(t, inst.DismantleJammer(ctx, hold, jammerGoods, jamID))
		assert.Equal(t, 0, rowCount(t, pool, "jammers"), "the generator row is gone")
		assert.EqualValues(t, 1, heldQty(t, pool, hold, jammerGoods), "and its unit is back in the hold")

		require.NoError(t, inst.DismantleSatellite(ctx, hold, satelliteGoods, satID))
		assert.Equal(t, 0, rowCount(t, pool, "satellites"))
		assert.EqualValues(t, 1, heldQty(t, pool, hold, satelliteGoods))

		// The unit is back, so it can be redeployed — install/dismantle round-trips.
		_, err = inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, rowCount(t, pool, "jammers"))
		assert.Zero(t, heldQty(t, pool, hold, jammerGoods))
	})

	// A hold with no room refuses the dismantle whole: one object is indivisible, so
	// the alternative would be either overfilling the hold (the TASK-156 exploit
	// under another name) or destroying a ≈1.13M cr object. The generator stays
	// deployed and the transaction rolls back.
	t.Run("DismantleWithoutRoomKeepsTheObject", func(t *testing.T) {
		ctx := installCtx(t)
		resetInstallerTables(t, pool)
		stockHold(t, pool, hold, jammerGoods, 1)

		jamID, err := inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.NoError(t, err)

		// The hold is 2000 and the generator takes 535, so 1700 batteries (space 1)
		// leave 300 free — not enough for it to come back.
		stockHold(t, pool, hold, batteryGoods, 1700)

		err = inst.DismantleJammer(ctx, hold, jammerGoods, jamID)
		require.ErrorIs(t, err, cargo.ErrNoSpace)
		assert.Equal(t, 1, rowCount(t, pool, "jammers"), "still deployed")
		assert.Zero(t, heldQty(t, pool, hold, jammerGoods), "nothing credited")

		// Room appears and the same call goes through.
		clearInstallerHold(t, pool, hold, batteryGoods)
		require.NoError(t, inst.DismantleJammer(ctx, hold, jammerGoods, jamID))
		assert.Equal(t, 0, rowCount(t, pool, "jammers"))
		assert.EqualValues(t, 1, heldQty(t, pool, hold, jammerGoods))
	})

	// The INSERT is rejected (sector_id violates the foreign key) *after* the debit
	// ran inside the same transaction. The rollback must give the goods back — the
	// ≈1.13M cr generator must never be swallowed by a failed insert.
	t.Run("FailedInsertKeepsGoods", func(t *testing.T) {
		ctx := installCtx(t)
		resetInstallerTables(t, pool)
		stockHold(t, pool, hold, jammerGoods, 1)
		stockHold(t, pool, hold, satelliteGoods, 1)

		_, err := inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerBadSectorID, Built: true,
		})
		require.Error(t, err, "unknown sector violates jammers.sector_id")
		assert.Equal(t, 0, rowCount(t, pool, "jammers"))
		assert.EqualValues(t, 1, heldQty(t, pool, hold, jammerGoods), "the debit rolled back with the insert")

		_, err = inst.InstallSatellite(ctx, hold, satelliteGoods, domain.Satellite{
			OwnerID: &owner, SectorID: installerBadSectorID, Built: true,
		})
		require.Error(t, err)
		assert.Equal(t, 0, rowCount(t, pool, "satellites"))
		assert.EqualValues(t, 1, heldQty(t, pool, hold, satelliteGoods))

		// The hold is intact, so a retry against a real sector still works.
		id, err := inst.InstallJammer(ctx, hold, jammerGoods, domain.Jammer{
			OwnerID: &owner, SectorID: installerSectorID, Built: true,
		})
		require.NoError(t, err)
		require.NotZero(t, id)
		assert.Zero(t, heldQty(t, pool, hold, jammerGoods))
	})
}
