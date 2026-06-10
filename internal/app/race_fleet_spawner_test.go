package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	npcshipsrepo "spaceempire/back/internal/persistence/npcships"
)

func TestUnit_RaceFleet_WarshipClassesByRace_FiltersAndSorts(t *testing.T) {
	t.Parallel()
	all := []balance.ShipClass{
		{ID: 78, Race: 1, Class: 6, Name: "Кентавр"}, // M6
		{ID: 76, Race: 1, Class: 4, Name: "Охотник"}, // M4
		{ID: 75, Race: 1, Class: 3, Name: "Нова"},    // M3
		{ID: 1, Race: 1, Class: 9, Name: "Меркурий"}, // TS — not a warship
		{ID: 120, Race: 6, Class: 3, Name: "Ориноко"},
	}
	got := warshipClassesByRace(all)
	require.Len(t, got[1], 3, "race 1 keeps M3/M4/M6, drops the TS")
	// Sorted by (class, id): M3(75), M4(76), M6(78).
	assert.Equal(t, domain.ShipClassID(75), got[1][0].ID)
	assert.Equal(t, domain.ShipClassID(76), got[1][1].ID)
	assert.Equal(t, domain.ShipClassID(78), got[1][2].ID)
	require.Len(t, got[6], 1, "pirate M3 kept")
	assert.Empty(t, got[2], "a race with no warship class has no entry")
}

func TestUnit_RaceFleet_CollectRaceAnchors_LowestIDBuiltRaceFiltered(t *testing.T) {
	t.Parallel()
	statics := map[domain.SectorID]domain.SectorStatics{
		1: {Stations: []domain.Station{
			{ID: 9, SectorID: 1, Race: 1, Built: true, Pos: domain.Vec2{X: 50}},
			{ID: 3, SectorID: 1, Race: 1, Built: true, Pos: domain.Vec2{X: 10}}, // lowest built → anchor
			{ID: 1, SectorID: 1, Race: 1, Built: false},                         // unbuilt → ignored
		}},
		25: {Stations: []domain.Station{
			{ID: 1568, SectorID: 25, Race: 7, Built: true}, // Xenon → excluded (>6)
		}},
		21: {Pirbases: []domain.Pirbase{
			{ID: 1, SectorID: 21, Race: 6, Built: true, Pos: domain.Vec2{X: -5}},
		}},
	}
	got := collectRaceAnchors(statics)
	require.Len(t, got[1], 1)
	assert.Equal(t, int64(3), got[1][0].ref.ID, "lowest-id built station is the anchor")
	assert.Equal(t, domain.Vec2{X: 10}, got[1][0].pos)
	require.Len(t, got[6], 1, "pirate pirbase is a valid anchor")
	assert.Equal(t, domain.EntityKindPirbase, got[6][0].ref.Kind)
	assert.NotContains(t, got, 7, "Xenon (race 7) is not seeded — invasion only")
}

func TestUnit_RaceFleet_DistributeEven(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []int{2, 2, 1}, distributeEven(5, 3, 10), "5 across 3 bins fills evenly")
	assert.Equal(t, []int{3, 3}, distributeEven(10, 2, 3), "ceiling 3 caps each bin — 6 placed, 4 dropped")
	assert.Equal(t, []int{0, 0}, distributeEven(0, 2, 5), "no budget → empty bins")
	assert.Empty(t, distributeEven(5, 0, 5), "no bins → nothing placed")
}

func TestUnit_RaceFleet_PlanRaceFleets_BudgetCapAndClassRotation(t *testing.T) {
	t.Parallel()
	anchorA := anchorRef{ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 3}, sector: 1, pos: domain.Vec2{X: 10}}
	anchorB := anchorRef{ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 7}, sector: 2, pos: domain.Vec2{X: 20}}
	anchors := map[int][]anchorRef{1: {anchorA, anchorB}}
	classes := map[int][]balance.ShipClass{1: {
		{ID: 75, Race: 1, Class: 3}, // M3
		{ID: 76, Race: 1, Class: 4}, // M4
		{ID: 78, Race: 1, Class: 6}, // M6
	}}
	cfg := RaceFleetConfig{WarshipsPerRace: map[int]int{1: 5}, MaxPerSector: 10}

	plans := planRaceFleets(anchors, classes, nil, cfg)
	require.Len(t, plans, 5, "budget 5 fully placed across 2 anchors")

	var anchorAClasses []int
	for _, p := range plans {
		assert.Equal(t, 1, p.race, "all spawned ships are race 1")
		if p.anchor.ref.ID == 3 {
			anchorAClasses = append(anchorAClasses, p.class.Class)
		}
	}
	// distributeEven(5,2,10) = [3,2] → anchor A gets idx 0,1,2 → M3,M4,M6.
	assert.Equal(t, []int{3, 4, 6}, anchorAClasses, "classes rotate M3/M4/M6 by per-anchor index")
}

