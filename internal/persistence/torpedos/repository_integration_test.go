package torpedos_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/torpedos"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool) domain.PlayerID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ('p', 'h') RETURNING id`).Scan(&id)
	require.NoError(t, err)
	return domain.PlayerID(id)
}

func seedShip(t *testing.T, pool *pgxpool.Pool, pid domain.PlayerID, sector domain.SectorID) domain.ShipID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, hp, shield) VALUES ($1, $2, 100, 100) RETURNING id`,
		int64(pid), int64(sector)).Scan(&id)
	require.NoError(t, err)
	return domain.ShipID(id)
}

// sampleTorpedo sets every field to a distinctive value so the round-trip
// asserts each column independently. Target uses a static kind to exercise a
// non-ship target_kind (target_id has no FK).
func sampleTorpedo(pid domain.PlayerID, ship domain.ShipID, sector domain.SectorID) domain.Torpedo {
	return domain.Torpedo{
		SectorID:      sector,
		OwnerShipID:   ship,
		PlayerID:      pid,
		Pos:           domain.Vec2{X: 1, Y: 2},
		Vel:           domain.Vec2{X: 3, Y: 4},
		Direction:     domain.Vec2{X: 1, Y: 0},
		Target:        domain.EntityRef{Kind: domain.EntityKindStation, ID: 777},
		LastTargetPos: domain.Vec2{X: 5, Y: 6},
		Class:         3,
		Damage:        1000000,
		Speed:         80,
		Accel:         80,
		TurnRate:      240,
		HitRadius:     12,
		SplashRadius:  40,
		HP:            26000,
		ExpiresAt:     time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond),
	}
}

// TestIntegration_Torpedos_CreateLoadAll round-trips a torpedo through
// Create and LoadAll, asserting the DB-assigned id and every column survive,
// and that LoadAll filters by sector.
func TestIntegration_Torpedos_CreateLoadAll(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	repo := torpedos.New(pool)

	tp := sampleTorpedo(pid, ship, 10)
	id, err := repo.Create(context.Background(), tp)
	require.NoError(t, err)
	require.NotZero(t, id)

	// A torpedo in another sector must not come back.
	other := sampleTorpedo(pid, seedShip(t, pool, pid, 99), 99)
	_, err = repo.Create(context.Background(), other)
	require.NoError(t, err)

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].ID)
	require.Equal(t, domain.SectorID(10), got[0].SectorID, "sector_id round-trips (also the LoadAll filter key)")
	require.Equal(t, ship, got[0].OwnerShipID)
	require.Equal(t, pid, got[0].PlayerID)
	require.Equal(t, tp.Pos, got[0].Pos)
	require.Equal(t, tp.Vel, got[0].Vel)
	require.Equal(t, tp.Direction, got[0].Direction)
	require.Equal(t, tp.Target, got[0].Target)
	require.Equal(t, tp.LastTargetPos, got[0].LastTargetPos)
	require.Equal(t, tp.Class, got[0].Class)
	require.Equal(t, tp.Damage, got[0].Damage)
	require.Equal(t, tp.Speed, got[0].Speed)
	require.Equal(t, tp.Accel, got[0].Accel)
	require.Equal(t, tp.TurnRate, got[0].TurnRate)
	require.Equal(t, tp.HitRadius, got[0].HitRadius)
	require.Equal(t, tp.SplashRadius, got[0].SplashRadius)
	require.Equal(t, tp.HP, got[0].HP)
	require.WithinDuration(t, tp.ExpiresAt, got[0].ExpiresAt, time.Second)
}

// TestIntegration_Torpedos_BatchUpdate persists the mutable fields and
// leaves the static profile untouched. The payload deliberately carries
// DIFFERENT static values (Class 2 / Damage 5) than the stored row (Class 3 /
// Damage 1000000): if batchUpdateSQL ever started writing a static column the
// row would flip to the payload's value, so the assertion that the ORIGINAL
// statics survive is what catches that — a tautological payload (same statics)
// would pass even with a buggy UPDATE.
func TestIntegration_Torpedos_BatchUpdate(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	repo := torpedos.New(pool)

	id, err := repo.Create(context.Background(), sampleTorpedo(pid, ship, 10))
	require.NoError(t, err)

	updated := sampleTorpedo(pid, ship, 10)
	updated.ID = id
	updated.Pos = domain.Vec2{X: 99, Y: 88}
	updated.Vel = domain.Vec2{X: -1, Y: -2}
	updated.LastTargetPos = domain.Vec2{X: 77, Y: 66}
	updated.HP = 7
	// Static profile in the payload differs from the stored row — BatchUpdate
	// must ignore these and keep the originals.
	updated.Class = 2
	updated.Damage = 5
	updated.Speed = 1
	updated.SplashRadius = 1
	require.NoError(t, repo.BatchUpdate(context.Background(), []domain.Torpedo{updated}))

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, domain.Vec2{X: 99, Y: 88}, got[0].Pos)
	require.Equal(t, domain.Vec2{X: -1, Y: -2}, got[0].Vel)
	require.Equal(t, domain.Vec2{X: 77, Y: 66}, got[0].LastTargetPos)
	require.Equal(t, 7, got[0].HP)
	// Static profile is not batched: the ORIGINAL stored values must survive
	// even though the payload tried to overwrite them.
	require.Equal(t, 3, got[0].Class, "BatchUpdate must not write the static class column")
	require.Equal(t, 1000000, got[0].Damage, "BatchUpdate must not write the static damage column")
	require.Equal(t, float64(80), got[0].Speed, "BatchUpdate must not write the static speed column")
	require.Equal(t, float64(40), got[0].SplashRadius, "BatchUpdate must not write the static splash_radius column")
}

// TestIntegration_Torpedos_Delete removes a row and reports missing ones.
func TestIntegration_Torpedos_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	repo := torpedos.New(pool)

	id, err := repo.Create(context.Background(), sampleTorpedo(pid, ship, 10))
	require.NoError(t, err)

	require.NoError(t, repo.Delete(context.Background(), id))
	require.ErrorIs(t, repo.Delete(context.Background(), id), torpedos.ErrTorpedoNotFound)

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestIntegration_Torpedos_OwnerCascade: deleting the owner ship cascades to
// its torpedoes (owner-loss cleanup at the DB level).
func TestIntegration_Torpedos_OwnerCascade(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	repo := torpedos.New(pool)

	_, err := repo.Create(context.Background(), sampleTorpedo(pid, ship, 10))
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(), `DELETE FROM ships WHERE id = $1`, int64(ship))
	require.NoError(t, err)

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, got, "torpedoes cascade-deleted with their owner ship")
}
