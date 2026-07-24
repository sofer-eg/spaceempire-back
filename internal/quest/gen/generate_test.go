package gen

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// --- fakes ------------------------------------------------------------------

// fakeRouter answers Hops from a single player sector: `hops[to]` is the
// distance, a missing key means unreachable. Hops(x, x) is (0, true).
type fakeRouter struct{ hops map[domain.SectorID]int }

func (f fakeRouter) Hops(from, to domain.SectorID) (int, bool) {
	if from == to {
		return 0, true
	}
	h, ok := f.hops[to]
	return h, ok
}

// fakeMarket returns fixed listings, filtered to the requested sectors exactly
// as the real StaticMarket adapter does (it only queries reachable sectors).
type fakeMarket struct{ listings []MarketListing }

func (f fakeMarket) Listings(_ context.Context, sectors []domain.SectorID) ([]MarketListing, error) {
	set := make(map[domain.SectorID]bool, len(sectors))
	for _, s := range sectors {
		set[s] = true
	}
	var out []MarketListing
	for _, l := range f.listings {
		if set[l.Sector] {
			out = append(out, l)
		}
	}
	return out, nil
}

type fakeCatalog struct{ names map[domain.GoodsTypeID]string }

func (f fakeCatalog) Get(id domain.GoodsTypeID) (balance.Goods, bool) {
	n, ok := f.names[id]
	if !ok {
		return balance.Goods{}, false
	}
	return balance.Goods{ID: id, Name: n}, true
}

// --- test world -------------------------------------------------------------

const playerSector = domain.SectorID(10)

func station(id int64, sector domain.SectorID) domain.Station {
	return domain.Station{ID: domain.StationID(id), SectorID: sector}
}
func tradeStation(id int64, sector domain.SectorID) domain.TradeStation {
	return domain.TradeStation{ID: domain.TradeStationID(id), SectorID: sector}
}

// world builds the shared fixture: player at 10; sectors 11/12/13 reachable at
// 1/2/3 hops, 14 at 4 hops (outside R=3), 99 unreachable. Each populated sector
// has a dockable static. Returns the pieces plus a station->sector map so tests
// can resolve a step's Target to its sector.
func world() (map[domain.SectorID]domain.SectorStatics, fakeRouter, fakeMarket, fakeCatalog, map[domain.EntityRef]domain.SectorID) {
	statics := map[domain.SectorID]domain.SectorStatics{
		10: {Stations: []domain.Station{station(100, 10)}},                // player sector (hops 0, excluded)
		11: {Stations: []domain.Station{station(101, 11)}},                // 1 hop
		12: {Stations: []domain.Station{station(102, 12)}},                // 2 hops
		13: {TradeStations: []domain.TradeStation{tradeStation(201, 13)}}, // 3 hops
		14: {Stations: []domain.Station{station(141, 14)}},                // 4 hops (outside R)
		99: {Stations: []domain.Station{station(991, 99)}},                // unreachable
	}
	router := fakeRouter{hops: map[domain.SectorID]int{11: 1, 12: 2, 13: 3, 14: 4}}
	market := fakeMarket{listings: []MarketListing{
		{Station: station(101, 11).ObjectID(), Sector: 11, Goods: 7, CanBuy: true},  // buy good 7 @ 11
		{Station: station(102, 12).ObjectID(), Sector: 12, Goods: 7, CanSell: true}, // sell good 7 @ 12  -> trade pair
		{Station: tradeStation(201, 13).ObjectID(), Sector: 13, Goods: 4, CanBuy: true},
		{Station: tradeStation(201, 13).ObjectID(), Sector: 13, Goods: 5, CanSell: true},
		// Listings in out-of-radius / unreachable sectors must never surface.
		{Station: station(141, 14).ObjectID(), Sector: 14, Goods: 7, CanBuy: true, CanSell: true},
		{Station: station(991, 99).ObjectID(), Sector: 99, Goods: 7, CanBuy: true, CanSell: true},
	}}
	catalog := fakeCatalog{names: map[domain.GoodsTypeID]string{4: "Кристаллы", 5: "Микрочипы", 7: "Компьютеры"}}
	stationSector := map[domain.EntityRef]domain.SectorID{
		station(100, 10).ObjectID():      10,
		station(101, 11).ObjectID():      11,
		station(102, 12).ObjectID():      12,
		tradeStation(201, 13).ObjectID(): 13,
		station(141, 14).ObjectID():      14,
		station(991, 99).ObjectID():      99,
	}
	return statics, router, market, catalog, stationSector
}

