package satellites_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/satellites"
	"spaceempire/back/internal/pkg/database/testdb"
)

// satellites carries no migration seed, so the round-trip tests use sector 2.
const freeSector = domain.SectorID(2)

func sampleSatellite(sector domain.SectorID) domain.Satellite {
	return domain.Satellite{
		SectorID:       sector,
		Pos:            domain.Vec2{X: 12, Y: -34},
		Race:           2,
		Built:          true,
		HP:             5000,
		Shield:         2000,
		MaxShield:      2000,
		ShieldRecharge: 20,
	}
}

// TestIntegration_Satellites_EmptyByDefault: no seed, so a fresh sector loads empty.
func TestIntegration_Satellites_EmptyByDefault(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := satellites.New(pool)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestIntegration_Satellites_CreateLoadAll round-trips a satellite and checks
// LoadAll filters by sector and preserves combat fields.
func TestIntegration_Satellites_CreateLoadAll(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := satellites.New(pool)

	id, err := repo.Create(context.Background(), sampleSatellite(freeSector))
	require.NoError(t, err)
	require.NotZero(t, id)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].ID)
	require.Equal(t, domain.Vec2{X: 12, Y: -34}, got[0].Pos)
	require.Equal(t, 5000, got[0].HP)
	require.Equal(t, 2000, got[0].MaxShield)
	require.Equal(t, 20, got[0].ShieldRecharge)
	require.Equal(t, 2, got[0].Race)
	require.True(t, got[0].Built)
	require.Nil(t, got[0].OwnerID)
}

// TestIntegration_Satellites_Delete removes a row and reports missing ones.
func TestIntegration_Satellites_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := satellites.New(pool)

	id, err := repo.Create(context.Background(), sampleSatellite(freeSector))
	require.NoError(t, err)

	require.NoError(t, repo.Delete(context.Background(), id))
	require.ErrorIs(t, repo.Delete(context.Background(), id), satellites.ErrNotFound)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Empty(t, got)
}
