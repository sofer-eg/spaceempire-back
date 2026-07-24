package gen

import (
	"context"
	"fmt"
	"math/rand"
	"sort"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// Generator builds procedural quest instances. Construct one per generation
// request (or reuse with a stable rng); it holds no mutable state of its own.
type Generator struct {
	router  PathRouter
	market  Market
	catalog GoodsCatalog
	statics map[domain.SectorID]domain.SectorStatics
	rng     *rand.Rand
	cfg     Config
}

// New wires a Generator. rng seeds all choices — inject a fixed-seed
// *rand.Rand in tests for determinism (the sector.WithRNG pattern).
func New(
	router PathRouter,
	market Market,
	catalog GoodsCatalog,
	statics map[domain.SectorID]domain.SectorStatics,
	rng *rand.Rand,
	cfg Config,
) *Generator {
	return &Generator{
		router:  router,
		market:  market,
		catalog: catalog,
		statics: statics,
		rng:     rng,
		cfg:     cfg,
	}
}

// Generate builds one procedural quest for a player at playerSector. It returns
// the template kind, the frozen Def, and ok. ok is false (with a nil error)
// when there is nothing solvable to offer — fail-closed (FR-03/AC-3b): no
// reachable target sector at all. A non-nil error is a real failure (market
// read). deliver/trade are only offered when the market supports them; kill is
// always available as long as one target sector is reachable, so a reachable
// player always gets at least a kill.
func (g *Generator) Generate(ctx context.Context, playerSector domain.SectorID) (string, quest.Def, bool, error) {
	targets := g.reachableSectors(playerSector, g.cfg.TargetRadius)
	if len(targets) == 0 {
		return "", quest.Def{}, false, nil
	}

	goodsSectors := g.reachableSectors(playerSector, g.cfg.GoodsRadius)
	listings, err := g.market.Listings(ctx, goodsSectors)
	if err != nil {
		return "", quest.Def{}, false, fmt.Errorf("read market: %w", err)
	}

	targetSet := make(map[domain.SectorID]struct{}, len(targets))
	for _, s := range targets {
		targetSet[s] = struct{}{}
	}

	// Index tradeable goods from the player's point of view. Buy points (the
	// player buys) may sit anywhere in the goods radius. Sell points (the player
	// sells) must sit in a target sector, because a trade quest's sell leg drives
	// a goto-sector target and every target must satisfy AC-2 (1..TargetRadius).
	buyPoints := map[domain.GoodsTypeID][]MarketListing{}
	sellPoints := map[domain.GoodsTypeID][]MarketListing{}
	for _, l := range listings {
		if l.CanBuy {
			buyPoints[l.Goods] = append(buyPoints[l.Goods], l)
		}
		if l.CanSell {
			if _, ok := targetSet[l.Sector]; ok {
				sellPoints[l.Goods] = append(sellPoints[l.Goods], l)
			}
		}
	}

	dockables := g.dockableTargets(targets)

	var feasible []string
	if len(buyPoints) > 0 && len(dockables) > 0 {
		feasible = append(feasible, TemplateDeliver)
	}
	if hasTradePair(buyPoints, sellPoints) {
		feasible = append(feasible, TemplateTrade)
	}
	feasible = append(feasible, TemplateKill) // a reachable target sector is enough
	if len(dockables) > 0 {
		feasible = append(feasible, TemplateCourier)
	}

	kind := feasible[g.rng.Intn(len(feasible))]
	switch kind {
	case TemplateDeliver:
		return kind, g.buildDeliver(playerSector, buyPoints, dockables), true, nil
	case TemplateTrade:
		return kind, g.buildTrade(playerSector, buyPoints, sellPoints), true, nil
	case TemplateCourier:
		return kind, g.buildCourier(playerSector, dockables), true, nil
	default: // TemplateKill
		return kind, g.buildKill(playerSector, targets), true, nil
	}
}

// reachableSectors returns the statics-known sectors within [1..radius] gate
// hops of `from`, sorted ascending for deterministic selection. Hops == 0 (the
// player's own sector) is excluded: every generated target is at least one jump
// away (anti-exploit floor, R-1; AC-2's 1 <= n <= R). Excluded/unreachable
// sectors fall out because Hops reports them as (_, false).
func (g *Generator) reachableSectors(from domain.SectorID, radius int) []domain.SectorID {
	var out []domain.SectorID
	for s := range g.statics {
		hops, ok := g.router.Hops(from, s)
		if !ok || hops < 1 || hops > radius {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// stationTarget is a dockable static (station or trade-station) and its sector,
// used as a deliver dropoff or a courier destination.
type stationTarget struct {
	ref    domain.EntityRef
	sector domain.SectorID
}

// dockableTargets flattens the dockable statics of the given sectors, sorted by
// (kind, id) for deterministic selection.
func (g *Generator) dockableTargets(sectors []domain.SectorID) []stationTarget {
	var out []stationTarget
	for _, s := range sectors {
		st := g.statics[s]
		for _, station := range st.Stations {
			out = append(out, stationTarget{ref: station.ObjectID(), sector: s})
		}
		for _, ts := range st.TradeStations {
			out = append(out, stationTarget{ref: ts.ObjectID(), sector: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ref.Kind != out[j].ref.Kind {
			return out[i].ref.Kind < out[j].ref.Kind
		}
		return out[i].ref.ID < out[j].ref.ID
	})
	return out
}

// hasTradePair reports whether some good can be bought at one point and sold at
// another within radius — the precondition for a trade quest.
func hasTradePair(buy, sell map[domain.GoodsTypeID][]MarketListing) bool {
	for good := range buy {
		if len(sell[good]) > 0 {
			return true
		}
	}
	return false
}
