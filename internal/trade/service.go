package trade

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// Repo is the slice of persistence ops Service needs. Defined here per ISP
// so unit tests can stub it without pulling pgx into the test binary. The
// production wiring composes traderepo.Repository + playersrepo.Repository
// + cargorepo.Repository inside a single TxRunner.
type Repo interface {
	LoadShipDock(ctx context.Context, shipID domain.ShipID) (traderepo.ShipDock, error)

	GetMarketEntry(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) (traderepo.MarketEntry, error)
	ListMarket(ctx context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error)
	ProductionRefMaxes(ctx context.Context) (map[domain.GoodsTypeID]int64, error)
	AdjustStock(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error)

	GetCash(ctx context.Context, playerID domain.PlayerID) (int64, error)
	AdjustCash(ctx context.Context, playerID domain.PlayerID, delta int64) (int64, error)
	AddReputation(ctx context.Context, playerID domain.PlayerID, delta playersrepo.Reputation) (playersrepo.Reputation, error)

	GoodsType(ctx context.Context, id domain.GoodsTypeID) (domain.GoodsType, error)
	Capacity(ctx context.Context, owner domain.EntityRef) (float64, error)
	UsedSpace(ctx context.Context, owner domain.EntityRef) (float64, error)
	AddCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
	SubtractCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// TxRunner executes fn inside a database transaction and hands a Repo
// bound to that transaction. Production implementation lives in
// tx_runner.go (RepoTxRunner).
type TxRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error
}

// Service exposes the market read and the Buy/Sell mutations.
type Service struct {
	repo Repo
	tx   TxRunner
	bal  *balance.Balance
}

// New wires a Service. repo and tx are required; bal supplies the price bands
// for dynamic pricing (a nil bal degrades to the static column price).
func New(repo Repo, tx TxRunner, bal *balance.Balance) *Service {
	return &Service{repo: repo, tx: tx, bal: bal}
}

// MarketDocked returns the station's market, but only when the player's
// referenced ship is docked at that exact station — same guard Buy/Sell run
// (phase 10.3.12). The plain Market reader (below) is left ungated for the
// sector price-scanner, which authorizes through the trade_up module instead
// of physical presence. The dock check reads through the pool (no transaction):
// a stale dock here at worst denies a market the player just left.
func (s *Service) MarketDocked(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	if !isStationKind(owner.Kind) {
		return nil, ErrInvalidStationKind
	}
	if err := s.authorizeDocked(ctx, s.repo, playerID, shipID, owner); err != nil {
		return nil, err
	}
	return s.Market(ctx, owner)
}

// Market returns every goods entry the station offers (read-only), with the
// live price filled into each direction so the UI shows what a trade would
// actually cost at the current stock level. Reads use the pool directly — a
// market view is a snapshot, not a consistent view across other operations.
func (s *Service) Market(ctx context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	entries, err := s.repo.ListMarket(ctx, owner)
	if err != nil {
		if errors.Is(err, traderepo.ErrUnsupportedStationKind) {
			return nil, ErrInvalidStationKind
		}
		return nil, err
	}
	for i := range entries {
		e := &entries[i]
		if e.SellPrice != nil {
			p := s.unitPrice(owner.Kind, e.GoodsType, *e.SellPrice, e.Stock, e.MaxStock)
			e.SellPrice = &p
		}
		if e.BuyPrice != nil {
			p := s.unitPrice(owner.Kind, e.GoodsType, *e.BuyPrice, e.Stock, e.MaxStock)
			e.BuyPrice = &p
		}
	}
	return entries, nil
}

// unitPrice resolves the live trade price for one good. Production factories
// (EntityKindStation) price dynamically from stock fill (phase 10.18). Trade
// stations and pirbases run a universal market with the flat seeded column
// price — parity with the original StarWind Trade SP, where a good's price did
// not vary with the station's on-hand quantity (phase 10.19 follow-up).
func (s *Service) unitPrice(stationKind domain.EntityKind, gtype domain.GoodsTypeID, columnPrice, stock, maxStock int64) int64 {
	if stationKind == domain.EntityKindStation {
		return s.priceFor(gtype, columnPrice, stock, maxStock)
	}
	return columnPrice
}

// BuyResult is what Buy returns on success: the player's new cash balance
// and the station's new stock for the traded good. Both come from the
// authoritative UPDATE...RETURNING in the same transaction.
type BuyResult struct {
	NewCash     int64
	NewStock    int64
	UnitPrice   int64
	TotalAmount int64
}

