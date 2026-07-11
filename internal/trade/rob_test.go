package trade_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/trade"
)

// robFixture wires a trade station (owner_kind 4) with two goods and a hacker
// ship with a hold, so the richest-good pick + loot deposit can be asserted.
type robFixture struct {
	repo    *stubRepo
	svc     *trade.Service
	station domain.EntityRef
	ship    domain.EntityRef
}

func newRobFixture(t *testing.T, shipCapacity float64, goods map[domain.GoodsTypeID]traderepo.MarketEntry) *robFixture {
	return newRobFixtureKind(t, domain.EntityKindTradeStation, shipCapacity, goods)
}

// newRobFixtureKind wires a station of the given kind. For a trade station it
// also seeds a production reference equal to each good's own max_stock, so the
// hack basis (production ref, TASK-128) matches the entry cap and the deduct/
// deposit assertions read the same as before the basis split. Tests that need a
// divergent reference (resale cap != production ref) override repo.prodRef.
func newRobFixtureKind(t *testing.T, kind domain.EntityKind, shipCapacity float64, goods map[domain.GoodsTypeID]traderepo.MarketEntry) *robFixture {
	t.Helper()
	repo := newStubRepo()
	station := domain.EntityRef{Kind: kind, ID: 3}
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[ship] = shipCapacity
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Iron", Space: 1}
	for gid, e := range goods {
		e.Owner = station
		e.GoodsType = gid
		if repo.market[station] == nil {
			repo.market[station] = map[domain.GoodsTypeID]traderepo.MarketEntry{}
		}
		repo.market[station][gid] = e
		if kind == domain.EntityKindTradeStation {
			repo.prodRef[gid] = e.MaxStock
		}
	}
	return &robFixture{
		repo:    repo,
		svc:     trade.New(repo, inlineTx{repo: repo}, priceBalance(t)),
		station: station,
		ship:    ship,
	}
}

// The richest good (highest stock) is robbed: 15%+5% leaves its stock, and at
// deposit-to-hold the robbed units land in the hold in the same transaction.
func TestUnit_Service_Rob_RichestGood_DeductsAndDeposits(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		1: {Stock: 100, MaxStock: 1000},  // not the richest
		2: {Stock: 1000, MaxStock: 1000}, // richest → target
	})

	out, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, true)
	require.NoError(t, err)

	assert.EqualValues(t, 2, out.GoodsType, "richest good targeted")
	assert.EqualValues(t, 150, out.Robbed)
	assert.EqualValues(t, 50, out.Damaged)
	assert.True(t, out.Delivered)
	assert.EqualValues(t, 1000, out.MaxStock)

	assert.EqualValues(t, 800, f.repo.market[f.station][2].Stock, "robbed+damaged removed from stock")
	assert.EqualValues(t, 100, f.repo.market[f.station][1].Stock, "other goods untouched")
	assert.EqualValues(t, 150, f.repo.stacks[f.ship][2], "loot deposited to the hold")
}

// The richest good below the min fraction of its max → ErrTooLittleGoods.
func TestUnit_Service_Rob_TooLittleGoods(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		2: {Stock: 200, MaxStock: 1000}, // 20% < 30%
	})

	_, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, true)
	assert.ErrorIs(t, err, trade.ErrTooLittleGoods)
	assert.EqualValues(t, 200, f.repo.market[f.station][2].Stock, "nothing deducted on reject")
}

// depositToHold=false (up_hack level 1) deducts stock but never touches the hold.
func TestUnit_Service_Rob_NoDeposit_DeductsOnly(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		2: {Stock: 1000, MaxStock: 1000},
	})

	out, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, false)
	require.NoError(t, err)
	assert.False(t, out.Delivered)
	assert.EqualValues(t, 150, out.Robbed)
	assert.EqualValues(t, 800, f.repo.market[f.station][2].Stock)
	assert.Empty(t, f.repo.stacks[f.ship], "hold untouched")
}

// A full hold falls back to a container: stock is deducted but Delivered=false so
// the worker drops the loot in space.
func TestUnit_Service_Rob_HoldFull_NotDelivered(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 100, map[domain.GoodsTypeID]traderepo.MarketEntry{
		2: {Stock: 1000, MaxStock: 1000}, // robbed 150 > capacity 100
	})

	out, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, true)
	require.NoError(t, err)
	assert.False(t, out.Delivered, "loot does not fit → caller drops a container")
	assert.EqualValues(t, 150, out.Robbed)
	assert.EqualValues(t, 800, f.repo.market[f.station][2].Stock, "stock still deducted")
	assert.Empty(t, f.repo.stacks[f.ship], "hold untouched")
}

