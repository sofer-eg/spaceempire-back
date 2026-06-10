package drones_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/drones"
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

func sampleDrone(pid domain.PlayerID, ship domain.ShipID, sector domain.SectorID, target domain.ShipID) domain.Drone {
	return domain.Drone{
		SectorID:    sector,
		OwnerShipID: ship,
		PlayerID:    pid,
		Pos:         domain.Vec2{X: 1, Y: 2},
		Vel:         domain.Vec2{X: 3, Y: 4},
		Direction:   domain.Vec2{X: 1, Y: 0},
		Target:      domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target)},
		HP:          20,
		Damage:      8,
		ExpiresAt:   time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond),
	}
}

// TestIntegration_Drones_CreateLoadAll round-trips a drone through Create
// and LoadAll, asserting the DB-assigned id and every field survive, and
// that LoadAll filters by sector.
func TestIntegration_Drones_CreateLoadAll(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	target := seedShip(t, pool, pid, 10)
	repo := drones.New(pool)

	d := sampleDrone(pid, ship, 10, target)
	id, err := repo.Create(context.Background(), d)
	require.NoError(t, err)
	require.NotZero(t, id)

	// A drone in another sector must not come back.
	other := sampleDrone(pid, seedShip(t, pool, pid, 99), 99, target)
	_, err = repo.Create(context.Background(), other)
	require.NoError(t, err)

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].ID)
	require.Equal(t, ship, got[0].OwnerShipID)
	require.Equal(t, pid, got[0].PlayerID)
	require.Equal(t, d.Pos, got[0].Pos)
	require.Equal(t, d.Vel, got[0].Vel)
	require.Equal(t, d.Direction, got[0].Direction)
	require.Equal(t, d.Target, got[0].Target)
	require.Equal(t, d.HP, got[0].HP)
	require.Equal(t, d.Damage, got[0].Damage)
	require.WithinDuration(t, d.ExpiresAt, got[0].ExpiresAt, time.Second)
}

// TestIntegration_Drones_BatchUpdate persists the mutable fields.
func TestIntegration_Drones_BatchUpdate(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	target := seedShip(t, pool, pid, 10)
	repo := drones.New(pool)

	id, err := repo.Create(context.Background(), sampleDrone(pid, ship, 10, target))
	require.NoError(t, err)

	updated := sampleDrone(pid, ship, 10, target)
	updated.ID = id
	updated.Pos = domain.Vec2{X: 99, Y: 88}
	updated.Vel = domain.Vec2{X: -1, Y: -2}
	updated.HP = 7
	require.NoError(t, repo.BatchUpdate(context.Background(), []domain.Drone{updated}))

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, domain.Vec2{X: 99, Y: 88}, got[0].Pos)
	require.Equal(t, domain.Vec2{X: -1, Y: -2}, got[0].Vel)
	require.Equal(t, 7, got[0].HP)
}

// TestIntegration_Drones_Delete removes a row and reports missing ones.
func TestIntegration_Drones_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	target := seedShip(t, pool, pid, 10)
	repo := drones.New(pool)

	id, err := repo.Create(context.Background(), sampleDrone(pid, ship, 10, target))
	require.NoError(t, err)

	require.NoError(t, repo.Delete(context.Background(), id))
	require.ErrorIs(t, repo.Delete(context.Background(), id), drones.ErrDroneNotFound)

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestIntegration_Drones_OwnerCascade: deleting the owner ship cascades
// to its drones (owner-loss cleanup at the DB level).
func TestIntegration_Drones_OwnerCascade(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10)
	target := seedShip(t, pool, pid, 10)
	repo := drones.New(pool)

	_, err := repo.Create(context.Background(), sampleDrone(pid, ship, 10, target))
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(), `DELETE FROM ships WHERE id = $1`, int64(ship))
	require.NoError(t, err)

	got, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, got, "drones cascade-deleted with their owner ship")
}