// Buy transfers qty units of gtype from the station to the player's ship,
// debiting player cash and replenishing station stock. The whole operation
// runs in one transaction so a concurrent buy cannot oversell the stock
// or overdraw the wallet.
func (s *Service) Buy(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef, gtype domain.GoodsTypeID, qty int64) (BuyResult, error) {
	if qty <= 0 {
		return BuyResult{}, ErrNonPositiveQuantity
	}
	if !isStationKind(station.Kind) {
		return BuyResult{}, ErrInvalidStationKind
	}

	var result BuyResult
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		if err := s.authorizeDocked(ctx, txRepo, playerID, shipID, station); err != nil {
			return err
		}

		entry, err := txRepo.GetMarketEntry(ctx, station, gtype)
		if err != nil {
			if errors.Is(err, traderepo.ErrMarketEntryNotFound) {
				return ErrMarketEntryNotFound
			}
			if errors.Is(err, traderepo.ErrUnsupportedStationKind) {
				return ErrInvalidStationKind
			}
			return err
		}
		if entry.SellPrice == nil {
			return ErrStationDoesNotSell
		}
		if entry.Stock < qty {
			return ErrInsufficientStock
		}

		gt, err := txRepo.GoodsType(ctx, gtype)
		if err != nil {
			if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
				return ErrGoodsTypeNotFound
			}
			return err
		}

		shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(shipID)}
		capacity, err := txRepo.Capacity(ctx, shipRef)
		if err != nil {
			return fmt.Errorf("ship capacity: %w", err)
		}
		used, err := txRepo.UsedSpace(ctx, shipRef)
		if err != nil {
			return fmt.Errorf("ship used space: %w", err)
		}
		if used+float64(qty)*gt.Space > capacity {
			return ErrNoCargoSpace
		}

		unit := s.unitPrice(station.Kind, gtype, *entry.SellPrice, entry.Stock, entry.MaxStock)
		total := unit * qty
		newCash, err := txRepo.AdjustCash(ctx, playerID, -total)
		if err != nil {
			if errors.Is(err, playersrepo.ErrInsufficientCash) {
				return ErrInsufficientCash
			}
			return fmt.Errorf("debit player: %w", err)
		}

		newStock, err := txRepo.AdjustStock(ctx, station, gtype, -qty)
		if err != nil {
			if errors.Is(err, traderepo.ErrInsufficientStock) {
				return ErrInsufficientStock
			}
			return fmt.Errorf("decrement stock: %w", err)
		}

		if err := txRepo.AddCargo(ctx, shipRef, gtype, qty); err != nil {
			return fmt.Errorf("add cargo: %w", err)
		}

		if err := awardTradeReputation(ctx, txRepo, playerID, total); err != nil {
			return err
		}

		result = BuyResult{
			NewCash:     newCash,
			NewStock:    newStock,
			UnitPrice:   unit,
			TotalAmount: total,
		}
		return nil
	})
	if err != nil {
		return BuyResult{}, err
	}
	return result, nil
}

// SellResult mirrors BuyResult for the sell direction.
type SellResult struct {
	NewCash     int64
	NewStock    int64
	UnitPrice   int64
	TotalAmount int64
}

// Sell moves qty units of gtype from the ship to the station, crediting
// player cash and growing station stock. Mirrors Buy.
func (s *Service) Sell(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef, gtype domain.GoodsTypeID, qty int64) (SellResult, error) {
	if qty <= 0 {
		return SellResult{}, ErrNonPositiveQuantity
	}
	if !isStationKind(station.Kind) {
		return SellResult{}, ErrInvalidStationKind
	}

	var result SellResult
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		if err := s.authorizeDocked(ctx, txRepo, playerID, shipID, station); err != nil {
			return err
		}

		entry, err := txRepo.GetMarketEntry(ctx, station, gtype)
		if err != nil {
			if errors.Is(err, traderepo.ErrMarketEntryNotFound) {
				return ErrMarketEntryNotFound
			}
			if errors.Is(err, traderepo.ErrUnsupportedStationKind) {
				return ErrInvalidStationKind
			}
			return err
		}
		if entry.BuyPrice == nil {
			return ErrStationDoesNotBuy
		}
		if entry.Stock+qty > entry.MaxStock {
			return ErrStockOverflow
		}

		shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(shipID)}
		if err := txRepo.SubtractCargo(ctx, shipRef, gtype, qty); err != nil {
			if errors.Is(err, cargorepo.ErrInsufficientQuantity) {
				return ErrInsufficientCargo
			}
			return fmt.Errorf("subtract cargo: %w", err)
		}

		newStock, err := txRepo.AdjustStock(ctx, station, gtype, qty)
		if err != nil {
			if errors.Is(err, traderepo.ErrStockOverflow) {
				return ErrStockOverflow
			}
			return fmt.Errorf("increment stock: %w", err)
		}

		unit := s.unitPrice(station.Kind, gtype, *entry.BuyPrice, entry.Stock, entry.MaxStock)
		total := unit * qty
		newCash, err := txRepo.AdjustCash(ctx, playerID, total)
		if err != nil {
			return fmt.Errorf("credit player: %w", err)
		}

		if err := awardTradeReputation(ctx, txRepo, playerID, total); err != nil {
			return err
		}

		result = SellResult{
			NewCash:     newCash,
			NewStock:    newStock,
			UnitPrice:   unit,
			TotalAmount: total,
		}
		return nil
	})
	if err != nil {
		return SellResult{}, err
	}
	return result, nil
}

