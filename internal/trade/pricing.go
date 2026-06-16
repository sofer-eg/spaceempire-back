package trade

import (
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// priceFor returns the live unit price for a good at the given stock level.
// Commodity goods — those with a real avg<max band in the balance catalog —
// are priced dynamically: a full shelf trades at avg_price, an empty one at
// max_price, linearly in between, so scarcity raises the price and a glut
// lowers it (the same direction whether the station sells the good or buys
// it). Goods without a band (special items such as slaves, avg==max==0) keep
// the fixed column price their market row was seeded with.
func (s *Service) priceFor(gtype domain.GoodsTypeID, columnPrice, stock, maxStock int64) int64 {
	if s.bal == nil {
		return columnPrice
	}
	g, ok := s.bal.Get(gtype)
	if !ok {
		return columnPrice
	}
	dyn, ok := dynamicUnitPrice(g, stock, maxStock)
	if !ok {
		return columnPrice
	}
	return dyn
}

// PriceTier classifies a unit price as "high", "medium" or "low" relative to a
// good's [avg, max] price band (phase 10.3.12 sector price-scanner). The band's
// width is split into thirds: a price at or above the top third is "high", the
// bottom third "low", the middle "medium". Goods without a usable band
// (avg<=0 or max<=avg — flat-priced trade-station / pirbase wares such as
// slaves) are classified relative to AvgPrice alone: at or above avg is "high",
// below is "low". This is the only signal level-1 trade_up reveals — the real
// price and stock are masked to 0 until level 2/3.
func PriceTier(price, avg, max int64) string {
	if avg <= 0 || max <= avg {
		if price >= avg {
			return "high"
		}
		return "low"
	}
	band := max - avg
	lowCut := avg + band/3
	highCut := avg + 2*band/3
	switch {
	case price >= highCut:
		return "high"
	case price >= lowCut:
		return "medium"
	default:
		return "low"
	}
}

// dynamicUnitPrice interpolates the price across the good's [avg, max] band by
// how empty the shelf is. It reports false when the good has no usable band
// (avg<=0, max<=avg) or no capacity, leaving the caller on the column price.
func dynamicUnitPrice(g balance.Goods, stock, maxStock int64) (int64, bool) {
	avg, max := g.AvgPrice, g.MaxPrice
	if avg <= 0 || max <= avg || maxStock <= 0 {
		return 0, false
	}
	if stock < 0 {
		stock = 0
	}
	if stock > maxStock {
		stock = maxStock
	}
	// Full stock (stock==maxStock) → avg_price; empty (stock==0) → max_price.
	price := avg + (max-avg)*(maxStock-stock)/maxStock
	if price < 1 {
		price = 1
	}
	return price, true
}
