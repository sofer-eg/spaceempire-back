package jammers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/jammers"
	"spaceempire/back/internal/pkg/database/testdb"
)

// jammers carries no migration seed, so the round-trip tests use sector 2.
// otherSector holds the decoy row that makes LoadAll's sector filter observable.
const (
	freeSector  = domain.SectorID(2)
	otherSector = domain.SectorID(3)
)

func sampleJammer(sector domain.SectorID) domain.Jammer {
	return domain.Jammer{
		SectorID:       sector,
		Pos:            domain.Vec2{X: 12, Y: -34},
		Race:           2,
		Built:          true,
		HP:             7500,
		Shield:         4000,
		MaxShield:      4000,
		ShieldRecharge: 20,
	}
}

// TestIntegration_Jammers_EmptyByDefault: no seed, so a fresh sector loads empty.
func TestIntegration_Jammers_EmptyByDefault(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := jammers.New(pool)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestIntegration_Jammers_CreateLoadAll round-trips a jammer and checks LoadAll
// filters by sector and preserves combat fields.
func TestIntegration_Jammers_CreateLoadAll(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := jammers.New(pool)

	id, err := repo.Create(context.Background(), sampleJammer(freeSector))
	require.NoError(t, err)
	require.NotZero(t, id)

	// A second generator one sector over: LoadAll must not hand it back, which
	// is the only way the WHERE sector_id = $1 clause is actually exercised.
	decoy, err := repo.Create(context.Background(), sampleJammer(otherSector))
	require.NoError(t, err)
	require.NotEqual(t, id, decoy)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the generator in freeSector is returned")
	require.Equal(t, id, got[0].ID)
	require.Equal(t, domain.Vec2{X: 12, Y: -34}, got[0].Pos)
	require.Equal(t, 7500, got[0].HP)
	require.Equal(t, 4000, got[0].MaxShield)
	require.Equal(t, 20, got[0].ShieldRecharge)
	require.Equal(t, 2, got[0].Race)
	require.True(t, got[0].Built)
	require.Nil(t, got[0].OwnerID)
}

// TestIntegration_Jammers_Delete removes a row and reports missing ones.
func TestIntegration_Jammers_Delete(t *testing.T) {
	t.Parallel()
	pool := testdb.Setup(t)
	repo := jammers.New(pool)

	id, err := repo.Create(context.Background(), sampleJammer(freeSector))
	require.NoError(t, err)

	require.NoError(t, repo.Delete(context.Background(), id))
	require.ErrorIs(t, repo.Delete(context.Background(), id), jammers.ErrNotFound)

	got, err := repo.LoadAll(context.Background(), freeSector)
	require.NoError(t, err)
	require.Empty(t, got)
}
