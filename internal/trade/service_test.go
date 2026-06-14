package trade_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/trade"
)

// priceBalance is the catalog the service uses for dynamic pricing in unit
// tests: good 1 (Batteries) has the real [16, 96] band, so a full shelf sells
// at 16 and an empty one at 96. Goods absent here (e.g. slaves 323) fall back
// to the static column price.
func priceBalance(t *testing.T) *balance.Balance {
	t.Helper()
	b, err := balance.New([]balance.Goods{
		{ID: 1, Name: "Batteries", AvgPrice: 16, MaxPrice: 96, Space: 1},
	}, nil)
	require.NoError(t, err)
	return b
}

// stubRepo is an in-memory trade.Repo implementation that mirrors the
// production semantics closely enough for service-level assertions. It
// also serves as its own TxRunner: Do invokes fn directly — there is no
// real transaction, but the unit tests only need the fn-bound repo to
// have a consistent view, which a single-goroutine stub already gives.
type stubRepo struct {
	mu sync.Mutex

	ships      map[domain.ShipID]traderepo.ShipDock
	cash       map[domain.PlayerID]int64
	reputation map[domain.PlayerID]playersrepo.Reputation
	goodsTypes map[domain.GoodsTypeID]domain.GoodsType
	capacities map[domain.EntityRef]float64
	stacks     map[domain.EntityRef]map[domain.GoodsTypeID]int64
	market     map[domain.EntityRef]map[domain.GoodsTypeID]traderepo.MarketEntry
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		ships:      make(map[domain.ShipID]traderepo.ShipDock),
		cash:       make(map[domain.PlayerID]int64),
		reputation: make(map[domain.PlayerID]playersrepo.Reputation),
		goodsTypes: make(map[domain.GoodsTypeID]domain.GoodsType),
		capacities: make(map[domain.EntityRef]float64),
		stacks:     make(map[domain.EntityRef]map[domain.GoodsTypeID]int64),
		market:     make(map[domain.EntityRef]map[domain.GoodsTypeID]traderepo.MarketEntry),
	}
}

func (s *stubRepo) LoadShipDock(_ context.Context, id domain.ShipID) (traderepo.ShipDock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dock, ok := s.ships[id]
	if !ok {
		return traderepo.ShipDock{}, traderepo.ErrShipNotFound
	}
	return dock, nil
}

func (s *stubRepo) GetMarketEntry(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) (traderepo.MarketEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.market[owner][gtype]
	if !ok {
		return traderepo.MarketEntry{}, traderepo.ErrMarketEntryNotFound
	}
	return e, nil
}

func (s *stubRepo) ListMarket(_ context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch owner.Kind {
	case domain.EntityKindStation, domain.EntityKindTradeStation, domain.EntityKindPirbase:
	default:
		return nil, traderepo.ErrUnsupportedStationKind
	}
	rows := s.market[owner]
	out := make([]traderepo.MarketEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, e)
	}
	return out, nil
}

func (s *stubRepo) AdjustStock(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.market[owner][gtype]
	if !ok {
		return 0, traderepo.ErrMarketEntryNotFound
	}
	next := e.Stock + delta
	if next < 0 {
		return 0, traderepo.ErrInsufficientStock
	}
	if next > e.MaxStock {
		return 0, traderepo.ErrStockOverflow
	}
	e.Stock = next
	s.market[owner][gtype] = e
	return next, nil
}

func (s *stubRepo) GetCash(_ context.Context, playerID domain.PlayerID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.cash[playerID]
	if !ok {
		return 0, playersrepo.ErrPlayerNotFound
	}
	return v, nil
}

func (s *stubRepo) AdjustCash(_ context.Context, playerID domain.PlayerID, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.cash[playerID]
	if !ok {
		return 0, playersrepo.ErrPlayerNotFound
	}
	if v+delta < 0 {
		return 0, playersrepo.ErrInsufficientCash
	}
	v += delta
	s.cash[playerID] = v
	return v, nil
}

