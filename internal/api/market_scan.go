package api

import (
	"context"
	"net/http"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/trade"
)

// tradeUpModuleType is the equipment key (ct_updates.type) of the trade-info
// scanner module. A ship carrying it at level L reveals sector prices to depth
// L (1 tier-only, 2 +prices, 3 +stock); 4 (forecast) is a separate task.
const tradeUpModuleType = "trade_up"

// handleMarketScan implements GET /api/market-scan (phase 10.3.12): the
// trade_up sector price-scanner. It resolves the player's active ship, reads
// its trade_up level and its sector from RAM, then lists the goods on offer at
// every tradeable station in that sector, masking detail by level. Without the
// module (level 0) it returns 403 — the only way to see foreign stations'
// prices without docking is to fit one.
func (s *Server) handleMarketScan(w http.ResponseWriter, r *http.Request) {
	if s.trade == nil {
		writeError(w, http.StatusServiceUnavailable, "trade not available")
		return
	}
	if s.goods == nil {
		writeError(w, http.StatusServiceUnavailable, "goods catalog not available")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())
	shipID, ok := s.resolveActiveShip(r.Context(), playerID)
	if !ok {
		writeError(w, http.StatusBadRequest, "no active ship")
		return
	}
	ship, sectorID, ok := s.lookupShip(shipID)
	if !ok {
		writeError(w, http.StatusBadRequest, "active ship not in world")
		return
	}
	level := equipmentLevel(ship.Equipment, tradeUpModuleType)
	if level <= 0 {
		writeError(w, http.StatusForbidden, "no trade scanner fitted")
		return
	}

	statics := s.router.Snapshot(sectorID).Statics
	resp := dto.ScanResponse{Level: level, Stations: s.scanStations(r.Context(), statics, level)}
	writeJSON(w, http.StatusOK, resp)
}

// scanStations builds the per-station price boards for every tradeable static
// in statics, at the given trade_up level. Stations whose market read fails or
// that offer nothing are skipped — a scan is a best-effort overview, not a
// transaction.
func (s *Server) scanStations(ctx context.Context, statics domain.SectorStatics, level int) []dto.ScanStation {
	out := make([]dto.ScanStation, 0, len(statics.Stations)+len(statics.TradeStations)+len(statics.Pirbases))
	for _, st := range statics.Stations {
		owner := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(st.ID)}
		// st.Type is the station_types catalog id; the SPA resolves the precise
		// type name from it so several factories in one sector are distinct.
		if scan, ok := s.scanOne(ctx, owner, "Станция", st.Type, st.Pos, level); ok {
			out = append(out, scan)
		}
	}
	for _, ts := range statics.TradeStations {
		owner := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: int64(ts.ID)}
		// TradeStation.Type is the central/ring classification, not a catalog id
		// — leave StationType 0 so the SPA labels it by kind.
		if scan, ok := s.scanOne(ctx, owner, "Торговая станция", 0, ts.Pos, level); ok {
			out = append(out, scan)
		}
	}
	for _, pb := range statics.Pirbases {
		owner := domain.EntityRef{Kind: domain.EntityKindPirbase, ID: int64(pb.ID)}
		if scan, ok := s.scanOne(ctx, owner, "Пиратская база", 0, pb.Pos, level); ok {
			out = append(out, scan)
		}
	}
	return out
}

// scanOne reads one station's market and projects it onto a ScanStation at the
// given level. stationType is the station_types catalog id for a production
// station (0 for trade-stations / pirbases). ok=false when the station offers
// nothing or the read errors.
func (s *Server) scanOne(ctx context.Context, owner domain.EntityRef, name string, stationType int, pos domain.Vec2, level int) (dto.ScanStation, bool) {
	entries, err := s.trade.Market(ctx, owner)
	if err != nil {
		s.logger.Warn("market scan: station read", "kind", int(owner.Kind), "id", owner.ID, "err", err)
		return dto.ScanStation{}, false
	}
	goods := make([]dto.ScanGood, 0, len(entries))
	for _, e := range entries {
		// A row with neither a buy nor a sell price is a degenerate market state
		// (ref would be 0 and the tier meaningless) — skip it rather than emit a
		// bogus comparison cell.
		if e.BuyPrice == nil && e.SellPrice == nil {
			continue
		}
		goods = append(goods, s.scanGood(e, level))
	}
	if len(goods) == 0 {
		return dto.ScanStation{}, false
	}
	return dto.ScanStation{
		Owner:       dto.EntityRef{Kind: int(owner.Kind), ID: owner.ID},
		Name:        name,
		StationType: stationType,
		Pos:         dto.ScanPos{X: pos.X, Y: pos.Y},
		Goods:       goods,
	}, true
}

