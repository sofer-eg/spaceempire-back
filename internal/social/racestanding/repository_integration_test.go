package racestanding_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/social/racestanding"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

// TestIntegration_RaceStanding_RoundTrip exercises Upsert (ON CONFLICT in-place
// update), LoadAll, and DecayAll against real Postgres. player_race_standing
// has an FK to players, so a player row is seeded first.
func TestIntegration_RaceStanding_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := racestanding.NewRepository(pool)

	player := seedPlayer(t, pool, "smuggler")
	const argon = domain.RaceID(1)

	require.NoError(t, repo.Upsert(ctx, player, argon, -5))

	rows, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, player, rows[0].Player)
	assert.Equal(t, argon, rows[0].Race)
	assert.Equal(t, -5, rows[0].Standing)

	// Upsert the same pair — must update in place, not duplicate.
	require.NoError(t, repo.Upsert(ctx, player, argon, -12))
	rows, err = repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "upsert must not duplicate the (player,race) pair")
	assert.Equal(t, -12, rows[0].Standing)

	// DecayAll nudges toward 0 by the step (and never overshoots).
	require.NoError(t, repo.DecayAll(ctx, 3))
	rows, err = repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, -9, rows[0].Standing)
}