func (s *stubRepo) AddReputation(_ context.Context, playerID domain.PlayerID, delta playersrepo.Reputation) (playersrepo.Reputation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.reputation[playerID]
	cur.War += delta.War
	cur.Trade += delta.Trade
	s.reputation[playerID] = cur
	return cur, nil
}

func (s *stubRepo) GoodsType(_ context.Context, id domain.GoodsTypeID) (domain.GoodsType, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gt, ok := s.goodsTypes[id]
	if !ok {
		return domain.GoodsType{}, cargorepo.ErrGoodsTypeNotFound
	}
	return gt, nil
}

func (s *stubRepo) Capacity(_ context.Context, owner domain.EntityRef) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.capacities[owner]
	if !ok {
		return 0, cargorepo.ErrOwnerNotFound
	}
	return c, nil
}

func (s *stubRepo) UsedSpace(_ context.Context, owner domain.EntityRef) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var used float64
	for gid, qty := range s.stacks[owner] {
		used += float64(qty) * s.goodsTypes[gid].Space
	}
	return used, nil
}

func (s *stubRepo) AddCargo(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stacks[owner] == nil {
		s.stacks[owner] = make(map[domain.GoodsTypeID]int64)
	}
	s.stacks[owner][gtype] += qty
	return nil
}

func (s *stubRepo) SubtractCargo(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	have := s.stacks[owner][gtype]
	if have < qty {
		return cargorepo.ErrInsufficientQuantity
	}
	have -= qty
	if have == 0 {
		delete(s.stacks[owner], gtype)
	} else {
		s.stacks[owner][gtype] = have
	}
	return nil
}

// inlineTx invokes fn with the same Repo it was constructed with —
// matches the cargo unit-test idiom and is enough for service tests.
type inlineTx struct{ repo trade.Repo }

func (t inlineTx) Do(ctx context.Context, fn func(context.Context, trade.Repo) error) error {
	return fn(ctx, t.repo)
}

// fixture is a freshly wired stub + service in a known state used by
// most happy-path tests so each test does not have to repeat the setup.
type fixture struct {
	repo    *stubRepo
	svc     *trade.Service
	player  domain.PlayerID
	ship    domain.ShipID
	station domain.EntityRef
	shipRef domain.EntityRef
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := newStubRepo()
	player := domain.PlayerID(42)
	ship := domain.ShipID(7)
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 11}
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship)}

	dockedAt := station
	repo.ships[ship] = traderepo.ShipDock{
		PlayerID: player,
		SectorID: domain.SectorID(1),
		Docked:   &dockedAt,
	}
	repo.cash[player] = 10000
	repo.capacities[shipRef] = 100
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	sell := int64(50)
	buy := int64(30)
	repo.market[station] = map[domain.GoodsTypeID]traderepo.MarketEntry{
		1: {
			Owner:     station,
			GoodsType: 1,
			BuyPrice:  &buy,
			SellPrice: &sell,
			Stock:     100,
			MaxStock:  500,
		},
	}

	svc := trade.New(repo, inlineTx{repo: repo}, priceBalance(t))
	return &fixture{
		repo:    repo,
		svc:     svc,
		player:  player,
		ship:    ship,
		station: station,
		shipRef: shipRef,
	}
}

func TestUnit_TradeService_Buy_HappyPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	res, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 20)
	require.NoError(t, err)

	// Stock 100 of cap 500 → price = 16 + (96-16)*(500-100)/500 = 80.
	assert.EqualValues(t, 10000-80*20, res.NewCash)
	assert.EqualValues(t, 100-20, res.NewStock)
	assert.EqualValues(t, 80, res.UnitPrice)
	assert.EqualValues(t, 1600, res.TotalAmount)

	assert.EqualValues(t, 8400, f.repo.cash[f.player])
	assert.EqualValues(t, 80, f.repo.market[f.station][1].Stock)
	assert.EqualValues(t, 20, f.repo.stacks[f.shipRef][1])
}

