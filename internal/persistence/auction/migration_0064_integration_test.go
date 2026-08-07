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
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
)

// seedLot writes one auction_lots row directly, bypassing the repo, because the
// fixture needs a status the repo only reaches through a full lot lifecycle.
func seedLot(
	t *testing.T,
	pool *pgxpool.Pool,
	seller, bidder domain.PlayerID,
	goods int,
	price int64,
	status auctionrepo.Status,
) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO auction_lots
		    (seller_id, goods_type_id, quantity, source_owner_kind, source_owner_id,
		     start_price, current_price, current_bidder_id, ends_at, status)
		VALUES ($1, $2, 1, $3, 1, $4, $4, $5, $6, $7)`,
		int64(seller), goods, int16(domain.EntityKindStation),
		price, int64(bidder), time.Now().UTC().Add(time.Hour), int16(status))
	require.NoError(t, err)
}

// TestIntegration_DropEnglishNamedGoods_RefundsEveryEscrowedBid pins the money
// statement of migration 0064. An active lot holds the leading bid as escrow and
// the migration deletes the lot, so it has to hand the cash back first — every
// bid, not one per bidder.
//
// The case that makes this worth a test: UPDATE ... FROM updates each target row
// exactly once, so joining players to auction_lots lot by lot applies an
// arbitrary single lot per bidder and silently drops the rest. Nothing limits a
// player to one active lot, so a bidder leading on a lot of each good loses one
// of the two bids outright — money destroyed by the statement written to save
// it, against docs/specs/auction.md («деньги не теряются»).
func TestIntegration_DropEnglishNamedGoods_RefundsEveryEscrowedBid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	dsn := pool.Config().ConnString()

	// Roll 0064 back: its Down restores the two catalog rows, which the lots
	// below FK-reference.
	require.NoError(t, database.MigrateDown(ctx, dsn), "roll back 0064")
	var restored int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM goods_types WHERE id IN (104, 114)`).Scan(&restored))
	require.Equal(t, 2, restored,
		"0064 has to be the migration that just rolled back; once a newer one exists this test must step down past it")

	seller := seedPlayer(t, pool, "escrow-seller")
	bidder := seedPlayer(t, pool, "escrow-bidder")
	const startCash = int64(1_000)
	_, err := pool.Exec(ctx, `UPDATE players SET cash = $1 WHERE id = $2`, startCash, int64(bidder))
	require.NoError(t, err)

	// One bidder leading on a lot of each doomed good — what the lot-by-lot
	// join halves.
	seedLot(t, pool, seller, bidder, 104, 5_000, auctionrepo.StatusActive)
	seedLot(t, pool, seller, bidder, 114, 3_000, auctionrepo.StatusActive)
	// Same bidder on a lot that already settled: it paid out at close, so
	// refunding it again would mint money.
	seedLot(t, pool, seller, bidder, 114, 700, auctionrepo.StatusClosed)

	require.NoError(t, database.MigrateUp(ctx, dsn), "re-apply 0064")

	var cash int64
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT cash FROM players WHERE id = $1`, int64(bidder)).Scan(&cash))
	assert.Equal(t, startCash+5_000+3_000, cash,
		"both escrowed bids come back and the settled lot's price does not")

	// The lots and the catalog rows are gone, so the refund was the only way
	// that cash could survive.
	var lots, goods int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM auction_lots WHERE goods_type_id IN (104, 114)`).Scan(&lots))
	assert.Zero(t, lots, "every lot of the dropped goods is deleted")
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM goods_types WHERE id IN (104, 114)`).Scan(&goods))
	assert.Zero(t, goods, "the catalog rows are gone")
}
