package gen

import (
	"fmt"
	"sort"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// buildDeliver: buy Q of a good sold nearby, deliver it to a dockable station
// in a target sector. The buy leg is any-cargo (AcquireCargo), the delivery is
// bound to a concrete target station.
func (g *Generator) buildDeliver(from domain.SectorID, buyPoints map[domain.GoodsTypeID][]MarketListing, dockables []stationTarget) quest.Def {
	good := g.pickGood(buyPoints)
	qty := int64(g.randRange(deliverQtyMin, deliverQtyMax))
	dropoff := dockables[g.rng.Intn(len(dockables))]
	hops := g.hops(from, dropoff.sector)
	name := g.goodsName(good)
	reward := g.reward(hops, qty, 0)

	return quest.Def{
		ID:        TemplateDeliver,
		Title:     "Доставка груза",
		Offerable: true,
		Steps: []quest.Step{
			{Kind: quest.StepAcquireCargo, Qty: qty, Goods: good,
				Desc: fmt.Sprintf("Купи %d %s", qty, name)},
			{Kind: quest.StepDeliver, Goods: good, Count: qty, Target: dropoff.ref, RewardCash: reward,
				Desc: fmt.Sprintf("Доставь %d %s на станцию в сектор #%d", qty, name, int64(dropoff.sector))},
		},
	}
}

// buildTrade: buy Q of a good sold nearby, fly to a target sector where a
// station buys it, sell it there.
func (g *Generator) buildTrade(from domain.SectorID, buyPoints, sellPoints map[domain.GoodsTypeID][]MarketListing) quest.Def {
	good := g.pickTradeGood(buyPoints, sellPoints)
	qty := int64(g.randRange(tradeQtyMin, tradeQtyMax))
	dest := sellPoints[good][g.rng.Intn(len(sellPoints[good]))]
	hops := g.hops(from, dest.Sector)
	name := g.goodsName(good)
	reward := g.reward(hops, qty, 0)

	return quest.Def{
		ID:        TemplateTrade,
		Title:     "Торговый рейс",
		Offerable: true,
		Steps: []quest.Step{
			{Kind: quest.StepTrade, Side: "buy", Goods: good, Count: qty,
				Desc: fmt.Sprintf("Купи %d %s", qty, name)},
			{Kind: quest.StepGotoSector, Sector: dest.Sector,
				Desc: fmt.Sprintf("Прибудь в сектор #%d", int64(dest.Sector))},
			{Kind: quest.StepTrade, Side: "sell", Goods: good, Count: qty, RewardCash: reward,
				Desc: fmt.Sprintf("Продай %d %s в секторе #%d", qty, name, int64(dest.Sector))},
		},
	}
}

// buildKill: fly to a target sector and destroy N ships. No market needed —
// the count-based objective is the fallback that keeps a reachable player
// always offerable (enemy spawn is a separate balance concern, not this MR).
func (g *Generator) buildKill(from domain.SectorID, targets []domain.SectorID) quest.Def {
	dest := targets[g.rng.Intn(len(targets))]
	count := int64(g.randRange(killCountMin, killCountMax))
	hops := g.hops(from, dest)
	reward := g.reward(hops, 0, count)

	return quest.Def{
		ID:        TemplateKill,
		Title:     "Патруль сектора",
		Offerable: true,
		Steps: []quest.Step{
			{Kind: quest.StepGotoSector, Sector: dest,
				Desc: fmt.Sprintf("Прибудь в сектор #%d", int64(dest))},
			{Kind: quest.StepKill, Count: count, RewardCash: reward,
				Desc: fmt.Sprintf("Уничтожь %d кораблей", count)},
		},
	}
}

// buildCourier: fly to a target sector and dock at a specific station. No
// market needed — the second fail-closed fallback.
func (g *Generator) buildCourier(from domain.SectorID, dockables []stationTarget) quest.Def {
	dest := dockables[g.rng.Intn(len(dockables))]
	hops := g.hops(from, dest.sector)
	reward := g.reward(hops, 0, 0)

	return quest.Def{
		ID:        TemplateCourier,
		Title:     "Курьерский рейс",
		Offerable: true,
		Steps: []quest.Step{
			{Kind: quest.StepGotoSector, Sector: dest.sector,
				Desc: fmt.Sprintf("Прибудь в сектор #%d", int64(dest.sector))},
			{Kind: quest.StepDockAt, Target: dest.ref, RewardCash: reward,
				Desc: fmt.Sprintf("Пристыкуйся к станции в секторе #%d", int64(dest.sector))},
		},
	}
}

// reward applies the FR-09 formula, floored at base. With non-negative
// coefficients and hops >= 1 the floor is naturally met; the clamp documents
// the contract and guards a misconfigured negative coefficient.
func (g *Generator) reward(hops int, qty, enemies int64) int64 {
	r := g.cfg.RewardBase +
		g.cfg.RewardPerHop*int64(hops) +
		g.cfg.RewardPerUnit*qty +
		g.cfg.RewardPerEnemy*enemies
	if r < g.cfg.RewardBase {
		return g.cfg.RewardBase
	}
	return r
}

// hops is the gate distance to a sector already known reachable, so the router
// always resolves it; the ignored ok can only be false for an unreachable
// input, in which case the zero distance floors the reward at base.
func (g *Generator) hops(from, to domain.SectorID) int {
	n, _ := g.router.Hops(from, to)
	return n
}

// pickGood chooses a good uniformly from the map's keys, sorted first so the
// choice is reproducible under a fixed rng.
func (g *Generator) pickGood(m map[domain.GoodsTypeID][]MarketListing) domain.GoodsTypeID {
	keys := make([]domain.GoodsTypeID, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys[g.rng.Intn(len(keys))]
}

// pickTradeGood chooses a good present in both maps (buyable and sellable).
func (g *Generator) pickTradeGood(buy, sell map[domain.GoodsTypeID][]MarketListing) domain.GoodsTypeID {
	var keys []domain.GoodsTypeID
	for k := range buy {
		if len(sell[k]) > 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys[g.rng.Intn(len(keys))]
}

// goodsName resolves a display name, falling back to a numeric label when the
// good is not in the catalog.
func (g *Generator) goodsName(id domain.GoodsTypeID) string {
	if goods, ok := g.catalog.Get(id); ok {
		return goods.Name
	}
	return fmt.Sprintf("товар #%d", int64(id))
}

// randRange returns a uniform int in [min, max].
func (g *Generator) randRange(minVal, maxVal int) int {
	if maxVal <= minVal {
		return minVal
	}
	return minVal + g.rng.Intn(maxVal-minVal+1)
}
