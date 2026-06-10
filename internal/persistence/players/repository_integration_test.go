package players_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool) domain.PlayerID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, $2) RETURNING id`,
		t.Name(), "x").Scan(&id)
	require.NoError(t, err)
	return domain.PlayerID(id)
}

func seedShip(t *testing.T, pool *pgxpool.Pool, owner domain.PlayerID) domain.ShipID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id) VALUES ($1, 1) RETURNING id`,
		int64(owner)).Scan(&id)
	require.NoError(t, err)
	return domain.ShipID(id)
}

func TestIntegration_PlayersRepository_ActiveShip_DefaultNull(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)
	shipID, ok, err := repo.ActiveShip(context.Background(), pid)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Zero(t, shipID)
}

func TestIntegration_PlayersRepository_SetActiveShip_RoundTrip(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)
	shipID := seedShip(t, pool, pid)

	require.NoError(t, repo.SetActiveShip(context.Background(), pid, shipID))

	got, ok, err := repo.ActiveShip(context.Background(), pid)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, shipID, got)
}

func TestIntegration_PlayersRepository_PassengerHost_RoundTripAndLinks(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)
	ctx := context.Background()

	pid := seedPlayer(t, pool)
	host := seedShip(t, pool, pid)

	_, ok, err := repo.PassengerHost(ctx, pid)
	require.NoError(t, err)
	assert.False(t, ok, "default passenger_of_ship_id is null")

	require.NoError(t, repo.SetPassengerHost(ctx, pid, host))
	got, ok, err := repo.PassengerHost(ctx, pid)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, host, got)

	links, err := repo.PassengerLinks(ctx)
	require.NoError(t, err)
	found := false
	for _, l := range links {
		if l.PlayerID == pid && l.HostShipID == host {
			found = true
		}
	}
	assert.True(t, found, "PassengerLinks reports the active link")

	require.NoError(t, repo.SetPassengerHost(ctx, pid, 0))
	_, ok, err = repo.PassengerHost(ctx, pid)
	require.NoError(t, err)
	assert.False(t, ok, "zero clears passenger host")
}

func TestIntegration_PlayersRepository_SetActiveShip_ZeroClears(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)
	shipID := seedShip(t, pool, pid)
	require.NoError(t, repo.SetActiveShip(context.Background(), pid, shipID))

	require.NoError(t, repo.SetActiveShip(context.Background(), pid, 0))

	_, ok, err := repo.ActiveShip(context.Background(), pid)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestIntegration_PlayersRepository_GetCash_ReturnsDefault(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)
	cash, err := repo.GetCash(context.Background(), pid)
	require.NoError(t, err)
	assert.EqualValues(t, 10000, cash)
}

func TestIntegration_PlayersRepository_GetCash_NotFound(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	_, err := repo.GetCash(context.Background(), domain.PlayerID(999_999))
	require.ErrorIs(t, err, players.ErrPlayerNotFound)
}

func TestIntegration_PlayersRepository_AdjustCash_AddAndSubtract(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)

	newCash, err := repo.AdjustCash(context.Background(), pid, 500)
	require.NoError(t, err)
	assert.EqualValues(t, 10500, newCash)

	newCash, err = repo.AdjustCash(context.Background(), pid, -2000)
	require.NoError(t, err)
	assert.EqualValues(t, 8500, newCash)
}

func TestIntegration_PlayersRepository_AdjustCash_Insufficient(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)
	_, err := repo.AdjustCash(context.Background(), pid, -99_999)
	require.ErrorIs(t, err, players.ErrInsufficientCash)
}

func TestIntegration_PlayersRepository_GetReputation_ReturnsDefaultZero(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	pid := seedPlayer(t, pool)
	rep, err := repo.GetReputation(context.Background(), pid)
	require.NoError(t, err)
	assert.Equal(t, players.Reputation{}, rep)
}

func TestIntegration_PlayersRepository_GetReputation_NotFound(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	_, err := repo.GetReputation(context.Background(), domain.PlayerID(999_999))
	require.ErrorIs(t, err, players.ErrPlayerNotFound)
}

func TestIntegration_PlayersRepository_AddReputation_RoundTrip(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)
	ctx := context.Background()

	pid := seedPlayer(t, pool)

	got, err := repo.AddReputation(ctx, pid, players.Reputation{War: 10, Trade: 5, Race: 3})
	require.NoError(t, err)
	assert.Equal(t, players.Reputation{War: 10, Trade: 5, Race: 3}, got)

	got, err = repo.AddReputation(ctx, pid, players.Reputation{War: -4, Trade: 1})
	require.NoError(t, err)
	assert.Equal(t, players.Reputation{War: 6, Trade: 6, Race: 3}, got)

	persisted, err := repo.GetReputation(ctx, pid)
	require.NoError(t, err)
	assert.Equal(t, got, persisted)
}

func TestIntegration_PlayersRepository_AddReputation_NotFound(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := players.New(pool)

	_, err := repo.AddReputation(context.Background(), domain.PlayerID(999_999), players.Reputation{War: 1})
	require.ErrorIs(t, err, players.ErrPlayerNotFound)
}
