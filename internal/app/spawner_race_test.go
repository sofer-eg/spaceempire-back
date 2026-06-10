package app

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

type fakeRaceReader struct {
	race domain.RaceID
	err  error
}

func (f fakeRaceReader) PlayerRace(context.Context, domain.PlayerID) (domain.RaceID, error) {
	return f.race, f.err
}

func scoutCatalog(t *testing.T) *balance.ShipClasses {
	t.Helper()
	c, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 1, Race: 1, Class: 5, Name: "Разведчик"}, // Argon M5
		{ID: 2, Race: 2, Class: 5, Name: "Осьминог"},  // Boron M5
	})
	require.NoError(t, err)
	return c
}

// Phase 10.10: a player of a race with a home shipyard spawns there with the
// race's M5 name; a race without a home yard falls back to the config sector.
func TestUnit_ShipSpawner_HomeSpawnAndName(t *testing.T) {
	cfg := ShipSpawnerConfig{SectorID: 99}.withDefaults()
	yard := homeShipyard{Sector: 5, Pos: domain.Vec2{X: 70, Y: -422}}
	s := &shipSpawner{
		cfg:       cfg,
		rng:       rand.New(rand.NewPCG(1, 2)),
		players:   fakeRaceReader{race: 1},
		homeYards: map[domain.RaceID]homeShipyard{1: yard},
		classes:   scoutCatalog(t),
	}

	// Race resolution reads the player's race, ignoring the passed id.
	race, err := s.playerRace(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, domain.RaceID(1), race)

	// Home spawn: same sector as the yard, position within the spawn offset.
	sec, pos := s.homeSpawn(1)
	assert.Equal(t, domain.SectorID(5), sec)
	assert.LessOrEqual(t, math.Abs(pos.X-yard.Pos.X), cfg.SpawnHalfX)
	assert.LessOrEqual(t, math.Abs(pos.Y-yard.Pos.Y), cfg.SpawnHalfY)

	// Name comes from the race's M5 model.
	assert.Equal(t, "Разведчик", s.starterName(1))

	// A race with no home yard falls back to the config sector.
	sec2, _ := s.homeSpawn(2)
	assert.Equal(t, domain.SectorID(99), sec2)

	// A race with no scout in the catalog yields an empty name.
	assert.Equal(t, "", s.starterName(9))
}

// With no race reader / catalog wired (minimal deployments, tests), the
// spawner falls back to neutral race, empty name, config sector.
func TestUnit_ShipSpawner_NilDepsNeutralFallback(t *testing.T) {
	s := &shipSpawner{
		cfg: ShipSpawnerConfig{SectorID: 42}.withDefaults(),
		rng: rand.New(rand.NewPCG(1, 2)),
	}

	race, err := s.playerRace(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, domain.RaceID(0), race)

	assert.Equal(t, "", s.starterName(1))

	sec, _ := s.homeSpawn(0)
	assert.Equal(t, domain.SectorID(42), sec)
}

// starterCatalogWithStats returns a catalog with real M5 stats for races 1 and 2.
func starterCatalogWithStats(t *testing.T) *balance.ShipClasses {
	t.Helper()
	c, err := balance.NewShipClasses([]balance.ShipClass{
		{
			ID: 77, Race: 1, Class: 5, Name: "Разведчик",
			Speed: 60.6, Acceleration: 54.54, Hull: 4040,
			Shield: 6900, ShieldCharge: 80, Laser: 1356,
		},
		{ID: 81, Race: 2, Class: 5, Name: "Осьминог",
			Speed: 83.5, Acceleration: 87.675, Hull: 3200,
			Shield: 5400, ShieldCharge: 60, Laser: 1075,
		},
	})
	require.NoError(t, err)
	return c
}

// starterClass returns the M5 catalog entry for the race; nil catalog → false.
func TestUnit_StarterClass_ReturnsM5Stats(t *testing.T) {
	s := &shipSpawner{cfg: ShipSpawnerConfig{}.withDefaults(), classes: starterCatalogWithStats(t)}

	cls, ok := s.starterClass(1)
	require.True(t, ok, "Argon M5 present in catalog")
	assert.Equal(t, 4040, cls.Hull)
	assert.InDelta(t, 60.6, cls.Speed, 0.01)
	assert.InDelta(t, 54.54, cls.Acceleration, 0.01)
	assert.Equal(t, 6900, cls.Shield)
	assert.Equal(t, 80, cls.ShieldCharge)
	assert.Equal(t, 1356, cls.Laser)

	_, ok2 := s.starterClass(9) // no M5 for race 9
	assert.False(t, ok2)

	sNil := &shipSpawner{cfg: ShipSpawnerConfig{}.withDefaults()}
	_, ok3 := sNil.starterClass(1)
	assert.False(t, ok3, "nil catalog → fallback")
}

// baseShipStats bridges the class catalog to the equipment-effect baseline
// (phase 10.14): speed/accel/shield/laser from the class (laser via the
// warship divisor, floored), energy pools from spawn config. The same base
// feeds a freshly bought ship and the install-time recompute, and an
// accumulator then boosts MaxEnergy by 25%.
func TestUnit_BaseShipStats_FromClassAndConfig(t *testing.T) {
	cfg := ShipSpawnerConfig{}.withDefaults()
	cls, ok := starterCatalogWithStats(t).ScoutForRace(1)
	require.True(t, ok)

	base := baseShipStats(cls, cfg)
	assert.InDelta(t, 60.6, base.MaxSpeed, 0.01)
	assert.InDelta(t, 54.54, base.Acceleration, 0.01)
	assert.Equal(t, 6900, base.MaxShield)
	assert.Equal(t, 80, base.ShieldRecharge)
	assert.Equal(t, cfg.StartEnergy, base.MaxEnergy, "energy comes from spawn config, not the class")
	assert.Equal(t, cfg.StartEnergyChrg, base.EnergyRecharge)
	assert.Equal(t, 135, base.LaserDamage, "1356 / warshipLaserDivisor(10)")

	withAccum := balance.ApplyEquipmentEffects(base, []domain.InstalledEquipment{
		{Type: "up_accumulator", Level: 1},
	})
	assert.Equal(t, base.MaxEnergy+base.MaxEnergy/4, withAccum.MaxEnergy)
}

// buildHomeShipyards prefers the lowest-sector shipyard per race and skips
// unowned (race 0) yards (phase 10.10).
func TestUnit_BuildHomeShipyards_PrefersLowestSector(t *testing.T) {
	statics := map[domain.SectorID]domain.SectorStatics{
		1:  {Shipyards: []domain.Shipyard{{ID: 1, SectorID: 1, Race: 1, Pos: domain.Vec2{X: 1}}}},
		27: {Shipyards: []domain.Shipyard{{ID: 50, SectorID: 27, Race: 1, Pos: domain.Vec2{X: 2}}}},
		5:  {Shipyards: []domain.Shipyard{{ID: 2, SectorID: 5, Race: 2, Pos: domain.Vec2{X: 3}}}},
		60: {Shipyards: []domain.Shipyard{{ID: 99, SectorID: 60, Race: 0, Pos: domain.Vec2{X: 4}}}},
	}
	out := buildHomeShipyards(statics)

	require.Contains(t, out, domain.RaceID(1))
	assert.Equal(t, domain.SectorID(1), out[domain.RaceID(1)].Sector, "Argon picks sector 1, not 27")
	require.Contains(t, out, domain.RaceID(2))
	assert.Equal(t, domain.SectorID(5), out[domain.RaceID(2)].Sector)
	assert.NotContains(t, out, domain.RaceID(0), "unowned shipyards are skipped")
}
