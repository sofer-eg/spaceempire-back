package gen

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/trade"
)

// OwnerMarketReader is the slice of the trade repository the market adapter
// needs: read every market row for a set of owners in one query. Satisfied by
// *trade.Repository.
type OwnerMarketReader interface {
	ListMarketsByOwners(ctx context.Context, owners []domain.EntityRef) ([]trade.MarketEntry, error)
}

// StaticMarket is the real Market: it resolves the dockable-station owners in
// the requested sectors from the sector statics, reads their market rows in one
// query, and projects each row into a MarketListing tagged with its sector.
type StaticMarket struct {
	statics map[domain.SectorID]domain.SectorStatics
	reader  OwnerMarketReader
}

// NewStaticMarket wires the adapter over the immutable statics map and the
// trade repository.
func NewStaticMarket(statics map[domain.SectorID]domain.SectorStatics, reader OwnerMarketReader) *StaticMarket {
	return &StaticMarket{statics: statics, reader: reader}
}

// Listings returns the tradeable positions across the given sectors. An empty
// or station-less sector set yields no listings without hitting the database.
func (m *StaticMarket) Listings(ctx context.Context, sectors []domain.SectorID) ([]MarketListing, error) {
	ownerSector := make(map[domain.EntityRef]domain.SectorID)
	var owners []domain.EntityRef
	for _, s := range sectors {
		st := m.statics[s]
		for _, station := range st.Stations {
			ref := station.ObjectID()
			ownerSector[ref] = s
			owners = append(owners, ref)
		}
		for _, ts := range st.TradeStations {
			ref := ts.ObjectID()
			ownerSector[ref] = s
			owners = append(owners, ref)
		}
	}
	if len(owners) == 0 {
		return nil, nil
	}

	entries, err := m.reader.ListMarketsByOwners(ctx, owners)
	if err != nil {
		return nil, fmt.Errorf("list markets by owners: %w", err)
	}

	out := make([]MarketListing, 0, len(entries))
	for _, e := range entries {
		sector, ok := ownerSector[e.Owner]
		if !ok {
			continue // owner outside the requested sectors — defensive skip
		}
		out = append(out, MarketListing{
			Station: e.Owner,
			Sector:  sector,
			Goods:   e.GoodsType,
			CanBuy:  e.SellPrice != nil && e.Stock > 0,
			CanSell: e.BuyPrice != nil,
		})
	}
	return out, nil
}
