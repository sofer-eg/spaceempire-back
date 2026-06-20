package production

import (
	"spaceempire/back/internal/domain"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// StationForecast projects a factory station's stock `cycles` production cycles
// into the future (phase 10.3.22, trade_up level 4). It is a deterministic
// dry-run of the per-tick production loop (Service.Tick): each simulated cycle
// applies the same input/output gates (hasInputs / hasOutputRoom) and the same
// consume-then-produce arithmetic, but against a private copy of the market — it
// touches no persistence and no live state.
//
// Returns the projected stock per good (every good the station's market lists)
// and how many cycles completed before the station stalls. A station whose type
// has no recipe (trade station / pirbase) yields ok=false; the caller then omits
// the forecast block. starvation (a missing input) or a full output shelf stops
// the simulation early, so `completed` < cycles signals a chain that will idle.
func (r *Reader) StationForecast(st domain.Station, entries []traderepo.MarketEntry, cycles int) (projected map[domain.GoodsTypeID]int64, completed int, ok bool) {
	recipe, has := r.bal.Recipe(st.Type)
	if !has {
		return nil, 0, false
	}
	freeInput := freeInputProduction(&st, recipe)

	// Private copy of the market keyed by good, mutated in place across the
	// simulated cycles — never the live rows.
	sim := make(map[domain.GoodsTypeID]traderepo.MarketEntry, len(entries))
	for _, e := range entries {
		sim[e.GoodsType] = e
	}

	for c := 0; c < cycles; c++ {
		// Same gates as the live tick: a non-free station needs its full input
		// stack, and every output shelf must have room for one more cycle.
		if !freeInput && !hasInputs(sim, recipe.Inputs) {
			break
		}
		if !hasOutputRoom(sim, recipe.Outputs) {
			break
		}
		for _, in := range recipe.Inputs {
			take := in.Quantity
			if freeInput {
				take = min(sim[in.GoodsType].Stock, in.Quantity)
			}
			e := sim[in.GoodsType]
			e.Stock -= take
			sim[in.GoodsType] = e
		}
		for _, out := range recipe.Outputs {
			e := sim[out.GoodsType]
			e.Stock += out.Quantity
			sim[out.GoodsType] = e
		}
		completed++
	}

	projected = make(map[domain.GoodsTypeID]int64, len(sim))
	for g, e := range sim {
		projected[g] = e.Stock
	}
	return projected, completed, true
}
