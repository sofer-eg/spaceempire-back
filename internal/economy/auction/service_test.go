package auction_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/auction"
	auctionrepo "spaceempire/back/internal/persistence/auction"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
)

// stubRepo is an in-memory auction.Repo: lots are mutated through the
// service the way the real repository would let them be, with the same
// error shapes so the service code path is exercised exactly. It doubles
// as its own TxRunner (single-goroutine consistency is enough for these
// tests).
type stubRepo struct {
	mu sync.Mutex

	lots       map[int64]auctionrepo.Lot
	bids       []bidEntry
	nextLotID  int64
	cash       map[domain.PlayerID]int64
	stacks     map[domain.EntityRef]map[domain.GoodsTypeID]int64
	goodsTypes map[domain.GoodsTypeID]domain.GoodsType
	ships      []deliveryShip
	shipDocks  map[domain.ShipID]auctionrepo.ShipDock
}

type bidEntry struct {
	lotID  int64
	bidder domain.PlayerID
	amount int64
}

type deliveryShip struct {
	id     domain.ShipID
	player domain.PlayerID
	free   float64
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		lots:       make(map[int64]auctionrepo.Lot),
		cash:       make(map[domain.PlayerID]int64),
		stacks:     make(map[domain.EntityRef]map[domain.GoodsTypeID]int64),
		goodsTypes: make(map[domain.GoodsTypeID]domain.GoodsType),
		shipDocks:  make(map[domain.ShipID]auctionrepo.ShipDock),
	}
}

// dockShip registers a ship as docked at the given station, owned by player.
func (s *stubRepo) dockShip(id domain.ShipID, player domain.PlayerID) {
	s.shipDocks[id] = auctionrepo.ShipDock{
		PlayerID: player,
		Docked:   &domain.EntityRef{Kind: domain.EntityKindStation, ID: 1},
	}
}

func (s *stubRepo) LoadShipDock(_ context.Context, shipID domain.ShipID) (auctionrepo.ShipDock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.shipDocks[shipID]
	if !ok {
		return auctionrepo.ShipDock{}, auctionrepo.ErrShipNotFound
	}
	return d, nil
}

func (s *stubRepo) Do(ctx context.Context, fn func(context.Context, auction.Repo) error) error {
	return fn(ctx, s)
}

func (s *stubRepo) CreateLot(_ context.Context, p auctionrepo.CreateLotParams) (auctionrepo.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextLotID++
	lot := auctionrepo.Lot{
		ID:           s.nextLotID,
		SellerID:     p.SellerID,
		GoodsType:    p.GoodsType,
		Quantity:     p.Quantity,
		Source:       p.Source,
		StartPrice:   p.StartPrice,
		CurrentPrice: p.StartPrice,
		EndsAt:       p.EndsAt,
		Status:       auctionrepo.StatusActive,
		CreatedAt:    time.Now(),
	}
	s.lots[lot.ID] = lot
	return lot, nil
}

func (s *stubRepo) GetLot(_ context.Context, id int64) (auctionrepo.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lot, ok := s.lots[id]
	if !ok {
		return auctionrepo.Lot{}, auctionrepo.ErrLotNotFound
	}
	return lot, nil
}

func (s *stubRepo) LockLot(_ context.Context, id int64) (auctionrepo.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lot, ok := s.lots[id]
	if !ok {
		return auctionrepo.Lot{}, auctionrepo.ErrLotNotFound
	}
	if lot.Status != auctionrepo.StatusActive {
		return lot, auctionrepo.ErrLotNotActive
	}
	return lot, nil
}

