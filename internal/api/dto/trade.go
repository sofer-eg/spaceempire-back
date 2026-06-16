package dto

import (
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/production"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// MarketEntry mirrors traderepo.MarketEntry on the wire. Nullable prices
// become pointers so the JSON faithfully distinguishes "0 credits" (never
// used in seed data) from "not on offer". A missing price is emitted as
// explicit JSON null (NOT omitted): the SPA classifies a factory good as
// «продукция» vs «сырьё» by which price is null, and an omitted field would
// arrive as undefined and break that test (both sections would list it).
type MarketEntry struct {
	TypeID    int32  `json:"typeID"`
	BuyPrice  *int64 `json:"buyPrice"`
	SellPrice *int64 `json:"sellPrice"`
	Stock     int64  `json:"stock"`
	MaxStock  int64  `json:"maxStock"`
}

// MarketResponse is the body of GET /api/station/{id}/market and the two
// neighbouring trade-station / pirbase endpoints. Items may be empty when
// the station does not trade at all. Production is set only for producing
// factories (EntityKind.Station with a recipe); nil otherwise.
type MarketResponse struct {
	OwnerKind  int             `json:"ownerKind"`
	OwnerID    int64           `json:"ownerID"`
	Items      []MarketEntry   `json:"items"`
	Production *ProductionInfo `json:"production,omitempty"`
}

// ProductionInfo is the factory's production-cycle state for the market UI.
// SecondsRemaining counts down to the end of the in-progress cycle (0 when
// idle); CycleSeconds is the full recipe cycle length, so the SPA can draw
// a progress bar without a second request.
type ProductionInfo struct {
	InProgress       bool    `json:"inProgress"`
	SecondsRemaining float64 `json:"secondsRemaining"`
	CycleSeconds     float64 `json:"cycleSeconds"`
}

// ProductionInfoFromCycle maps a production.CycleInfo for the wire. Returns
// nil for a non-producing station type so the response omits the block.
// now anchors the SecondsRemaining countdown server-side, sidestepping
// client clock skew — the SPA just decrements locally from there.
func ProductionInfoFromCycle(info production.CycleInfo, now time.Time) *ProductionInfo {
	if !info.Produces {
		return nil
	}
	out := &ProductionInfo{
		InProgress:   info.InProgress,
		CycleSeconds: info.CycleTime.Seconds(),
	}
	if info.InProgress && !info.NextCycleAt.IsZero() {
		if remaining := info.NextCycleAt.Sub(now).Seconds(); remaining > 0 {
			out.SecondsRemaining = remaining
		}
	}
	return out
}

// MarketResponseFromRepo packages a list of repo rows for the wire. The
// owner is passed in because the repo rows carry it redundantly per-row.
func MarketResponseFromRepo(owner domain.EntityRef, entries []traderepo.MarketEntry) MarketResponse {
	items := make([]MarketEntry, 0, len(entries))
	for _, e := range entries {
		items = append(items, MarketEntry{
			TypeID:    int32(e.GoodsType),
			BuyPrice:  e.BuyPrice,
			SellPrice: e.SellPrice,
			Stock:     e.Stock,
			MaxStock:  e.MaxStock,
		})
	}
	return MarketResponse{
		OwnerKind: int(owner.Kind),
		OwnerID:   owner.ID,
		Items:     items,
	}
}

// ScanResponse is the body of GET /api/market-scan (phase 10.3.12). It lists
// the goods on offer at every tradeable station in the player's current sector,
// with detail gated by the active ship's trade_up module Level: 1 reveals only
// the price tier, 2 adds the real prices, 3 adds the on-hand stock. Masked
// numeric fields arrive as 0 (NOT null / omitted) — the SPA branches on Level,
// not on field presence, so the zeros are intentional and must serialize.
type ScanResponse struct {
	Level    int           `json:"level"`
	Stations []ScanStation `json:"stations"`
}

// ScanStation is one tradeable station's price board. Owner pins the static;
// Name is a generic per-kind fallback label; StationType is the station_types
// catalog id of a production station (0 for trade-stations / pirbases, whose
// Type field is not a catalog id) so the SPA can resolve a precise type name
// and tell several factories in one sector apart; Pos lets the SPA point the
// player at it on the map.
type ScanStation struct {
	Owner       EntityRef  `json:"owner"`
	Name        string     `json:"name"`
	StationType int        `json:"stationType"`
	Pos         ScanPos    `json:"pos"`
	Goods       []ScanGood `json:"goods"`
}

// ScanPos is the station's sector position.
type ScanPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ScanGood is one good's scanned line. PriceLevel ("high"/"medium"/"low") is
// always present (level 1+). BuyPrice/SellPrice are the real prices at level
// 2+, 0 below that. Stock is the on-hand quantity at level 3, 0 below that.
// The 0-masks are significant — see ScanResponse.
type ScanGood struct {
	TypeID     int32  `json:"typeID"`
	PriceLevel string `json:"priceLevel"`
	BuyPrice   int64  `json:"buyPrice"`
	SellPrice  int64  `json:"sellPrice"`
	Stock      int64  `json:"stock"`
}

// TradeBuyRequest is the body of POST /api/cmd/trade/buy.
type TradeBuyRequest struct {
	ShipID   int64     `json:"shipID"`
	Station  EntityRef `json:"station"`
	TypeID   int32     `json:"typeID"`
	Quantity int64     `json:"qty"`
}

// TradeBuyResponse acknowledges Buy with the post-trade snapshot the SPA
// needs to refresh the wallet and the market row without an extra GET.
type TradeBuyResponse struct {
	NewCash     int64 `json:"newCash"`
	NewStock    int64 `json:"newStock"`
	Moved       int64 `json:"moved"`
	UnitPrice   int64 `json:"unitPrice"`
	TotalAmount int64 `json:"totalAmount"`
}

// TradeSellRequest mirrors TradeBuyRequest for the sell direction.
type TradeSellRequest struct {
	ShipID   int64     `json:"shipID"`
	Station  EntityRef `json:"station"`
	TypeID   int32     `json:"typeID"`
	Quantity int64     `json:"qty"`
}

// TradeSellResponse mirrors TradeBuyResponse for the sell direction.
type TradeSellResponse struct {
	NewCash     int64 `json:"newCash"`
	NewStock    int64 `json:"newStock"`
	Moved       int64 `json:"moved"`
	UnitPrice   int64 `json:"unitPrice"`
	TotalAmount int64 `json:"totalAmount"`
}
