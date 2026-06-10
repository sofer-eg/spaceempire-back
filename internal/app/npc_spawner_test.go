package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai/passenger"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/world"
)

// linearTopology builds sectors 1-2-3 connected by two gates (1↔2, 2↔3) plus
// an isolated sector 9, and returns a router over it.
func linearTopology(t *testing.T) *world.PathRouter {
	t.Helper()
	sectors := []domain.Sector{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"}, {ID: 9, Name: "Z"}}
	gates := []domain.Gate{
		{ID: 1, SectorA: 1, PosA: domain.Vec2{X: 100}, SectorB: 2, PosB: domain.Vec2{X: -100}},
		{ID: 2, SectorA: 2, PosA: domain.Vec2{X: 100}, SectorB: 3, PosB: domain.Vec2{X: -100}},
	}
	return world.NewPathRouter(world.New(sectors, gates), nil)
}

func tradeStation(id int64, sector domain.SectorID, x float64) tradeStationRef {
	return tradeStationRef{
		sector: sector,
		pos:    domain.Vec2{X: x},
		ref:    domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: id},
		id:     id,
	}
}

func TestUnit_NPCSpawner_NearestTradeStation_FewestHops(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	candidates := []tradeStationRef{
		tradeStation(30, 3, 0), // 2 hops from sector 1
		tradeStation(20, 2, 0), // 1 hop
		tradeStation(10, 1, 0), // same sector, 0 hops
	}
	got, ok := s.nearestTradeStation(1, candidates)
	require.True(t, ok)
	assert.Equal(t, int64(10), got.id, "must pick the same-sector (0-hop) trade station")
}

func TestUnit_NPCSpawner_NearestTradeStation_TieBreaksByID(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	// Two trade stations in the same sector (both 0 hops) → lowest id wins.
	candidates := []tradeStationRef{
		tradeStation(50, 1, 0),
		tradeStation(40, 1, 0),
	}
	got, ok := s.nearestTradeStation(1, candidates)
	require.True(t, ok)
	assert.Equal(t, int64(40), got.id)
}

func TestUnit_NPCSpawner_NearestTradeStation_Unreachable(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	// Only candidate sits in the isolated sector 9 → no route from sector 1.
	_, ok := s.nearestTradeStation(1, []tradeStationRef{tradeStation(90, 9, 0)})
	assert.False(t, ok, "an unreachable trade station must not be selected")
}

func TestUnit_NPCSpawner_CollectFactories_DeterministicOrder(t *testing.T) {
	t.Parallel()
	statics := map[domain.SectorID]domain.SectorStatics{
		3: {Stations: []domain.Station{{ID: 5, SectorID: 3}}},
		1: {Stations: []domain.Station{{ID: 2, SectorID: 1}, {ID: 1, SectorID: 1}}},
	}
	got := collectFactories(statics)
	require.Len(t, got, 3)
	// Sorted by (sector, id): (1,1), (1,2), (3,5) — independent of map order.
	assert.Equal(t, domain.StationID(1), got[0].ID)
	assert.Equal(t, domain.StationID(2), got[1].ID)
	assert.Equal(t, domain.StationID(5), got[2].ID)
}

func asteroidCand(id int64, sector domain.SectorID, ore domain.GoodsTypeID) asteroidRef {
	return asteroidRef{id: domain.AsteroidID(id), sector: sector, oreType: ore}
}

func TestUnit_NPCSpawner_NearestAsteroid_FewestHops(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	const ore = domain.GoodsTypeID(2)
	candidates := []asteroidRef{
		asteroidCand(30, 3, ore), // 2 hops from sector 1
		asteroidCand(20, 2, ore), // 1 hop
		asteroidCand(10, 1, ore), // same sector, 0 hops
	}
	got, ok := s.nearestAsteroid(1, ore, candidates)
	require.True(t, ok)
	assert.Equal(t, domain.AsteroidID(10), got.ID, "must pick the same-sector (0-hop) asteroid")
}

func TestUnit_NPCSpawner_NearestAsteroid_TieBreaksByID(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	const ore = domain.GoodsTypeID(2)
	got, ok := s.nearestAsteroid(1, ore, []asteroidRef{
		asteroidCand(50, 1, ore),
		asteroidCand(40, 1, ore),
	})
	require.True(t, ok)
	assert.Equal(t, domain.AsteroidID(40), got.ID)
}

func TestUnit_NPCSpawner_NearestAsteroid_SkipsWrongOre(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	// The nearest asteroid (0 hops) is the wrong ore; the right ore is 1 hop.
	got, ok := s.nearestAsteroid(1, 2, []asteroidRef{
		asteroidCand(10, 1, 99), // wrong ore, same sector
		asteroidCand(20, 2, 2),  // right ore, 1 hop
	})
	require.True(t, ok)
	assert.Equal(t, domain.AsteroidID(20), got.ID, "must skip the wrong-ore asteroid")
}

func TestUnit_NPCSpawner_NearestAsteroid_Unreachable(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t)}
	// Only candidate sits in the isolated sector 9 → no route from sector 1.
	_, ok := s.nearestAsteroid(1, 2, []asteroidRef{asteroidCand(90, 9, 2)})
	assert.False(t, ok, "an unreachable asteroid must not be selected")
}