// scanGood projects one market entry to a ScanGood, masking by level. The
// price tier (level 1+) is computed against the good's band from the catalog;
// the real prices (level 2+) and stock (level 3) are revealed or left 0. The
// tier uses whichever direction the station offers — sell price for a factory
// product, buy price for raw materials — so flat trade-station/pirbase wares
// (one shared price) tier off that single value.
func (s *Server) scanGood(e traderepo.MarketEntry, level int) dto.ScanGood {
	var ref int64
	if e.SellPrice != nil {
		ref = *e.SellPrice
	} else if e.BuyPrice != nil {
		ref = *e.BuyPrice
	}
	var avg, max int64
	if g, ok := s.goodsBands(e.GoodsType); ok {
		avg, max = g.AvgPrice, g.MaxPrice
	}
	good := dto.ScanGood{
		TypeID:     int32(e.GoodsType),
		PriceLevel: trade.PriceTier(ref, avg, max),
	}
	if level >= 2 {
		if e.BuyPrice != nil {
			good.BuyPrice = *e.BuyPrice
		}
		if e.SellPrice != nil {
			good.SellPrice = *e.SellPrice
		}
	}
	if level >= 3 {
		good.Stock = e.Stock
	}
	return good
}

// resolveActiveShip returns the player's controlled ship id: the explicit
// active_ship_id when set and present in RAM, otherwise the lowest-id ship the
// player owns. ok=false when the player has no ship in the world (observer /
// just-died with no respawn yet) — callers turn that into a 400. Mirrors the
// WS subscribe resolution (initialSubscribeSector) minus the passenger branch:
// trade is the player's own action, run from their own ship.
func (s *Server) resolveActiveShip(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool) {
	if playerID == 0 {
		return 0, false
	}
	if s.activeShips != nil {
		if shipID, ok, err := s.activeShips.ActiveShip(ctx, playerID); err == nil && ok && shipID != 0 {
			if _, found := s.router.LookupShipSector(shipID); found {
				return shipID, true
			}
		}
	}
	if shipID, _, ok := s.router.LookupPrimaryShipByPlayer(playerID); ok {
		return shipID, true
	}
	return 0, false
}

// lookupShip finds the ship by id and returns a copy of the domain.Ship plus
// its sector. It locates the hosting sector via the router, then scans that
// sector's snapshot for the row (so it can read the ship's equipment, which
// LookupShipSector alone does not surface). ok=false when no worker holds it.
func (s *Server) lookupShip(shipID domain.ShipID) (domain.Ship, domain.SectorID, bool) {
	sectorID, found := s.router.LookupShipSector(shipID)
	if !found {
		return domain.Ship{}, 0, false
	}
	snap := s.router.Snapshot(sectorID)
	for i := range snap.Ships {
		if snap.Ships[i].ID == shipID {
			return snap.Ships[i], sectorID, true
		}
	}
	return domain.Ship{}, 0, false
}

// goodsBands reads a good's price band from the catalog for the scanner's
// tier classification. False when the catalog is unset or the id is unknown.
func (s *Server) goodsBands(id domain.GoodsTypeID) (balance.Goods, bool) {
	if s.goods == nil {
		return balance.Goods{}, false
	}
	return s.goods.Get(id)
}

// equipmentLevel returns the install level of the first module of the given
// type fitted on the ship, or 0 when none is fitted.
func equipmentLevel(eq []domain.InstalledEquipment, moduleType string) int {
	for _, m := range eq {
		if m.Type == moduleType {
			return m.Level
		}
	}
	return 0
}