// AC-1: on a trade station the gate/penalty denominator is the production
// reference cap of the good, NOT the resale cap sitting in station_goods.max_stock
// (1e6). Good 2 holds 4000 units under a 1e6 resale cap but a 10000 production
// reference: 4000 >= 30% of 10000 passes (it would fail against the 1e6 cap:
// 4000 < 300000), and out.MaxStock reports the reference so the caller's penalty
// is computed from it.
func TestUnit_Service_Rob_TradeStation_UsesProductionReferenceAsDenominator(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		2: {Stock: 4000, MaxStock: 1_000_000}, // resale cap, not the basis
	})
	f.repo.prodRef[2] = 10000 // production reference cap for good 2

	out, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, false)
	require.NoError(t, err, "4000 >= 30% of the 10000 production reference")

	assert.EqualValues(t, 2, out.GoodsType)
	assert.EqualValues(t, 600, out.Robbed)  // floor(0.15*4000)
	assert.EqualValues(t, 200, out.Damaged) // floor(0.05*4000)
	assert.EqualValues(t, 10000, out.MaxStock, "denominator is the production reference, not the 1e6 resale cap")
	assert.EqualValues(t, 3200, f.repo.market[f.station][2].Stock)
}

// AC-2: a trade-station good that no factory produces (no production reference)
// is excluded from target selection — even when its stock is the fattest pile on
// the shelf. Good 301 (a gun) sits at 900000 with no reference; the robbable good
// 1 at 4000 is picked instead, so the unproduced heap neither gets robbed nor
// shadows the producible position behind an unpassable gate.
func TestUnit_Service_Rob_TradeStation_UnproducedGoodExcluded(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		1:   {Stock: 4000, MaxStock: 1_000_000},   // produced (ref 10000) → robbable
		301: {Stock: 900000, MaxStock: 1_000_000}, // a gun: fattest pile, no ref
	})
	f.repo.prodRef[1] = 10000
	delete(f.repo.prodRef, 301) // no factory produces guns → no reference

	out, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, false)
	require.NoError(t, err)

	assert.EqualValues(t, 1, out.GoodsType, "produced good targeted, not the fatter unproduced pile")
	assert.EqualValues(t, 600, out.Robbed)
	assert.EqualValues(t, 10000, out.MaxStock)
	assert.EqualValues(t, 3200, f.repo.market[f.station][1].Stock, "produced good robbed")
	assert.EqualValues(t, 900000, f.repo.market[f.station][301].Stock, "unproduced pile untouched")
}

// AC-5 (regression): a production station (owner_kind 2) keeps using its own
// max_stock as the basis and never consults the production reference map — a
// misleading prodRef entry is ignored, out.MaxStock is the good's own cap, and
// the deduct/deposit result is byte-identical to the pre-TASK-128 behaviour.
func TestUnit_Service_Rob_ProductionStation_UsesOwnMaxStock(t *testing.T) {
	t.Parallel()
	f := newRobFixtureKind(t, domain.EntityKindStation, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		2: {Stock: 1000, MaxStock: 1000},
	})
	f.repo.prodRef[2] = 999999 // must be ignored for a production station

	out, err := f.svc.Rob(context.Background(), f.station, f.ship, 0.15, 0.05, 0.3, true)
	require.NoError(t, err)

	assert.EqualValues(t, 2, out.GoodsType)
	assert.EqualValues(t, 150, out.Robbed)
	assert.EqualValues(t, 50, out.Damaged)
	assert.True(t, out.Delivered)
	assert.EqualValues(t, 1000, out.MaxStock, "own max_stock is the basis, prodRef ignored")
	assert.EqualValues(t, 800, f.repo.market[f.station][2].Stock)
	assert.EqualValues(t, 150, f.repo.stacks[f.ship][2])
}

// A non-station target is rejected before any transaction.
func TestUnit_Service_Rob_InvalidStationKind(t *testing.T) {
	t.Parallel()
	f := newRobFixture(t, 1000, map[domain.GoodsTypeID]traderepo.MarketEntry{
		2: {Stock: 1000, MaxStock: 1000},
	})

	_, err := f.svc.Rob(context.Background(),
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 9}, f.ship, 0.15, 0.05, 0.3, true)
	assert.ErrorIs(t, err, trade.ErrInvalidStationKind)
}
