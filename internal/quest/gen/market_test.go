package gen

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/trade"
)

// fakeOwnerReader stubs the trade repository's batch read. It records the call
// count and the owners it was asked for, and returns a fixed row set regardless
// of the argument, so the adapter's own sector-scoping is what gets exercised.
type fakeOwnerReader struct {
	entries   []trade.MarketEntry
	calls     int
	gotOwners []domain.EntityRef
}

func (f *fakeOwnerReader) ListMarketsByOwners(_ context.Context, owners []domain.EntityRef) ([]trade.MarketEntry, error) {
	f.calls++
	f.gotOwners = owners
	return f.entries, nil
}

func priceOf(v int64) *int64 { return &v }

// TestUnit_StaticMarket_Listings locks the CanBuy/CanSell projection and the
// owner->sector tagging that AC-3 relies on — the real adapter that every
// generator test bypasses via fakeMarket. A CanBuy/CanSell swap or a broken
// owner->sector map would slip past the generator tests but fail here.
func TestUnit_StaticMarket_Listings(t *testing.T) {
	t.Parallel()
	st := station(101, 11)      // sector 11
	ts := tradeStation(201, 12) // sector 12
	rogue := station(999, 99)   // owner not in the requested sectors
	statics := map[domain.SectorID]domain.SectorStatics{
		11: {Stations: []domain.Station{st}},
		12: {TradeStations: []domain.TradeStation{ts}},
	}
	reader := &fakeOwnerReader{entries: []trade.MarketEntry{
		// station sells good 7 and has stock -> the player can BUY it here.
		{Owner: st.ObjectID(), GoodsType: 7, SellPrice: priceOf(100), Stock: 5},
		// station sells good 8 but stock is 0 -> NOT buyable.
		{Owner: st.ObjectID(), GoodsType: 8, SellPrice: priceOf(100), Stock: 0},
		// trade station buys good 9 (BuyPrice set, no sell) -> the player can SELL it here.
		{Owner: ts.ObjectID(), GoodsType: 9, BuyPrice: priceOf(50)},
		// a row for an owner outside the requested sectors must be dropped.
		{Owner: rogue.ObjectID(), GoodsType: 7, BuyPrice: priceOf(1), SellPrice: priceOf(1), Stock: 9},
	}}

	m := NewStaticMarket(statics, reader)
	got, err := m.Listings(context.Background(), []domain.SectorID{11, 12})
	require.NoError(t, err)
	require.Equal(t, 1, reader.calls, "one batch query")
	assert.ElementsMatch(t, []domain.EntityRef{st.ObjectID(), ts.ObjectID()}, reader.gotOwners,
		"adapter must ask only for the requested sectors' station owners")

	type key struct {
		sector domain.SectorID
		goods  domain.GoodsTypeID
	}
	by := map[key]MarketListing{}
	for _, l := range got {
		by[key{l.Sector, l.Goods}] = l
	}

	// good 7 @ sector 11: buyable (sells + stock), not sellable.
	if l, ok := by[key{11, 7}]; assert.True(t, ok, "good 7 in sector 11 present") {
		assert.True(t, l.CanBuy, "sells + stock => CanBuy")
		assert.False(t, l.CanSell, "no BuyPrice => not CanSell")
		assert.Equal(t, st.ObjectID(), l.Station)
	}
	// good 8 @ sector 11: sells but zero stock => not buyable.
	if l, ok := by[key{11, 8}]; assert.True(t, ok, "good 8 in sector 11 present") {
		assert.False(t, l.CanBuy, "zero stock => not CanBuy")
		assert.False(t, l.CanSell)
	}
	// good 9 @ sector 12: buys => sellable, not buyable.
	if l, ok := by[key{12, 9}]; assert.True(t, ok, "good 9 in sector 12 present") {
		assert.True(t, l.CanSell, "BuyPrice => CanSell")
		assert.False(t, l.CanBuy, "no SellPrice => not CanBuy")
	}
	// the rogue owner's row (sector 99) must never surface.
	for _, l := range got {
		assert.NotEqualf(t, domain.SectorID(99), l.Sector, "owner outside requested sectors must be skipped: %+v", l)
	}
}

// TestUnit_StaticMarket_NoStations_NoDBHit verifies the adapter never queries
// the database when the requested sectors carry no dockable stations (an empty
// owner set), so a bootstrap/isolated player costs zero market reads.
func TestUnit_StaticMarket_NoStations_NoDBHit(t *testing.T) {
	t.Parallel()
	statics := map[domain.SectorID]domain.SectorStatics{11: {}} // sector present but no dockables
	reader := &fakeOwnerReader{entries: []trade.MarketEntry{
		{Owner: station(1, 11).ObjectID(), GoodsType: 7, SellPrice: priceOf(1), Stock: 1},
	}}

	m := NewStaticMarket(statics, reader)
	got, err := m.Listings(context.Background(), []domain.SectorID{11, 42}) // 42 absent from statics
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Equal(t, 0, reader.calls, "no owners => no DB query")
}
