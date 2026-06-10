package stations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/stations"
	"spaceempire/back/internal/pkg/database/testdb"
)

func TestIntegration_Stations_LoadAll_ReturnsSeed(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := stations.New(pool)
	ctx := context.Background()

	// Migration 0036 (full StarWind map import, phase 9.2/10.8) replaced the
	// 0004 stub seed. Sector 1 (Argon Prime) holds many production stations
	// plus a shipyard and a trade station; sector 5 adds a pirbase. Assert the
	// kinds load and split correctly, then spot-check known statics — don't pin
	// exact counts, which track the imported map.
	s1, err := repo.LoadAll(ctx, domain.SectorID(1))
	require.NoError(t, err)
	assert.NotEmpty(t, s1.Stations)
	assert.NotEmpty(t, s1.Shipyards)
	assert.NotEmpty(t, s1.TradeStations)
	assert.Empty(t, s1.Pirbases)

	// Station id 1 (Argon power plant, type 160) round-trips the fields the
	// snapshot pipeline depends on. LoadAll makes no ordering guarantee, so
	// look it up by id rather than indexing.
	station := findStation(t, s1.Stations, 1)
	assert.Equal(t, domain.SectorID(1), station.SectorID)
	assert.Equal(t, domain.Vec2{X: 96, Y: 705}, station.Pos)
	assert.Equal(t, 160, station.Type)
	assert.Equal(t, 1, station.Race)
	assert.True(t, station.Built)
	assert.Greater(t, station.HP, 0)
	assert.Greater(t, station.Shield, 0)
	assert.Nil(t, station.OwnerID)

	// Sector 5 carries the pirbase (race 6, pirates).
	s5, err := repo.LoadAll(ctx, domain.SectorID(5))
	require.NoError(t, err)
	assert.NotEmpty(t, s5.Stations)
	require.Len(t, s5.Pirbases, 1)
	pirbase := s5.Pirbases[0]
	assert.Equal(t, domain.SectorID(5), pirbase.SectorID)
	assert.Equal(t, domain.Vec2{X: -180, Y: -120}, pirbase.Pos)
	assert.Equal(t, 6, pirbase.Race)
	assert.True(t, pirbase.Built)

	// A sector with no static object still produces an empty (non-error)
	// result. Sector 28 carries no statics in the imported map.
	empty, err := repo.LoadAll(ctx, domain.SectorID(28))
	require.NoError(t, err)
	assert.True(t, empty.IsEmpty())
}

// findStation returns the loaded station with the given id, failing the test
// when it is absent — LoadAll makes no ordering guarantee.
func findStation(t *testing.T, list []domain.Station, id domain.StationID) domain.Station {
	t.Helper()
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("station %d not found in LoadAll result", id)
	return domain.Station{}
}

func TestIntegration_Stations_GetStation_RoundTripsAndNotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := stations.New(pool)
	ctx := context.Background()

	// Pick whatever station the seed places in sector 1 and read it back by id.
	s1, err := repo.LoadAll(ctx, domain.SectorID(1))
	require.NoError(t, err)
	require.NotEmpty(t, s1.Stations)
	want := s1.Stations[0]

	got, err := repo.GetStation(ctx, want.ID)
	require.NoError(t, err)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.SectorID, got.SectorID)
	assert.Equal(t, want.Type, got.Type)
	assert.Equal(t, want.Pos, got.Pos)
	// A freshly seeded station is idle: no cycle in flight.
	assert.False(t, got.InProgress)
	assert.True(t, got.NextCycleAt.IsZero())

	_, err = repo.GetStation(ctx, domain.StationID(99_999_999))
	require.ErrorIs(t, err, stations.ErrStationNotFound)
}