func TestUnit_TradeService_TradeStation_FlatColumnPrice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	// A trade station runs a universal flat-price market (phase 10.19
	// follow-up): even though good 1 (Batteries) has a [16,96] balance band a
	// factory would price dynamically (80 at this stock/cap), a trade station
	// charges the flat seeded column price. Re-dock at a trade station.
	ts := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 5}
	dockedAt := ts
	f.repo.ships[f.ship] = traderepo.ShipDock{PlayerID: f.player, SectorID: domain.SectorID(1), Docked: &dockedAt}
	sell := int64(50)
	buy := int64(30)
	f.repo.market[ts] = map[domain.GoodsTypeID]traderepo.MarketEntry{
		1: {Owner: ts, GoodsType: 1, BuyPrice: &buy, SellPrice: &sell, Stock: 100, MaxStock: 500},
	}

	bres, err := f.svc.Buy(context.Background(), f.player, f.ship, ts, 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 50, bres.UnitPrice, "trade station charges the flat column price, not the dynamic 80")

	sres, err := f.svc.Sell(context.Background(), f.player, f.ship, ts, 1, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 30, sres.UnitPrice, "trade station buys at the flat column price, not a dynamic band")
}

func TestUnit_TradeService_DynamicPrice_ScalesWithStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	require.NoError(t, f.repo.AddCargo(ctx, f.shipRef, 1, 10)) // hold to sell from

	setStock := func(s int64) {
		e := f.repo.market[f.station][1]
		e.Stock = s
		f.repo.market[f.station][1] = e
	}

	// Full shelf → avg_price (cheap to buy).
	setStock(500)
	buy, err := f.svc.Buy(ctx, f.player, f.ship, f.station, 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 16, buy.UnitPrice, "full shelf trades at avg_price")

	// 40% full → mid band: 16 + (96-16)*(500-100)/500.
	setStock(100)
	buy, err = f.svc.Buy(ctx, f.player, f.ship, f.station, 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 80, buy.UnitPrice)

	// Empty shelf → max_price: the station pays top price to restock.
	setStock(0)
	sell, err := f.svc.Sell(ctx, f.player, f.ship, f.station, 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 96, sell.UnitPrice, "empty shelf trades at max_price")
}

func TestUnit_TradeService_Buy_NonPositiveQty(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 0)
	require.ErrorIs(t, err, trade.ErrNonPositiveQuantity)
}

func TestUnit_TradeService_Buy_InvalidStationKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	bad := domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 1}
	_, err := f.svc.Buy(context.Background(), f.player, f.ship, bad, 1, 5)
	require.ErrorIs(t, err, trade.ErrInvalidStationKind)
}

func TestUnit_TradeService_Buy_ShipNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Buy(context.Background(), f.player, domain.ShipID(999), f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrShipNotFound)
}

func TestUnit_TradeService_Buy_ForbiddenForOtherPlayer(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Buy(context.Background(), domain.PlayerID(999), f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrForbidden)
}

func TestUnit_TradeService_Buy_NotDocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dock := f.repo.ships[f.ship]
	dock.Docked = nil
	f.repo.ships[f.ship] = dock

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrNotDocked)
}

func TestUnit_TradeService_Buy_WrongStation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := domain.EntityRef{Kind: domain.EntityKindStation, ID: 555}
	dock := f.repo.ships[f.ship]
	dock.Docked = &other
	f.repo.ships[f.ship] = dock

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrWrongStation)
}

func TestUnit_TradeService_Buy_StationDoesNotOfferGood(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 999, 5)
	require.ErrorIs(t, err, trade.ErrMarketEntryNotFound)
}

func TestUnit_TradeService_Buy_StationDoesNotSell(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entry := f.repo.market[f.station][1]
	entry.SellPrice = nil
	f.repo.market[f.station][1] = entry

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrStationDoesNotSell)
}

