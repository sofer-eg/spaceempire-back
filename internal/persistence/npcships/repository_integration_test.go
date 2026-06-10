package npcships_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/npcships"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id)
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

// TestIntegration_NPCShips_SystemPlayerSeeded confirms migration 0024 seeded
// the reserved __npc__ player and the repo finds it.
func TestIntegration_NPCShips_SystemPlayerSeeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := npcships.New(pool)

	id, err := repo.SystemPlayerID(ctx)
	require.NoError(t, err)
	assert.Greater(t, int64(id), int64(0), "system player must have a valid id")

	var login string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT login FROM players WHERE id = $1`, int64(id)).Scan(&login))
	assert.Equal(t, npcships.SystemPlayerLogin, login)
}

// TestIntegration_NPCShips_CreateAndCountByHome inserts two traders for the
// same home and confirms CountByHome aggregates them under the home EntityRef.
func TestIntegration_NPCShips_CreateAndCountByHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := npcships.New(pool)

	pid := seedPlayer(t, pool, "owner")
	ship1 := seedShip(t, pool, pid, 1)
	ship2 := seedShip(t, pool, pid, 1)
	home := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	other := domain.EntityRef{Kind: domain.EntityKindStation, ID: 2}
	ship3 := seedShip(t, pool, pid, 1)

	require.NoError(t, repo.Create(ctx, ship1, home, "trader"))
	require.NoError(t, repo.Create(ctx, ship2, home, "trader"))
	require.NoError(t, repo.Create(ctx, ship3, other, "miner"))

	counts, err := repo.CountByHome(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, counts[npcships.HomeKind{Home: home, Kind: "trader"}], "two traders serve home station 1")
	assert.Equal(t, 1, counts[npcships.HomeKind{Home: other, Kind: "miner"}], "one miner serves home station 2")
}

// TestIntegration_NPCShips_CascadeOnShipDelete verifies the ships ON DELETE
// CASCADE removes the npc_ships row when the ship dies.
func TestIntegration_NPCShips_CascadeOnShipDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := npcships.New(pool)

	pid := seedPlayer(t, pool, "owner")
	ship := seedShip(t, pool, pid, 1)
	home := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	require.NoError(t, repo.Create(ctx, ship, home, "trader"))

	_, err := pool.Exec(ctx, `DELETE FROM ships WHERE id = $1`, int64(ship))
	require.NoError(t, err)

	counts, err := repo.CountByHome(ctx)
	require.NoError(t, err)
	assert.Zero(t, counts[npcships.HomeKind{Home: home, Kind: "trader"}], "npc_ships row should cascade-delete with its ship")
}