func (s *stubRepo) ListActive(_ context.Context) ([]auctionrepo.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []auctionrepo.Lot
	for _, l := range s.lots {
		if l.Status == auctionrepo.StatusActive {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *stubRepo) ListDue(_ context.Context, now time.Time, limit int) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	for _, l := range s.lots {
		if l.Status == auctionrepo.StatusActive && !l.EndsAt.After(now) {
			out = append(out, l.ID)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *stubRepo) UpdateBid(_ context.Context, lotID int64, newPrice int64, bidder domain.PlayerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lot, ok := s.lots[lotID]
	if !ok {
		return auctionrepo.ErrLotNotFound
	}
	lot.CurrentPrice = newPrice
	b := bidder
	lot.CurrentBidderID = &b
	s.lots[lotID] = lot
	return nil
}

func (s *stubRepo) InsertBid(_ context.Context, lotID int64, bidder domain.PlayerID, amount int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bids = append(s.bids, bidEntry{lotID: lotID, bidder: bidder, amount: amount})
	return nil
}

func (s *stubRepo) SetStatus(_ context.Context, lotID int64, status auctionrepo.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lot, ok := s.lots[lotID]
	if !ok {
		return auctionrepo.ErrLotNotFound
	}
	lot.Status = status
	s.lots[lotID] = lot
	return nil
}

func (s *stubRepo) FindDeliveryShip(_ context.Context, buyer domain.PlayerID, requiredSpace float64) (domain.ShipID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.ships {
		if sh.player == buyer && sh.free >= requiredSpace {
			return sh.id, true, nil
		}
	}
	return 0, false, nil
}

func (s *stubRepo) ListByParticipant(_ context.Context, player domain.PlayerID, _ int) ([]auctionrepo.Lot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []auctionrepo.Lot
	for _, l := range s.lots {
		if l.SellerID == player || (l.CurrentBidderID != nil && *l.CurrentBidderID == player) {
			out = append(out, l)
		}
	}
	return out, nil
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

func (s *stubRepo) GoodsType(_ context.Context, id domain.GoodsTypeID) (domain.GoodsType, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gt, ok := s.goodsTypes[id]
	if !ok {
		return domain.GoodsType{}, cargorepo.ErrGoodsTypeNotFound
	}
	return gt, nil
}

// fixture wires a fresh stub + service at a known time, with one seller,
// two bidders, and 100 units of "Iron" in the seller's source ship.
type fixture struct {
	repo    *stubRepo
	svc     *auction.Service
	clk     *clock.FakeClock
	seller  domain.PlayerID
	bidderA domain.PlayerID
	bidderB domain.PlayerID
	source  domain.EntityRef
	shipA   domain.ShipID
	shipB   domain.ShipID
	gtype   domain.GoodsTypeID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := newStubRepo()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.NewFakeClock(now)
	svc := auction.New(repo, clk, nil)

	seller := domain.PlayerID(1)
	bidderA := domain.PlayerID(2)
	bidderB := domain.PlayerID(3)
	source := domain.EntityRef{Kind: domain.EntityKindShip, ID: 100}
	gtype := domain.GoodsTypeID(2)

	repo.cash[seller] = 0
	repo.cash[bidderA] = 5000
	repo.cash[bidderB] = 8000
	repo.goodsTypes[gtype] = domain.GoodsType{ID: gtype, Name: "Iron", Space: 1}
	repo.stacks[source] = map[domain.GoodsTypeID]int64{gtype: 100}

	// All participants start docked: the seller's source ship (source.ID),
	// and each bidder's ship. Commerce requires a dock (X-Tension model).
	shipA := domain.ShipID(200)
	shipB := domain.ShipID(300)
	repo.dockShip(domain.ShipID(source.ID), seller)
	repo.dockShip(shipA, bidderA)
	repo.dockShip(shipB, bidderB)

	return &fixture{
		repo: repo, svc: svc, clk: clk,
		seller: seller, bidderA: bidderA, bidderB: bidderB,
		source: source, shipA: shipA, shipB: shipB, gtype: gtype,
	}
}

func (f *fixture) defaultCreate(t *testing.T) auctionrepo.Lot {
	t.Helper()
	lot, err := f.svc.Create(context.Background(), auction.CreateParams{
		Seller:     f.seller,
		Source:     f.source,
		GoodsType:  f.gtype,
		Quantity:   10,
		StartPrice: 100,
		Duration:   time.Hour,
	})
	require.NoError(t, err)
	return lot
}

func TestUnit_AuctionService_Create_DebitsCargoAndInsertsLot(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)

	assert.Equal(t, f.seller, lot.SellerID)
	assert.Equal(t, int64(10), lot.Quantity)
	assert.Equal(t, int64(100), lot.StartPrice)
	assert.Equal(t, int64(100), lot.CurrentPrice)
	assert.Equal(t, auctionrepo.StatusActive, lot.Status)
	assert.Equal(t, f.clk.Now().Add(time.Hour), lot.EndsAt)
	// source had 100 units of Iron; 10 went into the lot.
	assert.Equal(t, int64(90), f.repo.stacks[f.source][f.gtype])
}

func TestUnit_AuctionService_Create_RejectsInsufficientCargo(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Create(context.Background(), auction.CreateParams{
		Seller:     f.seller,
		Source:     f.source,
		GoodsType:  f.gtype,
		Quantity:   200,
		StartPrice: 100,
		Duration:   time.Hour,
	})
	require.ErrorIs(t, err, auction.ErrInsufficientCargo)
	// Cargo and lot table both untouched.
	assert.Equal(t, int64(100), f.repo.stacks[f.source][f.gtype])
	assert.Empty(t, f.repo.lots)
}

func TestUnit_AuctionService_Create_RejectsBadInput(t *testing.T) {
	f := newFixture(t)
	cases := map[string]auction.CreateParams{
		"zero qty":         {Seller: f.seller, Source: f.source, GoodsType: f.gtype, Quantity: 0, StartPrice: 100, Duration: time.Hour},
		"zero start":       {Seller: f.seller, Source: f.source, GoodsType: f.gtype, Quantity: 1, StartPrice: 0, Duration: time.Hour},
		"duration too low": {Seller: f.seller, Source: f.source, GoodsType: f.gtype, Quantity: 1, StartPrice: 1, Duration: time.Second},
		"duration too big": {Seller: f.seller, Source: f.source, GoodsType: f.gtype, Quantity: 1, StartPrice: 1, Duration: 30 * 24 * time.Hour},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Create(context.Background(), in)
			require.Error(t, err)
		})
	}
}

func TestUnit_AuctionService_Bid_HappyPath(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)

	res, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 200)
	require.NoError(t, err)
	assert.Equal(t, int64(200), res.NewPrice)
	assert.True(t, res.NewLeader)

	// Bidder cash is escrowed; previous leader was nil so no refund.
	assert.Equal(t, int64(4800), f.repo.cash[f.bidderA])
	lotAfter, _ := f.repo.GetLot(context.Background(), lot.ID)
	require.NotNil(t, lotAfter.CurrentBidderID)
	assert.Equal(t, f.bidderA, *lotAfter.CurrentBidderID)
	assert.Equal(t, int64(200), lotAfter.CurrentPrice)
}