func TestUnit_RaceFleet_PlanRaceFleets_TopUpSkipsServed(t *testing.T) {
	t.Parallel()
	anchor := anchorRef{ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 3}, sector: 1}
	anchors := map[int][]anchorRef{1: {anchor}}
	classes := map[int][]balance.ShipClass{1: {{ID: 75, Race: 1, Class: 3}}}
	cfg := RaceFleetConfig{WarshipsPerRace: map[int]int{1: 4}, MaxPerSector: 10}
	served := map[npcshipsrepo.HomeKind]int{
		{Home: anchor.ref, Kind: race.Kind}: 3, // 3 already alive
	}

	plans := planRaceFleets(anchors, classes, served, cfg)
	assert.Len(t, plans, 1, "quota 4 minus 3 served → top up exactly 1")
}

func TestUnit_RaceFleet_PlanRaceFleets_NoClassesSkips(t *testing.T) {
	t.Parallel()
	anchors := map[int][]anchorRef{1: {{ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 3}, sector: 1}}}
	cfg := RaceFleetConfig{WarshipsPerRace: map[int]int{1: 5}, MaxPerSector: 10}
	plans := planRaceFleets(anchors, map[int][]balance.ShipClass{}, nil, cfg)
	assert.Empty(t, plans, "a race with no warship classes spawns nothing")
}

func TestUnit_RaceFleet_NewWarship_RaceAndCatalogStats(t *testing.T) {
	t.Parallel()
	s := &raceFleetSpawner{ship: ShipSpawnerConfig{}.withDefaults()}
	p := warshipSpawn{
		race:   6,
		anchor: anchorRef{sector: 21, pos: domain.Vec2{X: 100, Y: 50}},
		class:  balance.ShipClass{Class: 6, Hull: 66000, Shield: 161000, ShieldCharge: 1950, Speed: 23.8, Acceleration: 8.33, Laser: 10000},
		idx:    2,
	}
	ship := s.newWarship(7, p)
	assert.Equal(t, domain.RaceID(6), ship.Race)
	assert.Equal(t, domain.PlayerID(7), ship.PlayerID)
	assert.Equal(t, domain.SectorID(21), ship.SectorID)
	assert.Equal(t, 66000, ship.HP)
	assert.Equal(t, 66000, ship.MaxHP)
	assert.Equal(t, 161000, ship.Shield)
	assert.Equal(t, 1950, ship.ShieldRecharge)
	assert.Equal(t, 1000, ship.LaserDamage, "catalog Laser 10000 / divisor 10")
	assert.Equal(t, 110.0, ship.Pos.X, "anchor x + idx*5")
	assert.Greater(t, ship.Energy, ship.LaserEnergyCost, "energy pool sustains fire")
}

// Ties the spawner output to the runtime hostility oracle (9.1): a pirate
// warship and an Argon Navy ship spawned by newWarship are mutually hostile by
// the race matrix, while same-race Navy ships are not — the "pirate attacks
// Argon Navy and vice versa; Navy spares its own race" acceptance, as a unit.
func TestUnit_RaceFleet_SpawnedShips_HostileByRaceMatrix(t *testing.T) {
	t.Parallel()
	s := &raceFleetSpawner{ship: ShipSpawnerConfig{}.withDefaults()}
	m3 := balance.ShipClass{Class: 3}
	argon := s.newWarship(1, warshipSpawn{race: 1, class: m3})
	argon2 := s.newWarship(1, warshipSpawn{race: 1, class: m3})
	pirate := s.newWarship(1, warshipSpawn{race: 6, class: m3})

	tg := raceMatrixTargeter{}
	assert.True(t, tg.IsHostile(pirate, argon), "pirate attacks Argon Navy")
	assert.True(t, tg.IsHostile(argon, pirate), "Argon Navy attacks the pirate")
	assert.False(t, tg.IsHostile(argon, argon2), "Navy spares its own race")
}
