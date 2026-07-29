package auction_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/auction"
	auctionrepo "spaceempire/back/internal/persistence/auction"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
)

// gatedTxRunner wraps the production RepoTxRunner and hands fn a Repo whose
// lot read is observable. It is the only seam this test inserts: the
// transaction, the SQL and the isolation level all stay production wiring.
type gatedTxRunner struct {
	inner  auction.TxRunner
	onRead func()
}

func (g gatedTxRunner) Do(ctx context.Context, fn func(context.Context, auction.Repo) error) error {
	return g.inner.Do(ctx, func(ctx context.Context, txRepo auction.Repo) error {
		return fn(ctx, lotReadGate{Repo: txRepo, onRead: g.onRead})
	})
}

// lotReadGate reports the moment Bid has read the lot from inside its own
// transaction. Both reads are hooked on purpose: the row lock lives in
// LockLot, and the regression this test exists to catch is precisely someone
// swapping it for the unlocked GetLot — the barrier has to keep firing in
// that case, so the test fails on the invariant instead of hanging on a
// signal that never comes.
type lotReadGate struct {
	auction.Repo
	onRead func()
}

func (g lotReadGate) LockLot(ctx context.Context, id int64) (auctionrepo.Lot, error) {
	lot, err := g.Repo.LockLot(ctx, id)
	g.onRead()
	return lot, err
}

func (g lotReadGate) GetLot(ctx context.Context, id int64) (auctionrepo.Lot, error) {
	lot, err := g.Repo.GetLot(ctx, id)
	g.onRead()
	return lot, err
}

// secondReadWindow is how long the first bidder keeps its transaction open
// after reading the lot, waiting for the second bidder to read it too. Under
// the real SELECT ... FOR UPDATE that read cannot happen — B is parked on the
// row lock until A commits — so the window always elapses in full; that is
// the cost of proving a negative. Drop the lock and B reads within a
// millisecond, closing the window at once.
const secondReadWindow = time.Second

