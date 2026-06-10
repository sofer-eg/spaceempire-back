package bounties_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	bountyrepo "spaceempire/back/internal/persistence/bounties"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

func seedClan(t *testing.T, pool *pgxpool.Pool, name, tag string, leader domain.PlayerID) domain.ClanID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO clans (name, tag, leader_id) VALUES ($1, $2, $3) RETURNING id`,
		name, tag, int64(leader)).Scan(&id))
	return domain.ClanID(id)
}

// withTx runs fn with a repo bound to a fresh transaction and commits — the
// production path for the FOR UPDATE reads (ActiveForTargets/DueExpired).
func withTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *bountyrepo.Repository, fn func(*bountyrepo.Repository)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	fn(repo.WithExecutor(tx))
	require.NoError(t, tx.Commit(ctx))
}

// TestIntegration_Bounties_LifecycleAndMatching exercises the repo SQL against
// real Postgres: the unnest-paired ActiveForTargets matching, the name-
// resolving ListActive JOINs, MarkPaid/MarkExpired status guards, DueExpired,
// and HistoryForTarget.
func TestIntegration_Bounties_LifecycleAndMatching(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := bountyrepo.New(pool)
	now := time.Now().UTC()

	hunter := seedPlayer(t, pool, "hunter")
	target := seedPlayer(t, pool, "target")
	sponsor := seedPlayer(t, pool, "sponsor")
	leader := seedPlayer(t, pool, "cleader")
	clan := seedClan(t, pool, "Pirates", "PIR", leader)

	id1, err := repo.Create(ctx, domain.Bounty{
		Target: domain.PlayerRef(target), Sponsor: domain.PlayerRef(sponsor),
		Amount: 1000, ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)
	id2, err := repo.Create(ctx, domain.Bounty{
		Target: domain.ClanRef(clan), Sponsor: domain.PlayerRef(sponsor),
		Amount: 2000, ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)

	// ActiveForTargets / DueExpired take FOR UPDATE, so — like production —
	// they run inside a transaction (the payout/expiry tx). withTx binds the
	// repo to a pgx.Tx and commits at the end.
	withTx(t, ctx, pool, repo, func(tr *bountyrepo.Repository) {
		// Paired matching: both targets → both bounties.
		both, err := tr.ActiveForTargets(ctx, now, []domain.EntityRef{domain.PlayerRef(target), domain.ClanRef(clan)})
		require.NoError(t, err)
		assert.Len(t, both, 2)

		// Only the player target → exactly the player bounty (no cross of
		// player-id with clan-kind).
		onlyPlayer, err := tr.ActiveForTargets(ctx, now, []domain.EntityRef{domain.PlayerRef(target)})
		require.NoError(t, err)
		require.Len(t, onlyPlayer, 1)
		assert.Equal(t, id1, onlyPlayer[0].ID)
	})

	// ListActive: highest amount first, names resolved via the LEFT JOINs.
	views, err := repo.ListActive(ctx, now, 50)
	require.NoError(t, err)
	require.Len(t, views, 2)
	assert.Equal(t, id2, views[0].ID)
	assert.Equal(t, "Pirates", views[0].TargetName)
	assert.Equal(t, "sponsor", views[0].SponsorName)
	assert.Equal(t, "target", views[1].TargetName)

	// MarkPaid removes the bounty from the active set.
	require.NoError(t, repo.MarkPaid(ctx, id1, hunter, now))

	withTx(t, ctx, pool, repo, func(tr *bountyrepo.Repository) {
		stillActive, err := tr.ActiveForTargets(ctx, now, []domain.EntityRef{domain.PlayerRef(target)})
		require.NoError(t, err)
		assert.Empty(t, stillActive)

		// DueExpired sees the remaining active clan bounty once we pass its
		// deadline; MarkExpired then closes it.
		due, err := tr.DueExpired(ctx, now.Add(2*time.Hour), 100)
		require.NoError(t, err)
		require.Len(t, due, 1)
		assert.Equal(t, id2, due[0].ID)
		require.NoError(t, tr.MarkExpired(ctx, id2))
	})

	// History for the player target includes the now-paid bounty. (The View
	// carries status for display but not paid_to — payout attribution lives
	// on the bounty row, not the read model.)
	hist, err := repo.HistoryForTarget(ctx, domain.PlayerRef(target), 50)
	require.NoError(t, err)
	require.Len(t, hist, 1)
	assert.Equal(t, domain.BountyPaid, hist[0].Status)
}