func TestUnit_TradeService_Buy_InsufficientStock(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entry := f.repo.market[f.station][1]
	entry.Stock = 3
	f.repo.market[f.station][1] = entry

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 10)
	require.ErrorIs(t, err, trade.ErrInsufficientStock)
	// Cash and cargo must not have been touched.
	assert.EqualValues(t, 10000, f.repo.cash[f.player])
	assert.Empty(t, f.repo.stacks[f.shipRef])
}

func TestUnit_TradeService_Buy_InsufficientCash(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.repo.cash[f.player] = 100 // not enough for 5 * 80

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrInsufficientCash)
	// Stock and cargo must not have been touched.
	assert.EqualValues(t, 100, f.repo.market[f.station][1].Stock)
	assert.Empty(t, f.repo.stacks[f.shipRef])
}

func TestUnit_TradeService_Buy_NoCargoSpace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// Set capacity 5 (Batteries.Space=1) so a buy of 6 will not fit.
	f.repo.capacities[f.shipRef] = 5

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 6)
	require.ErrorIs(t, err, trade.ErrNoCargoSpace)
}

func TestUnit_TradeService_Sell_HappyPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// Seed the ship with 25 batteries.
	require.NoError(t, f.repo.AddCargo(context.Background(), f.shipRef, 1, 25))

	res, err := f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 20)
	require.NoError(t, err)

	// Pre-sale stock 100 of cap 500 → price = 80 (same band as the buy side).
	assert.EqualValues(t, 10000+80*20, res.NewCash)
	assert.EqualValues(t, 100+20, res.NewStock)
	assert.EqualValues(t, 80, res.UnitPrice)
	assert.EqualValues(t, 1600, res.TotalAmount)

	assert.EqualValues(t, 11600, f.repo.cash[f.player])
	assert.EqualValues(t, 120, f.repo.market[f.station][1].Stock)
	assert.EqualValues(t, 5, f.repo.stacks[f.shipRef][1])
}

// TestUnit_TradeService_Buy_GrowsTradeReputation proves a buy grows the
// player's trade_rate by total>>8 (phase 10.3.13): total 1600 -> 1600>>8 = 6.
func TestUnit_TradeService_Buy_GrowsTradeReputation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	res, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1600, res.TotalAmount)
	assert.Equal(t, 6, f.repo.reputation[f.player].Trade, "trade_rate grows by total>>8")
}

// TestUnit_TradeService_Sell_GrowsTradeReputation proves a sell grows the
// player's trade_rate by total>>8 too — the same direction as a buy.
func TestUnit_TradeService_Sell_GrowsTradeReputation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	require.NoError(t, f.repo.AddCargo(context.Background(), f.shipRef, 1, 25))

	res, err := f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1600, res.TotalAmount)
	assert.Equal(t, 6, f.repo.reputation[f.player].Trade, "trade_rate grows by total>>8")
}

// TestUnit_TradeService_SmallDeal_NoTradeReputation proves a sub-256-credit deal
// (total 80 -> 80>>8 = 0) leaves trade_rate untouched — parity with the original
// integer shift, and no no-op UPDATE.
func TestUnit_TradeService_SmallDeal_NoTradeReputation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	res, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 80, res.TotalAmount)
	assert.Zero(t, f.repo.reputation[f.player].Trade, "a deal below 256 credits yields no trade_rate")
}

func TestUnit_TradeService_Sell_StationDoesNotBuy(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entry := f.repo.market[f.station][1]
	entry.BuyPrice = nil
	f.repo.market[f.station][1] = entry

	require.NoError(t, f.repo.AddCargo(context.Background(), f.shipRef, 1, 10))

	_, err := f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrStationDoesNotBuy)
}

