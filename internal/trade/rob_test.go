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
	t.Helper()
	repo := newStubRepo()
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3}
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
