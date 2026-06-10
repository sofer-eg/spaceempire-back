package world_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/world"
	"spaceempire/back/internal/pkg/database/testdb"
)

func TestIntegration_World_LoadAll_ReturnsSeed(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := world.New(pool)

	sectors, gates, err := repo.LoadAll(context.Background())
	require.NoError(t, err)

	// Migration 0034 (the StarWind port) replaces the stub world with the
	// 70 named sectors and their 75 gates; 0039 backfills grid coordinates.
	require.Len(t, sectors, 70)
	require.Len(t, gates, 75)

	// Spot-check the first sector: name + bounds round-trip, plus the grid
	// coordinates (0039) and controlling race (Argon trade station → 1)
	// that drive the schematic galaxy map.
	assert.Equal(t, domain.SectorID(1), sectors[0].ID)
	assert.Equal(t, "Аргон Прайм", sectors[0].Name)
	assert.Equal(t, domain.Vec2{X: -1000, Y: -1000}, sectors[0].Bounds.Min)
	assert.Equal(t, domain.Vec2{X: 1000, Y: 1000}, sectors[0].Bounds.Max)
	assert.Equal(t, 45, sectors[0].GridX)
	assert.Equal(t, 48, sectors[0].GridY)
	assert.Equal(t, 1, sectors[0].Race)

	// Gates are loaded in id order and link two distinct, valid sectors.
	assert.NotEqual(t, gates[0].SectorA, gates[0].SectorB)
	assert.NotZero(t, gates[0].SectorA)
	assert.NotZero(t, gates[0].SectorB)
}
