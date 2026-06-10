package production

import (
	"context"
	"errors"
	"fmt"
	"time"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// Repo is the persistence surface a Service tick needs. Production runs on the
// station market (station_goods), not a separate cargo hold: a station's
// recipe inputs are its buy rows and the output is its sell row, so a finished
// cycle replenishes exactly the stock players buy and drains exactly the stock
// they sell. The implementation lives in tx_runner.go (RepoTxRunner) and
// composes persistence/trade + persistence/stations behind one bound executor.
//
// Unit tests substitute an in-memory stub; see service_test.go.
type Repo interface {
	ListMarket(ctx context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error)
	AdjustStock(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error)
	UpdateProduction(ctx context.Context, id domain.StationID, inProgress bool, nextCycleAt time.Time) error
}

// TxRunner executes fn inside a database transaction and supplies a Repo
// bound to that transaction. One Tick spawns one transaction per station
// that changes state (start, finish, or "stuck" recheck); independent
// stations cannot corrupt each other on rollback.
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error
}

// Service owns the per-tick production logic for one sector. A single
// instance is safe to call from many sector tick goroutines because each
// call only touches the stations slice passed in.
type Service struct {
	bal *balance.Balance
	tx  TxRunner
}

// New wires a Service. Both deps must be non-nil; New returns a sentinel
// error otherwise so wiring bugs surface at startup, not on the first
// tick.
func New(bal *balance.Balance, tx TxRunner) (*Service, error) {
	if bal == nil {
		return nil, ErrNoBalance
	}
	if tx == nil {
		return nil, ErrNoTxRunner
	}
	return &Service{bal: bal, tx: tx}, nil
}

// Tick advances the production state of every station in stations whose
// type has a registered recipe. The slice is mutated in place — the
// caller (sector worker) owns the backing array and re-publishes it in
// the next snapshot.
//
// Returns the number of cycles that completed successfully on this tick;
// callers feed it into Prometheus / WorkerStats. Errors from individual
// stations are logged via the returned multi-error wrapper but do not
// abort the loop — one bad station must not stall the rest of the sector.
func (s *Service) Tick(ctx context.Context, stations []domain.Station, now time.Time) (int, error) {
	var cycles int
	var errs []error
	for i := range stations {
		st := &stations[i]
		if !st.Built {
			continue
		}
		recipe, ok := s.bal.Recipe(st.Type)
		if !ok {
			continue
		}

		freeInput := freeInputProduction(st, recipe)
		switch {
		case st.InProgress && !now.Before(st.NextCycleAt):
			done, err := s.finishCycle(ctx, st, recipe, freeInput)
			if err != nil {
				errs = append(errs, fmt.Errorf("station %d finish: %w", st.ID, err))
				continue
			}
			if done {
				cycles++
			}
		case !st.InProgress:
			if err := s.maybeStartCycle(ctx, st, recipe, now, freeInput); err != nil {
				errs = append(errs, fmt.Errorf("station %d start: %w", st.ID, err))
			}
		}
	}
	if len(errs) > 0 {
		return cycles, errors.Join(errs...)
	}
	return cycles, nil
}

// maybeStartCycle attempts to flip an idle station into InProgress. The
// transaction reads current cargo, checks every input + output cap, and —
// on success — writes the new station row. RAM-side fields are updated
// only after commit so a rollback leaves the in-memory station idle.
func (s *Service) maybeStartCycle(ctx context.Context, st *domain.Station, recipe balance.Recipe, now time.Time, freeInput bool) error {
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(st.ID)}
	nextCycleAt := now.Add(recipe.CycleTime)

	return s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		market, err := loadMarket(ctx, txRepo, stationRef)
		if err != nil {
			return err
		}
		// NPC power plants start regardless of inputs (9.2); everyone else
		// needs the full input stack on hand.
		if !freeInput && !hasInputs(market, recipe.Inputs) {
			return nil
		}
		if !hasOutputRoom(market, recipe.Outputs) {
			return nil
		}
		if err := txRepo.UpdateProduction(ctx, st.ID, true, nextCycleAt); err != nil {
			return err
		}
		st.InProgress = true
		st.NextCycleAt = nextCycleAt
		return nil
	})
}