// TestIntegration_AuctionService_Bid_ConcurrentBidsSingleWinner pins the "one
// winner per price" invariant on real Postgres, which is the only layer that
// can carry it: the unit race test's stub serializes whole transactions for
// free, so it stays green even without the row lock.
//
// The interleaving is fixed rather than lucky. A enters Bid, reads the lot
// inside its transaction and stops there, holding the lock; only then does B
// enter Bid. B therefore either blocks on the row lock until A commits, or
// arrives after that commit — either way it re-reads current_price = 200 and
// must lose with ErrBidTooLow.
//
// Mutation check (the reason this test exists): replacing txRepo.LockLot with
// txRepo.GetLot in Service.Bid makes B's read pass unblocked on the stale
// price 100, and this test fails on errB == nil — 200 credits of A's escrow
// stranded, since Close only ever refunds current_bidder_id.
func TestIntegration_AuctionService_Bid_ConcurrentBidsSingleWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)

	now := time.Now().UTC()
	seller := seedITPlayer(t, pool, "seller", 0)
	bidderA := seedITPlayer(t, pool, "bidder_a", 1000)
	bidderB := seedITPlayer(t, pool, "bidder_b", 1000)
	shipA := seedITDockedShip(t, pool, bidderA)
	shipB := seedITDockedShip(t, pool, bidderB)

	lots := auctionrepo.New(pool)
	lot, err := lots.CreateLot(ctx, auctionrepo.CreateLotParams{
		SellerID:   seller,
		GoodsType:  anyITGoodsType(t, pool),
		Quantity:   1,
		Source:     domain.EntityRef{Kind: domain.EntityKindStation, ID: 1},
		StartPrice: 100,
		EndsAt:     now.Add(time.Hour),
	})
	require.NoError(t, err)

	runner := auction.NewRepoTxRunner(
		database.NewTxManager(pool), lots, playersrepo.New(pool), cargorepo.New(pool))
	clk := clock.NewFakeClock(now)

	aRead := make(chan struct{})
	bRead := make(chan struct{})
	// Each signal fires at most once. Bid reads the lot exactly once today, but
	// the day it grows a retry (a 40001 serialization_failure retry is the
	// obvious candidate) the second close would panic the package binary.
	var onceA, onceB sync.Once
	svcA := auction.New(gatedTxRunner{inner: runner, onRead: func() {
		onceA.Do(func() { close(aRead) })
		select {
		case <-bRead:
		case <-time.After(secondReadWindow):
		}
	}}, clk, nil)
	svcB := auction.New(gatedTxRunner{inner: runner, onRead: func() {
		onceB.Do(func() { close(bRead) })
	}}, clk, nil)

	var (
		wg         sync.WaitGroup
		errA, errB error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = svcA.Bid(ctx, bidderA, shipA, lot.ID, 200)
	}()
	go func() {
		defer wg.Done()
		// Enter only once A holds the lot inside its transaction — but never
		// wait forever. If A fails before reading the lot (a tightened
		// requireDocked, a ships-schema migration that invalidates the seed),
		// aRead is never closed; an unbounded receive would then hang until
		// the package-wide test timeout, whose panic kills the whole binary
		// and reddens the auction unit tests along with it, burying the real
		// cause. Failing here names it instead.
		select {
		case <-aRead:
		case <-time.After(secondReadWindow):
			t.Errorf("the first bidder never read the lot within %s — the barrier could not arm",
				secondReadWindow)
			return
		}
		_, errB = svcB.Bid(ctx, bidderB, shipB, lot.ID, 200)
	}()
	wg.Wait()

	require.NoError(t, errA, "the bidder that read the lot first must win")
	require.ErrorIs(t, errB, auction.ErrBidTooLow,
		"the second bid must see current_price=200 once the lock is released and lose")

	after, err := lots.GetLot(ctx, lot.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 200, after.CurrentPrice)
	require.NotNil(t, after.CurrentBidderID)
	assert.Equal(t, bidderA, *after.CurrentBidderID)

	// The escrow is what is actually at stake: a second winner is debited 200
	// that no one ever returns, because Close refunds only current_bidder_id.
	assert.EqualValues(t, 800, itPlayerCash(t, pool, bidderA), "winner's escrow debited once")
	assert.EqualValues(t, 1000, itPlayerCash(t, pool, bidderB), "loser's cash must not move")

	var bids int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM auction_bids WHERE lot_id = $1`, lot.ID).Scan(&bids))
	assert.Equal(t, 1, bids, "only the winning bid is recorded")
}

// seedITPlayer inserts a player with an explicit balance.
func seedITPlayer(t *testing.T, pool *pgxpool.Pool, login string, cash int64) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash, cash) VALUES ($1, 'h', $2) RETURNING id`,
		login, cash).Scan(&id))
	return domain.PlayerID(id)
}

// seedITDockedShip inserts a ship of the player docked at station 1, which is
// what Bid's requireDocked authorization needs.
func seedITDockedShip(t *testing.T, pool *pgxpool.Pool, owner domain.PlayerID) domain.ShipID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, docked_kind, docked_id) VALUES ($1, 1, $2, 1) RETURNING id`,
		int64(owner), int16(domain.EntityKindStation)).Scan(&id))
	return domain.ShipID(id)
}

// anyITGoodsType returns a goods_types id seeded by the migrations (auction_lots
// FK-references it).
func anyITGoodsType(t *testing.T, pool *pgxpool.Pool) domain.GoodsTypeID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id FROM goods_types ORDER BY id LIMIT 1`).Scan(&id))
	return domain.GoodsTypeID(id)
}

func itPlayerCash(t *testing.T, pool *pgxpool.Pool, player domain.PlayerID) int64 {
	t.Helper()
	var cash int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT cash FROM players WHERE id = $1`, int64(player)).Scan(&cash))
	return cash
}