func TestUnit_AuctionService_Bid_RefundsPreviousLeader(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)

	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 200)
	require.NoError(t, err)
	_, err = f.svc.Bid(context.Background(), f.bidderB, f.shipB, lot.ID, 300)
	require.NoError(t, err)

	// bidderA escrow returned in full; bidderB now holds the escrow.
	assert.Equal(t, int64(5000), f.repo.cash[f.bidderA])
	assert.Equal(t, int64(7700), f.repo.cash[f.bidderB])
}

func TestUnit_AuctionService_Bid_RejectsBelowCurrent(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 100)
	require.ErrorIs(t, err, auction.ErrBidTooLow)
}

func TestUnit_AuctionService_Bid_RejectsInsufficientCash(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 50000)
	require.ErrorIs(t, err, auction.ErrInsufficientCash)
}

func TestUnit_AuctionService_Bid_RejectsSeller(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	_, err := f.svc.Bid(context.Background(), f.seller, domain.ShipID(f.source.ID), lot.ID, 200)
	require.ErrorIs(t, err, auction.ErrSellerBid)
}

func TestUnit_AuctionService_Bid_RejectsAfterEndsAt(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	f.clk.Advance(2 * time.Hour)
	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 200)
	require.ErrorIs(t, err, auction.ErrLotNotActive)
}

func TestUnit_AuctionService_Close_NoBidder_RefundsSeller(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	f.clk.Advance(2 * time.Hour)

	require.NoError(t, f.svc.Close(context.Background(), lot.ID))

	after, _ := f.repo.GetLot(context.Background(), lot.ID)
	assert.Equal(t, auctionrepo.StatusCancelled, after.Status)
	// Source got the 10 units back: was 90 after Create, 100 now.
	assert.Equal(t, int64(100), f.repo.stacks[f.source][f.gtype])
	// No cash moved.
	assert.Equal(t, int64(0), f.repo.cash[f.seller])
}

func TestUnit_AuctionService_Close_DeliversToBuyerShip(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	buyerShip := domain.ShipID(500)
	f.repo.ships = append(f.repo.ships, deliveryShip{id: buyerShip, player: f.bidderA, free: 1000})

	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 200)
	require.NoError(t, err)
	f.clk.Advance(2 * time.Hour)
	require.NoError(t, f.svc.Close(context.Background(), lot.ID))

	after, _ := f.repo.GetLot(context.Background(), lot.ID)
	assert.Equal(t, auctionrepo.StatusClosed, after.Status)
	// Seller paid; bidderA still down by the escrow.
	assert.Equal(t, int64(200), f.repo.cash[f.seller])
	assert.Equal(t, int64(4800), f.repo.cash[f.bidderA])
	// Cargo landed in the buyer's ship.
	assert.Equal(t, int64(10), f.repo.stacks[domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(buyerShip)}][f.gtype])
}