func testConfig() Config {
	return Config{
		TargetRadius: 3, GoodsRadius: 3,
		RewardBase: 2000, RewardPerHop: 1500, RewardPerUnit: 300, RewardPerEnemy: 4000,
	}
}

// targetSectorsOf pulls every sector a Def sends the player to: goto-sector
// steps directly, and deliver/dock targets resolved through stationSector. An
// unresolvable target yields -1 so the assertion fails loudly.
func targetSectorsOf(def quest.Def, stationSector map[domain.EntityRef]domain.SectorID) []domain.SectorID {
	var out []domain.SectorID
	for _, st := range def.Steps {
		if st.Kind == quest.StepGotoSector {
			out = append(out, st.Sector)
		}
		if (st.Kind == quest.StepDeliver || st.Kind == quest.StepDockAt) && st.Target.Kind != domain.EntityKindUnknown {
			if sec, ok := stationSector[st.Target]; ok {
				out = append(out, sec)
			} else {
				out = append(out, -1)
			}
		}
	}
	return out
}

// --- AC-2: reachability -----------------------------------------------------

func TestUnit_Generate_TargetsAlwaysReachableWithinRadius(t *testing.T) {
	t.Parallel()
	statics, router, market, catalog, stationSector := world()
	g := New(router, market, catalog, statics, rand.New(rand.NewSource(1)), testConfig())

	reachable := map[domain.SectorID]bool{11: true, 12: true, 13: true}
	kinds := map[string]int{}
	for i := 0; i < 100; i++ {
		kind, def, ok, err := g.Generate(context.Background(), playerSector)
		require.NoError(t, err)
		require.True(t, ok, "iteration %d must produce an offer", i)
		kinds[kind]++
		for _, sec := range targetSectorsOf(def, stationSector) {
			assert.Truef(t, reachable[sec], "iter %d kind %s: target sector %d must be reachable within radius", i, kind, sec)
		}
	}
	// All four templates must be exercised so the AC covers every path.
	for _, k := range []string{TemplateDeliver, TemplateTrade, TemplateKill, TemplateCourier} {
		assert.Positivef(t, kinds[k], "expected at least one %s over 100 generations", k)
	}
}

// --- AC-3: tradeable goods --------------------------------------------------

func TestUnit_Generate_TradeGoodsAlwaysTradeableInRadius(t *testing.T) {
	t.Parallel()
	statics, router, market, catalog, _ := world()
	g := New(router, market, catalog, statics, rand.New(rand.NewSource(7)), testConfig())

	// Ground truth from the fixture, restricted to the reachable radius.
	reachable := map[domain.SectorID]bool{11: true, 12: true, 13: true}
	buyable := map[domain.GoodsTypeID]bool{}
	sellable := map[domain.GoodsTypeID]bool{}
	for _, l := range market.listings {
		if !reachable[l.Sector] {
			continue
		}
		if l.CanBuy {
			buyable[l.Goods] = true
		}
		if l.CanSell {
			sellable[l.Goods] = true
		}
	}

	for i := 0; i < 200; i++ {
		kind, def, ok, err := g.Generate(context.Background(), playerSector)
		require.NoError(t, err)
		require.True(t, ok)
		switch kind {
		case TemplateDeliver:
			good := def.Steps[0].Goods
			assert.Truef(t, buyable[good], "iter %d deliver good %d must be buyable in radius", i, good)
			assert.Equalf(t, good, def.Steps[1].Goods, "iter %d deliver goods must match across steps", i)
		case TemplateTrade:
			good := def.Steps[0].Goods
			assert.Truef(t, buyable[good], "iter %d trade good %d must be buyable in radius", i, good)
			assert.Truef(t, sellable[good], "iter %d trade good %d must be sellable in radius", i, good)
		}
	}
}

// --- AC-3b: fail-closed -----------------------------------------------------

