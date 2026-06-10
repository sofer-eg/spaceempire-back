package lasertowers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/lasertowers"
	"spaceempire/back/internal/pkg/database/testdb"
)

// sector 1 carries the migration-seeded tower; the round-trip tests use
// sector 2 (no seed) to stay isolated from it.
const (
	seededSector = domain.SectorID(1)
	freeSector   = domain.SectorID(2)
)

func sampleTower(sector domain.SectorID) domain.LaserTower {
	return domain.LaserTower{
		SectorID: sector,
		Pos:      domain.Vec2{X: 12, Y: -34},
		HP:       50000,
		Shield:   50000,
		Race:     2,
		Built:    true,
	}
}

// TestIntegration_LaserTowers_Seed asserts the 0019 migration seed loads.
func TestIntegration_LaserTowers_Seed(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := lasertowers.New(pool)

	got, err := repo.LoadAll(context.Background(), seededSector)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, domain.Vec2{X: 60, Y: -60}, got[0].Pos)
	require.Nil(t, got[0].OwnerID, "seeded NPC tower has no owner")
	require.True(t, got[0].Built)
}

// TestIntegration_LaserTowers_CreateLoadAll round-trips a tower and checks
// LoadAll filters by sector.
func TestIntegration_LaserTowers_CreateLoadAll(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := lasertowers.New(pool)

	id, err := repo.Create(context.Background(), sampleTower(freeSector))
	require.NoError(t, err)
	require.NotZero(t, id)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].ID)
	require.Equal(t, domain.Vec2{X: 12, Y: -34}, got[0].Pos)
	require.Equal(t, 50000, got[0].HP)
	require.Equal(t, 2, got[0].Race)
	require.Nil(t, got[0].OwnerID)
}

// TestIntegration_LaserTowers_Delete removes a row and reports missing ones.
func TestIntegration_LaserTowers_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := lasertowers.New(pool)

	id, err := repo.Create(context.Background(), sampleTower(freeSector))
	require.NoError(t, err)

	require.NoError(t, repo.Delete(context.Background(), id))
	require.ErrorIs(t, repo.Delete(context.Background(), id), lasertowers.ErrNotFound)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Empty(t, got)
}
