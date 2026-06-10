package containers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/containers"
	"spaceempire/back/internal/pkg/database"
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

func seedShip(t *testing.T, pool *pgxpool.Pool, pid domain.PlayerID, sector domain.SectorID, cargobay float64) domain.ShipID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, hp, shield, cargobay) VALUES ($1, $2, 100, 100, $3) RETURNING id`,
		int64(pid), int64(sector), cargobay).Scan(&id)
	require.NoError(t, err)
	return domain.ShipID(id)
}

func seedCargo(t *testing.T, pool *pgxpool.Pool, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO cargo (owner_kind, owner_id, goods_type_id, quantity) VALUES ($1, $2, $3, $4)`,
		int16(owner.Kind), owner.ID, int32(gtype), qty)
	require.NoError(t, err)
}

func cargoQty(t *testing.T, pool *pgxpool.Pool, owner domain.EntityRef, gtype domain.GoodsTypeID) int64 {
	t.Helper()
	var qty int64
	err := pool.QueryRow(context.Background(),
		`SELECT COALESCE((SELECT quantity FROM cargo WHERE owner_kind=$1 AND owner_id=$2 AND goods_type_id=$3), 0)`,
		int16(owner.Kind), owner.ID, int32(gtype)).Scan(&qty)
	require.NoError(t, err)
	return qty
}

func shipExists(t *testing.T, pool *pgxpool.Pool, id domain.ShipID) bool {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM ships WHERE id=$1`, int64(id)).Scan(&n)
	require.NoError(t, err)
	return n > 0
}

func newRepo(pool *pgxpool.Pool) *containers.Repository {
	return containers.New(pool, database.NewTxManager(pool))
}

func sampleDrop(gtype domain.GoodsTypeID, qty int64) domain.ContainerDrop {
	return domain.ContainerDrop{
		Pos:       domain.Vec2{X: 5, Y: 6},
		ExpiresAt: time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond),
		GoodsType: gtype,
		Quantity:  qty,
	}
}

func TestIntegration_Containers_ShipCargo(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10, 1000)
	seedCargo(t, pool, domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship)}, 7, 42)

	got, err := newRepo(pool).ShipCargo(context.Background(), ship)
	require.NoError(t, err)
	require.Equal(t, []domain.CargoItem{{GoodsType: 7, Quantity: 42}}, got)
}

func TestIntegration_Containers_RecordKillDropsCargo(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10, 1000)
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship)}
	seedCargo(t, pool, shipRef, 7, 100)
	repo := newRepo(pool)

	drops := []domain.ContainerDrop{sampleDrop(7, 100)}
	got, err := repo.RecordKill(context.Background(), ship, 10, drops)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotZero(t, got[0].ID)
	require.Equal(t, domain.SectorID(10), got[0].SectorID)
	require.Equal(t, domain.Vec2{X: 5, Y: 6}, got[0].Pos)

	require.False(t, shipExists(t, pool, ship), "victim ship deleted")
	require.Zero(t, cargoQty(t, pool, shipRef, 7), "victim cargo cleared")

	containerRef := domain.EntityRef{Kind: domain.EntityKindContainer, ID: int64(got[0].ID)}
	require.Equal(t, int64(100), cargoQty(t, pool, containerRef, 7), "cargo moved into the container")

	loaded, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, got[0].ID, loaded[0].ID)
}

func TestIntegration_Containers_RecordKillNoDrops(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10, 1000)
	repo := newRepo(pool)

	got, err := repo.RecordKill(context.Background(), ship, 10, nil)
	require.NoError(t, err)
	require.Empty(t, got, "no drops → no container")
	require.False(t, shipExists(t, pool, ship), "ship still deleted")
}

func TestIntegration_Containers_Pickup(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, 10, 1000)
	victim := seedShip(t, pool, pid, 10, 1000)
	seedCargo(t, pool, domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(victim)}, 7, 50)
	repo := newRepo(pool)

	created, err := repo.RecordKill(context.Background(), victim, 10, []domain.ContainerDrop{sampleDrop(7, 50)})
	require.NoError(t, err)
	require.Len(t, created, 1)
	cid := created[0].ID

	require.NoError(t, repo.Pickup(context.Background(), cid, ship))

	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship)}
	require.Equal(t, int64(50), cargoQty(t, pool, shipRef, 7), "cargo moved to the picker")

	loaded, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, loaded, "container removed after pickup")
}

func TestIntegration_Containers_PickupNoSpace(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	// Picker hold is only 10 units; the container carries 50 of a space-1 good.
	ship := seedShip(t, pool, pid, 10, 10)
	victim := seedShip(t, pool, pid, 10, 1000)
	seedCargo(t, pool, domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(victim)}, 7, 50)
	repo := newRepo(pool)

	created, err := repo.RecordKill(context.Background(), victim, 10, []domain.ContainerDrop{sampleDrop(7, 50)})
	require.NoError(t, err)
	cid := created[0].ID

	require.ErrorIs(t, repo.Pickup(context.Background(), cid, ship), containers.ErrNoSpace)

	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship)}
	require.Zero(t, cargoQty(t, pool, shipRef, 7), "nothing moved on no-space")
	loaded, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, loaded, 1, "container stays on no-space")
}

func TestIntegration_Containers_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	victim := seedShip(t, pool, pid, 10, 1000)
	seedCargo(t, pool, domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(victim)}, 7, 30)
	repo := newRepo(pool)

	created, err := repo.RecordKill(context.Background(), victim, 10, []domain.ContainerDrop{sampleDrop(7, 30)})
	require.NoError(t, err)
	cid := created[0].ID

	require.NoError(t, repo.Delete(context.Background(), cid))

	containerRef := domain.EntityRef{Kind: domain.EntityKindContainer, ID: int64(cid)}
	require.Zero(t, cargoQty(t, pool, containerRef, 7), "container cargo deleted with it")
	loaded, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, loaded)
}

func TestIntegration_Containers_LoadAllFiltersSector(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	here := seedShip(t, pool, pid, 10, 1000)
	there := seedShip(t, pool, pid, 99, 1000)
	repo := newRepo(pool)

	_, err := repo.RecordKill(context.Background(), here, 10, []domain.ContainerDrop{sampleDrop(7, 5)})
	require.NoError(t, err)
	_, err = repo.RecordKill(context.Background(), there, 99, []domain.ContainerDrop{sampleDrop(7, 5)})
	require.NoError(t, err)

	loaded, err := repo.LoadAll(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, domain.SectorID(10), loaded[0].SectorID)
}
