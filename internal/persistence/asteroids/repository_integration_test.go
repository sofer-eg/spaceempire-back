package asteroids_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/asteroids"
	"spaceempire/back/internal/pkg/database/testdb"
)

// insertAsteroid seeds one asteroid row directly (the repo has no Create — at
// runtime asteroids come from the migration seed) and returns its id.
func insertAsteroid(t *testing.T, pool *pgxpool.Pool, sector domain.SectorID, mass int64, ore domain.GoodsTypeID) domain.AsteroidID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO asteroids (sector_id, pos_x, pos_y, mass, ore_type)
		 VALUES ($1, 10, 20, $2, $3) RETURNING id`,
		int64(sector), mass, int64(ore)).Scan(&id)
	require.NoError(t, err)
	return domain.AsteroidID(id)
}

// TestIntegration_Asteroids_LoadAll_FiltersBySector round-trips an asteroid
// and confirms LoadAll returns only the requested sector's rows.
func TestIntegration_Asteroids_LoadAll_FiltersBySector(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := asteroids.New(pool)

	id := insertAsteroid(t, pool, 2, 150, 8)
	insertAsteroid(t, pool, 3, 99, 8) // another sector — must not come back

	got, err := repo.LoadAll(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, id, got[0].ID)
	assert.Equal(t, domain.SectorID(2), got[0].SectorID)
	assert.Equal(t, domain.Vec2{X: 10, Y: 20}, got[0].Pos)
	assert.EqualValues(t, 150, got[0].Mass)
	assert.Equal(t, domain.GoodsTypeID(8), got[0].OreType)
}

// TestIntegration_Asteroids_BatchUpdate_PersistsMass writes the mined-down
// mass and reads it back.
func TestIntegration_Asteroids_BatchUpdate_PersistsMass(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := asteroids.New(pool)

	id := insertAsteroid(t, pool, 2, 150, 8)
	require.NoError(t, repo.BatchUpdate(context.Background(), []domain.Asteroid{
		{ID: id, SectorID: 2, Pos: domain.Vec2{X: 10, Y: 20}, Mass: 40, OreType: 8},
	}))

	got, err := repo.LoadAll(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.EqualValues(t, 40, got[0].Mass)
}

// TestIntegration_Asteroids_Delete removes a row and reports a missing one.
func TestIntegration_Asteroids_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := asteroids.New(pool)

	id := insertAsteroid(t, pool, 2, 10, 8)
	require.NoError(t, repo.Delete(context.Background(), id))
	require.ErrorIs(t, repo.Delete(context.Background(), id), asteroids.ErrAsteroidNotFound)

	got, err := repo.LoadAll(context.Background(), 2)
	require.NoError(t, err)
	assert.Empty(t, got)
}