func TestUnit_AuctionService_Close_NoShip_RefundsBuyerReturnsGoods(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t) // 10 units leave the source (100 → 90)

	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 200)
	require.NoError(t, err)
	f.clk.Advance(2 * time.Hour)
	// bidderA has no delivery ship with room → no-sale.
	require.NoError(t, f.svc.Close(context.Background(), lot.ID))

	after, _ := f.repo.GetLot(context.Background(), lot.ID)
	assert.Equal(t, auctionrepo.StatusClosed, after.Status)
	// Seller NOT paid; buyer's escrow refunded; goods returned to the source.
	assert.Equal(t, int64(0), f.repo.cash[f.seller], "seller not paid on a failed delivery")
	assert.Equal(t, int64(5000), f.repo.cash[f.bidderA], "buyer's escrow refunded")
	assert.Equal(t, int64(100), f.repo.stacks[f.source][f.gtype], "goods returned to seller source")
}

func TestUnit_AuctionService_Close_Idempotent(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	f.clk.Advance(2 * time.Hour)

	require.NoError(t, f.svc.Close(context.Background(), lot.ID))
	// Second call must be a no-op (the closer can fire repeatedly).
	require.NoError(t, f.svc.Close(context.Background(), lot.ID))

	after, _ := f.repo.GetLot(context.Background(), lot.ID)
	assert.Equal(t, auctionrepo.StatusCancelled, after.Status)
}

func TestUnit_AuctionService_Close_UnknownLot(t *testing.T) {
	f := newFixture(t)
	err := f.svc.Close(context.Background(), 999)
	require.True(t, errors.Is(err, auction.ErrLotNotFound))
}

// TestUnit_AuctionService_Bid_RaceSingleWinner is the acceptance race
// scenario from the task: 10 bidders fire amount=200 at the same lot
// concurrently. Exactly one must succeed; the rest must see ErrBidTooLow.
// The stubRepo's Do is single-fn and the mutex serializes mutations the
// way SELECT FOR UPDATE would in real Postgres.
func TestUnit_AuctionService_Bid_RaceSingleWinner(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	const N = 10
	for i := 0; i < N; i++ {
		f.repo.cash[domain.PlayerID(100+i)] = 1000
		f.repo.dockShip(domain.ShipID(1000+i), domain.PlayerID(100+i))
	}

	var wg sync.WaitGroup
	results := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.svc.Bid(context.Background(), domain.PlayerID(100+i), domain.ShipID(1000+i), lot.ID, 200)
			results[i] = err
		}(i)
	}
	wg.Wait()

	var winners, tooLow int
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, auction.ErrBidTooLow):
			tooLow++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	assert.Equal(t, 1, winners, "exactly one bid should win the price 200")
	assert.Equal(t, N-1, tooLow)
}

// --- dock authorization (X-Tension: commerce only at a dock) ---

func TestUnit_AuctionService_Create_RejectsUndockedShip(t *testing.T) {
	f := newFixture(t)
	// Same ship, owned by the seller, but floating in space (Docked == nil).
	f.repo.shipDocks[domain.ShipID(f.source.ID)] = auctionrepo.ShipDock{PlayerID: f.seller}

	_, err := f.svc.Create(context.Background(), auction.CreateParams{
		Seller:     f.seller,
		Source:     f.source,
		GoodsType:  f.gtype,
		Quantity:   10,
		StartPrice: 100,
		Duration:   time.Hour,
	})
	require.ErrorIs(t, err, auction.ErrNotDocked)
	// Cargo and lot table both untouched — the guard runs before the debit.
	assert.Equal(t, int64(100), f.repo.stacks[f.source][f.gtype])
	assert.Empty(t, f.repo.lots)
}

func TestUnit_AuctionService_Bid_RejectsUndockedShip(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	// bidderA's ship leaves the dock.
	f.repo.shipDocks[f.shipA] = auctionrepo.ShipDock{PlayerID: f.bidderA}

	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipA, lot.ID, 200)
	require.ErrorIs(t, err, auction.ErrNotDocked)
	// No escrow taken.
	assert.Equal(t, int64(5000), f.repo.cash[f.bidderA])
}

func TestUnit_AuctionService_Bid_RejectsForeignShip(t *testing.T) {
	f := newFixture(t)
	lot := f.defaultCreate(t)
	// bidderA tries to bid using bidderB's (docked) ship.
	_, err := f.svc.Bid(context.Background(), f.bidderA, f.shipB, lot.ID, 200)
	require.ErrorIs(t, err, auction.ErrForbidden)
	assert.Equal(t, int64(5000), f.repo.cash[f.bidderA])
}