// finishCycle completes an in-progress cycle: consumes inputs, produces
// outputs, clears the in_progress flag. Returns true when goods were
// actually moved (the second return value distinguishes "cycle ended,
// cycles_total++" from "still stuck, no goods moved").
//
// If inputs disappeared between start and finish (player hauled the
// stack away mid-cycle), the cycle stays InProgress and waits for the
// resources to return. NextCycleAt is left untouched on purpose so the
// recheck happens every tick rather than after another full cycle.
func (s *Service) finishCycle(ctx context.Context, st *domain.Station, recipe balance.Recipe, freeInput bool) (bool, error) {
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(st.ID)}

	var produced bool
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		market, err := loadMarket(ctx, txRepo, stationRef)
		if err != nil {
			return err
		}
		// A non-free station waits if its inputs vanished mid-cycle; an NPC
		// power plant produces anyway, draining only what crystals are present.
		if !freeInput && !hasInputs(market, recipe.Inputs) {
			return nil
		}
		// Recheck output room before touching stock: if the sell rows are full
		// the cycle waits (stays InProgress) rather than committing a
		// consume-without-produce. Every AdjustStock below is pre-validated, so
		// a non-nil error rolls back the whole transaction — never a partial.
		if !hasOutputRoom(market, recipe.Outputs) {
			return nil
		}
		for _, in := range recipe.Inputs {
			take := in.Quantity
			if freeInput {
				take = min(market[in.GoodsType].Stock, in.Quantity) // consume only what's in stock
			}
			if take <= 0 {
				continue
			}
			if _, err := txRepo.AdjustStock(ctx, stationRef, in.GoodsType, -take); err != nil {
				return fmt.Errorf("consume input %d: %w", in.GoodsType, err)
			}
		}
		for _, out := range recipe.Outputs {
			if _, err := txRepo.AdjustStock(ctx, stationRef, out.GoodsType, out.Quantity); err != nil {
				return fmt.Errorf("produce output %d: %w", out.GoodsType, err)
			}
		}
		if err := txRepo.UpdateProduction(ctx, st.ID, false, time.Time{}); err != nil {
			return err
		}
		produced = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if produced {
		st.InProgress = false
		st.NextCycleAt = time.Time{}
	}
	return produced, nil
}

// loadMarket indexes a station's station_goods rows by good. A missing key
// (zero MarketEntry: Stock 0, MaxStock 0) means the station does not trade
// that good, which the input/output checks read as "no stock / no room".
func loadMarket(ctx context.Context, txRepo Repo, owner domain.EntityRef) (map[domain.GoodsTypeID]traderepo.MarketEntry, error) {
	entries, err := txRepo.ListMarket(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("list market: %w", err)
	}
	out := make(map[domain.GoodsTypeID]traderepo.MarketEntry, len(entries))
	for _, e := range entries {
		out[e.GoodsType] = e
	}
	return out, nil
}

// energyGoodsID is the base energy good (Батарейки, goods id 1 in
// configs/balance.yaml) — the root of every production chain.
const energyGoodsID domain.GoodsTypeID = 1

// freeInputProduction reports whether a station produces unconditionally,
// ignoring missing inputs (phase 9.2). True only for an NPC-owned power plant
// (OwnerID nil = unowned/NPC) whose recipe outputs energy. This bootstraps the
// NPC economy: NPC power plants always make energy (consuming crystals only
// when present), so traders have stock to haul and the rest of the chain runs.
// Player-owned plants and all non-base NPC factories keep the normal input gate.
func freeInputProduction(st *domain.Station, recipe balance.Recipe) bool {
	if st.OwnerID != nil {
		return false
	}
	for _, out := range recipe.Outputs {
		if out.GoodsType == energyGoodsID {
			return true
		}
	}
	return false
}

func hasInputs(market map[domain.GoodsTypeID]traderepo.MarketEntry, inputs []balance.RecipeLine) bool {
	for _, in := range inputs {
		if market[in.GoodsType].Stock < in.Quantity {
			return false
		}
	}
	return true
}

// hasOutputRoom reports whether every output's sell row exists and can absorb
// one more cycle without breaching its max_stock cap. A missing sell row
// (MaxStock 0) means the station cannot sell that output, so it cannot produce.
func hasOutputRoom(market map[domain.GoodsTypeID]traderepo.MarketEntry, outputs []balance.RecipeLine) bool {
	for _, out := range outputs {
		e, ok := market[out.GoodsType]
		if !ok {
			return false
		}
		if e.Stock+out.Quantity > e.MaxStock {
			return false
		}
	}
	return true
}