func TestUnit_Generate_EmptyMarket_OnlyKillOrCourier(t *testing.T) {
	t.Parallel()
	statics, router, _, catalog, _ := world()
	g := New(router, fakeMarket{}, catalog, statics, rand.New(rand.NewSource(3)), testConfig())

	sawKill, sawCourier := false, false
	for i := 0; i < 100; i++ {
		kind, _, ok, err := g.Generate(context.Background(), playerSector)
		require.NoError(t, err)
		require.True(t, ok)
		require.Contains(t, []string{TemplateKill, TemplateCourier}, kind, "iter %d: no market must fall back to kill/courier", i)
		sawKill = sawKill || kind == TemplateKill
		sawCourier = sawCourier || kind == TemplateCourier
	}
	assert.True(t, sawKill, "kill fallback must appear")
	assert.True(t, sawCourier, "courier fallback must appear")
}

func TestUnit_Generate_EmptyMarketNoDockables_OnlyKill(t *testing.T) {
	t.Parallel()
	_, router, _, catalog, _ := world()
	// Reachable sectors exist as statics keys but carry no dockable statics.
	statics := map[domain.SectorID]domain.SectorStatics{11: {}, 12: {}, 13: {}}
	g := New(router, fakeMarket{}, catalog, statics, rand.New(rand.NewSource(5)), testConfig())

	for i := 0; i < 30; i++ {
		kind, _, ok, err := g.Generate(context.Background(), playerSector)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equalf(t, TemplateKill, kind, "iter %d: only kill is feasible with no market and no dockables", i)
	}
}

func TestUnit_Generate_NoReachableSectors_NotOK(t *testing.T) {
	t.Parallel()
	statics, _, market, catalog, _ := world()
	// Router that reaches nothing.
	g := New(fakeRouter{hops: map[domain.SectorID]int{}}, market, catalog, statics, rand.New(rand.NewSource(9)), testConfig())

	kind, _, ok, err := g.Generate(context.Background(), playerSector)
	require.NoError(t, err)
	assert.False(t, ok, "no reachable sector must fail closed")
	assert.Empty(t, kind)
}

// --- FR-09: reward ----------------------------------------------------------

func TestUnit_Reward_MonotonicAndFlooredAtBase(t *testing.T) {
	t.Parallel()
	g := New(nil, nil, nil, nil, nil, testConfig())

	// Floor: zero of everything is exactly base.
	assert.EqualValues(t, 2000, g.reward(0, 0, 0))

	// Monotonic in each dimension.
	assert.Greater(t, g.reward(2, 0, 0), g.reward(1, 0, 0))
	assert.Greater(t, g.reward(0, 20, 0), g.reward(0, 10, 0))
	assert.Greater(t, g.reward(0, 0, 3), g.reward(0, 0, 2))

	// Full formula: 2000 + 1500*3 + 300*10 + 4000*2 = 17500.
	assert.EqualValues(t, 17500, g.reward(3, 10, 2))
}

// --- MR2 resolver compatibility: Def JSON round-trips -----------------------

func TestUnit_Generate_DefJSONRoundTrip(t *testing.T) {
	t.Parallel()
	statics, router, market, catalog, _ := world()
	g := New(router, market, catalog, statics, rand.New(rand.NewSource(11)), testConfig())

	for i := 0; i < 50; i++ {
		_, def, ok, err := g.Generate(context.Background(), playerSector)
		require.NoError(t, err)
		require.True(t, ok)

		raw, err := json.Marshal(def)
		require.NoError(t, err)
		var back quest.Def
		require.NoError(t, json.Unmarshal(raw, &back))
		require.Equal(t, def, back, "iter %d: Def must survive the resolver's marshal/unmarshal", i)
	}
}

// --- determinism ------------------------------------------------------------

func TestUnit_Generate_DeterministicUnderFixedSeed(t *testing.T) {
	t.Parallel()
	statics, router, market, catalog, _ := world()
	g1 := New(router, market, catalog, statics, rand.New(rand.NewSource(42)), testConfig())
	g2 := New(router, market, catalog, statics, rand.New(rand.NewSource(42)), testConfig())

	for i := 0; i < 50; i++ {
		k1, d1, ok1, err1 := g1.Generate(context.Background(), playerSector)
		k2, d2, ok2, err2 := g2.Generate(context.Background(), playerSector)
		require.NoError(t, err1)
		require.NoError(t, err2)
		require.Equal(t, ok1, ok2)
		require.Equal(t, k1, k2, "iter %d kind diverged", i)
		require.Equal(t, d1, d2, "iter %d def diverged", i)
	}
}
