package auction_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	auctionrepo "spaceempire/back/internal/persistence/auction"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

// anyGoodsType returns a goods_types id seeded by the migrations (the lots
// table FK-references it).
func anyGoodsType(t *testing.T, pool *pgxpool.Pool) domain.GoodsTypeID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT id FROM goods_types ORDER BY id LIMIT 1`).Scan(&id))
	return domain.GoodsTypeID(id)
}

// TestIntegration_Auction_LotLifecycle exercises the repo SQL on real Postgres:
// CreateLot, LockLot (FOR UPDATE), UpdateBid, GetLot, ListDue and SetStatus.
func TestIntegration_Auction_LotLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := auctionrepo.New(pool)
	now := time.Now().UTC()

	seller := seedPlayer(t, pool, "seller")
	bidder := seedPlayer(t, pool, "bidder")
	goods := anyGoodsType(t, pool)

	lot, err := repo.CreateLot(ctx, auctionrepo.CreateLotParams{
		SellerID:   seller,
		GoodsType:  goods,
		Quantity:   5,
		Source:     domain.EntityRef{Kind: domain.EntityKindStation, ID: 1},
		StartPrice: 100,
		EndsAt:     now.Add(time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, auctionrepo.StatusActive, lot.Status)
	assert.Equal(t, int64(100), lot.CurrentPrice)
	require.Nil(t, lot.CurrentBidderID)

	// LockLot returns the active row (FOR UPDATE); a bid updates price+bidder.
	locked, err := repo.LockLot(ctx, lot.ID)
	require.NoError(t, err)
	assert.Equal(t, lot.ID, locked.ID)
	require.NoError(t, repo.UpdateBid(ctx, lot.ID, 150, bidder))

	got, err := repo.GetLot(ctx, lot.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(150), got.CurrentPrice)
	require.NotNil(t, got.CurrentBidderID)
	assert.Equal(t, bidder, *got.CurrentBidderID)

	// ListByParticipant: the lot shows for both the seller and the bidder.
	sellerLots, err := repo.ListByParticipant(ctx, seller, 100)
	require.NoError(t, err)
	require.Len(t, sellerLots, 1)
	assert.Equal(t, lot.ID, sellerLots[0].ID)
	bidderLots, err := repo.ListByParticipant(ctx, bidder, 100)
	require.NoError(t, err)
	require.Len(t, bidderLots, 1)
	assert.Equal(t, lot.ID, bidderLots[0].ID)

	// Not due while ends_at is in the future.
	due, err := repo.ListDue(ctx, now, 100)
	require.NoError(t, err)
	assert.NotContains(t, due, lot.ID)

	// Close it; LockLot then reports it is no longer active.
	require.NoError(t, repo.SetStatus(ctx, lot.ID, auctionrepo.StatusClosed))
	_, err = repo.LockLot(ctx, lot.ID)
	require.ErrorIs(t, err, auctionrepo.ErrLotNotActive)
}

// TestIntegration_Auction_ListDueFiresPastLots: a lot whose timer has elapsed
// is returned by ListDue.
func TestIntegration_Auction_ListDueFiresPastLots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := auctionrepo.New(pool)
	now := time.Now().UTC()

	seller := seedPlayer(t, pool, "seller")
	goods := anyGoodsType(t, pool)

	past, err := repo.CreateLot(ctx, auctionrepo.CreateLotParams{
		SellerID: seller, GoodsType: goods, Quantity: 1,
		Source: domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}, StartPrice: 50,
		EndsAt: now.Add(-time.Minute),
	})
	require.NoError(t, err)

	due, err := repo.ListDue(ctx, now, 100)
	require.NoError(t, err)
	assert.Contains(t, due, past.ID, "an elapsed lot is due")
}