func TestUnit_NPCSpawner_CollectAsteroids_Flattens(t *testing.T) {
	t.Parallel()
	asteroids := map[domain.SectorID][]domain.Asteroid{
		1: {{ID: 1, SectorID: 1, OreType: 2}},
		2: {{ID: 2, SectorID: 2, OreType: 8}, {ID: 3, SectorID: 2, OreType: 2}},
	}
	got := collectAsteroids(asteroids)
	require.Len(t, got, 3)
	byID := map[domain.AsteroidID]asteroidRef{}
	for _, a := range got {
		byID[a.id] = a
	}
	assert.Equal(t, domain.SectorID(2), byID[3].sector)
	assert.Equal(t, domain.GoodsTypeID(8), byID[2].oreType)
}

func pDest(id int64, kind domain.EntityKind, sector domain.SectorID, race int) passengerDest {
	return passengerDest{sector: sector, ref: domain.EntityRef{Kind: kind, ID: id}, race: race}
}

func TestUnit_NPCSpawner_PassengerPool_FiltersByRadiusAndRace(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t), cfg: NPCSpawnerConfig{PassengerRouteRadius: 2}}
	dests := []passengerDest{
		pDest(1, domain.EntityKindTradeStation, 1, 1), // home sector, 0 hops, civilian
		pDest(10, domain.EntityKindStation, 2, 2),     // 1 hop, civilian
		pDest(20, domain.EntityKindStation, 3, 1),     // 2 hops, civilian
		pDest(30, domain.EntityKindStation, 9, 1),     // unreachable (isolated sector)
		pDest(40, domain.EntityKindStation, 2, 6),     // 1 hop but pirate race → skipped
	}
	pool := s.passengerPool(1, dests)
	require.Len(t, pool, 3, "civilian, reachable, within radius only")
	ids := map[int64]bool{}
	for _, leg := range pool {
		ids[leg.Ref.ID] = true
	}
	assert.True(t, ids[1] && ids[10] && ids[20])
	assert.False(t, ids[30], "unreachable sector excluded")
	assert.False(t, ids[40], "non-civilian race excluded")
}

func TestUnit_NPCSpawner_PassengerPool_RadiusExcludesFar(t *testing.T) {
	t.Parallel()
	s := &npcSpawner{router: linearTopology(t), cfg: NPCSpawnerConfig{PassengerRouteRadius: 1}}
	dests := []passengerDest{
		pDest(1, domain.EntityKindTradeStation, 1, 1), // 0 hops
		pDest(10, domain.EntityKindStation, 2, 1),     // 1 hop
		pDest(20, domain.EntityKindStation, 3, 1),     // 2 hops → out of radius 1
	}
	pool := s.passengerPool(1, dests)
	require.Len(t, pool, 2, "radius 1 keeps only 0- and 1-hop destinations")
}

func TestUnit_NPCSpawner_CollectPassengerDests_Flattens(t *testing.T) {
	t.Parallel()
	statics := map[domain.SectorID]domain.SectorStatics{
		1: {
			Stations:      []domain.Station{{ID: 5, SectorID: 1, Race: 1}},
			TradeStations: []domain.TradeStation{{ID: 7, SectorID: 1, Race: 2}},
		},
		2: {Stations: []domain.Station{{ID: 6, SectorID: 2, Race: 3}}},
	}
	got := collectPassengerDests(statics)
	require.Len(t, got, 3)
	byRef := map[domain.EntityRef]passengerDest{}
	for _, d := range got {
		byRef[d.ref] = d
	}
	assert.Equal(t, 1, byRef[domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}].race)
	assert.Equal(t, 2, byRef[domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 7}].race)
}

func TestUnit_NPCSpawner_PoolHasOtherThan(t *testing.T) {
	t.Parallel()
	home := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}
	other := domain.EntityRef{Kind: domain.EntityKindStation, ID: 10}
	assert.False(t, poolHasOtherThan([]passenger.Leg{{Ref: home}}, home), "only home → nowhere to go")
	assert.True(t, poolHasOtherThan([]passenger.Leg{{Ref: home}, {Ref: other}}, home))
}

func TestUnit_NPCSpawner_CollectTradeStations_Flattens(t *testing.T) {
	t.Parallel()
	statics := map[domain.SectorID]domain.SectorStatics{
		1: {TradeStations: []domain.TradeStation{{ID: 7, SectorID: 1, Pos: domain.Vec2{X: 0, Y: 0}}}},
		2: {TradeStations: []domain.TradeStation{{ID: 8, SectorID: 2, Pos: domain.Vec2{X: 5, Y: 5}}}},
	}
	got := collectTradeStations(statics)
	require.Len(t, got, 2)
	byID := map[int64]tradeStationRef{}
	for _, ts := range got {
		byID[ts.id] = ts
	}
	assert.Equal(t, domain.EntityKindTradeStation, byID[7].ref.Kind)
	assert.Equal(t, domain.SectorID(2), byID[8].sector)
}