func TestUnit_TradeService_Sell_InsufficientCargo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	require.NoError(t, f.repo.AddCargo(context.Background(), f.shipRef, 1, 2))

	_, err := f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrInsufficientCargo)
	// Cash and stock must not have moved.
	assert.EqualValues(t, 10000, f.repo.cash[f.player])
	assert.EqualValues(t, 100, f.repo.market[f.station][1].Stock)
}

func TestUnit_TradeService_Sell_StockOverflow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entry := f.repo.market[f.station][1]
	entry.Stock = 498
	entry.MaxStock = 500
	f.repo.market[f.station][1] = entry
	require.NoError(t, f.repo.AddCargo(context.Background(), f.shipRef, 1, 10))

	_, err := f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrStockOverflow)
}

// TestUnit_TradeService_Sell_SlavesAtPirbase proves the phase 5.6 slaves sale
// reuses the trade machinery: a buy-only Slaves (323) market entry on a
// pirbase lets a docked player sell, crediting cash and emptying the hold —
// no bespoke endpoint.
func TestUnit_TradeService_Sell_SlavesAtPirbase(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	player := domain.PlayerID(42)
	ship := domain.ShipID(7)
	pirbase := domain.EntityRef{Kind: domain.EntityKindPirbase, ID: 1}
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship)}

	dockedAt := pirbase
	repo.ships[ship] = traderepo.ShipDock{PlayerID: player, SectorID: domain.SectorID(1), Docked: &dockedAt}
	repo.cash[player] = 0
	repo.capacities[shipRef] = 100
	repo.goodsTypes[323] = domain.GoodsType{ID: 323, Name: "Slaves", Space: 1}
	buy := int64(800)
	repo.market[pirbase] = map[domain.GoodsTypeID]traderepo.MarketEntry{
		323: {Owner: pirbase, GoodsType: 323, BuyPrice: &buy, SellPrice: nil, Stock: 0, MaxStock: 1_000_000_000},
	}
	require.NoError(t, repo.AddCargo(context.Background(), shipRef, 323, 5))

	// 323 (slaves) is absent from the price catalog → the fixed column price
	// (800) is used, not a dynamic band.
	svc := trade.New(repo, inlineTx{repo: repo}, priceBalance(t))
	res, err := svc.Sell(context.Background(), player, ship, pirbase, 323, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 5*800, res.NewCash)
	assert.EqualValues(t, 800, res.UnitPrice)
	assert.EqualValues(t, 0, repo.stacks[shipRef][323], "slaves removed from the hold")
}

func TestUnit_TradeService_Sell_NotDocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dock := f.repo.ships[f.ship]
	dock.Docked = nil
	f.repo.ships[f.ship] = dock

	_, err := f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 5)
	require.ErrorIs(t, err, trade.ErrNotDocked)
}

func TestUnit_TradeService_Market_HappyPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	entries, err := f.svc.Market(context.Background(), f.station)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, domain.GoodsTypeID(1), entries[0].GoodsType)
}

func TestUnit_TradeService_Market_InvalidKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.Market(context.Background(), domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 1})
	require.ErrorIs(t, err, trade.ErrInvalidStationKind)
}

func TestUnit_TradeService_BuyThenSell_RoundTrip(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	_, err := f.svc.Buy(context.Background(), f.player, f.ship, f.station, 1, 10)
	require.NoError(t, err)
	_, err = f.svc.Sell(context.Background(), f.player, f.ship, f.station, 1, 10)
	require.NoError(t, err)

	// Buy 10 at stock 100 (price 80) drops the shelf to 90; selling 10 back at
	// the lower stock pays the higher dynamic price (81), so the round trip
	// nets +10 — scarcity moved the price.
	assert.EqualValues(t, 10000-80*10+81*10, f.repo.cash[f.player])
	// Stock returned to the seed level.
	assert.EqualValues(t, 100, f.repo.market[f.station][1].Stock)
	// Cargo back to empty.
	assert.Empty(t, f.repo.stacks[f.shipRef])
}