// RobOutcome is what Rob took from a station hack (TASK-100.3.9.3, SP UseHack).
// Robbed is the loot amount, Damaged the destroyed amount (both removed from the
// station's stock). Delivered reports whether Robbed was already added to the
// hacker's hold (level >= 2 with room) — when false the caller drops a loot
// container. MaxStock is the target good's hack basis (production station: its
// own max_stock; trade station: the production reference cap of that good), the
// denominator the caller uses for the reputation penalty.
type RobOutcome struct {
	GoodsType domain.GoodsTypeID
	Robbed    int64
	Damaged   int64
	Delivered bool
	MaxStock  int64
}

// Rob executes the market side of a station hack (SP UseHack, ЧТЗ FR-D3/D4). It
// targets the station's richest good (highest stock; tie-break lowest goods
// type), gates on that good holding >= minFrac of its max_stock (else
// ErrTooLittleGoods), then in one transaction removes robFrac+damageFrac of its
// stock and — when depositToHold and the hold has room — adds the robbed units
// to the hacker ship's hold. The clamp mirrors the SP: if robbed+damaged would
// exceed the stock, robbed is dropped to 0 (only the damage lands). Reputation
// is applied by the caller (racestanding), outside this transaction.
func (s *Service) Rob(ctx context.Context, station, hackerShip domain.EntityRef, robFrac, damageFrac, minFrac float64, depositToHold bool) (RobOutcome, error) {
	if !isStationKind(station.Kind) {
		return RobOutcome{}, ErrInvalidStationKind
	}
	var out RobOutcome
	err := s.tx.Do(ctx, func(ctx context.Context, txRepo Repo) error {
		entries, err := txRepo.ListMarket(ctx, station)
		if err != nil {
			if errors.Is(err, traderepo.ErrUnsupportedStationKind) {
				return ErrInvalidStationKind
			}
			return err
		}

		// prodRef is the production reference cap (max max_stock over owner_kind
		// 2) per good, the hack basis for a trade station whose own max_stock is a
		// 1e6 resale cap (migration 0044). Only trade stations consult it —
		// production stations and pirbases keep their own max_stock as basis, so
		// their behaviour is unchanged (TASK-128).
		var prodRef map[domain.GoodsTypeID]int64
		if station.Kind == domain.EntityKindTradeStation {
			prodRef, err = txRepo.ProductionRefMaxes(ctx)
			if err != nil {
				return fmt.Errorf("production ref maxes: %w", err)
			}
		}

		target, basis, ok := richestRobbable(entries, station.Kind, prodRef)
		if !ok || basis <= 0 ||
			float64(target.Stock) < minFrac*float64(basis) {
			return ErrTooLittleGoods
		}

		robbed := int64(robFrac * float64(target.Stock))
		damaged := int64(damageFrac * float64(target.Stock))
		if robbed+damaged > target.Stock {
			robbed = 0
		}
		if damaged > target.Stock {
			damaged = 0
		}

		if total := robbed + damaged; total > 0 {
			if _, err := txRepo.AdjustStock(ctx, station, target.GoodsType, -total); err != nil {
				return fmt.Errorf("rob adjust stock: %w", err)
			}
		}

		delivered := false
		if depositToHold && robbed > 0 {
			gt, err := txRepo.GoodsType(ctx, target.GoodsType)
			if err != nil {
				if errors.Is(err, cargorepo.ErrGoodsTypeNotFound) {
					return ErrGoodsTypeNotFound
				}
				return err
			}
			capacity, err := txRepo.Capacity(ctx, hackerShip)
			if err != nil {
				return fmt.Errorf("hacker capacity: %w", err)
			}
			used, err := txRepo.UsedSpace(ctx, hackerShip)
			if err != nil {
				return fmt.Errorf("hacker used space: %w", err)
			}
			if used+float64(robbed)*gt.Space <= capacity {
				if err := txRepo.AddCargo(ctx, hackerShip, target.GoodsType, robbed); err != nil {
					return fmt.Errorf("deposit loot: %w", err)
				}
				delivered = true
			}
		}

		out = RobOutcome{
			GoodsType: target.GoodsType,
			Robbed:    robbed,
			Damaged:   damaged,
			Delivered: delivered,
			MaxStock:  basis,
		}
		return nil
	})
	if err != nil {
		return RobOutcome{}, err
	}
	return out, nil
}

// hackBasis returns the gate/penalty denominator for one market entry and
// whether the entry is robbable at all. Production stations and pirbases use the
// good's own max_stock — a realistic on-hand ceiling — and every entry stays a
// candidate (the caller's basis <= 0 gate rejects a non-positive one), keeping
// their behaviour byte-identical to the pre-TASK-128 path. Trade stations use the
// production reference cap of the same good; a good with no reference — nobody
// produces it (guns, torpedoes, treasures, hyperfuel) — is not robbable there and
// is excluded from selection (TASK-128 AC-2).
func hackBasis(e traderepo.MarketEntry, kind domain.EntityKind, prodRef map[domain.GoodsTypeID]int64) (int64, bool) {
	if kind == domain.EntityKindTradeStation {
		ref, ok := prodRef[e.GoodsType]
		return ref, ok && ref > 0
	}
	return e.MaxStock, true
}

// richestRobbable picks the robbable market entry with the highest stock
// (tie-break: lowest goods type for determinism), returning it with its hack
// basis. Entries with no valid basis are skipped, so on a trade station a fat
// pile of an unproduced good cannot hijack the pick and shadow a producible one
// (TASK-128 AC-2). ok is false when no entry is robbable.
func richestRobbable(entries []traderepo.MarketEntry, kind domain.EntityKind, prodRef map[domain.GoodsTypeID]int64) (traderepo.MarketEntry, int64, bool) {
	var (
		best      traderepo.MarketEntry
		bestBasis int64
		found     bool
	)
	for _, e := range entries {
		basis, ok := hackBasis(e, kind, prodRef)
		if !ok {
			continue
		}
		if !found || e.Stock > best.Stock ||
			(e.Stock == best.Stock && e.GoodsType < best.GoodsType) {
			best = e
			bestBasis = basis
			found = true
		}
	}
	return best, bestBasis, found
}

// tradeRateShift mirrors StarWind update_user_cash_and_rating (db.sql): the
// trade reputation gained on a deal is cash_sum >> 8 — one point per 256 credits
// of turnover, in either direction (phase 10.3.13).
const tradeRateShift = 8

// awardTradeReputation grows the player's trade_rate by total>>8 inside the
// trade transaction so the accrual is atomic with the deal. A sub-256-credit
// deal yields 0 and is skipped to avoid a no-op UPDATE (parity: the original
// integer shift gives 0 for such deals too).
func awardTradeReputation(ctx context.Context, txRepo Repo, playerID domain.PlayerID, total int64) error {
	delta := int(total >> tradeRateShift)
	if delta <= 0 {
		return nil
	}
	if _, err := txRepo.AddReputation(ctx, playerID, playersrepo.Reputation{Trade: delta}); err != nil {
		return fmt.Errorf("award trade reputation: %w", err)
	}
	return nil
}

// authorizeDocked guards every mutation: the ship must exist, belong to
// the calling player, and be docked at the target station.
func (s *Service) authorizeDocked(ctx context.Context, txRepo Repo, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef) error {
	dock, err := txRepo.LoadShipDock(ctx, shipID)
	if err != nil {
		if errors.Is(err, traderepo.ErrShipNotFound) {
			return ErrShipNotFound
		}
		return fmt.Errorf("load ship dock: %w", err)
	}
	if dock.PlayerID != playerID {
		return ErrForbidden
	}
	if dock.Docked == nil {
		return ErrNotDocked
	}
	if dock.Docked.Kind != station.Kind || dock.Docked.ID != station.ID {
		return ErrWrongStation
	}
	return nil
}

func isStationKind(k domain.EntityKind) bool {
	switch k {
	case domain.EntityKindStation, domain.EntityKindTradeStation, domain.EntityKindPirbase:
		return true
	}
	return false
}
