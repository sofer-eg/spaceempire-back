package insurance_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	insurancerepo "spaceempire/back/internal/persistence/insurance"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

func seedShip(t *testing.T, pool *pgxpool.Pool, owner domain.PlayerID) domain.ShipID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, hp, shield) VALUES ($1, 1, 100, 100) RETURNING id`,
		int64(owner)).Scan(&id))
	return domain.ShipID(id)
}

// TestIntegration_Insurance_PolicyLifecycle exercises the repo SQL against
// real Postgres: ShipOwnership, Create + the active-per-ship partial-unique
// (ErrAlreadyInsured), ActiveForShip expiry filtering, the lazy
// ExpireActiveForShip → re-insure path, Claim, and ListByPlayer.
func TestIntegration_Insurance_PolicyLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := insurancerepo.New(pool)
	now := time.Now().UTC()

	owner := seedPlayer(t, pool, "pilot")
	ship := seedShip(t, pool, owner)

	// ShipOwnership reflects the seeded ship (undocked).
	gotOwner, docked, err := repo.ShipOwnership(ctx, ship)
	require.NoError(t, err)
	assert.Equal(t, owner, gotOwner)
	assert.Nil(t, docked)

	p1, err := repo.Create(ctx, domain.InsurancePolicy{
		ShipID: ship, PlayerID: owner, PremiumPaid: 1000, Coverage: 10000,
		ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)

	// Active + unexpired → found.
	got, ok, err := repo.ActiveForShip(ctx, ship, now)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, p1, got.ID)

	// A second active policy on the same ship hits the partial-unique index.
	_, err = repo.Create(ctx, domain.InsurancePolicy{
		ShipID: ship, PlayerID: owner, PremiumPaid: 500, Coverage: 5000,
		ExpiresAt: now.Add(time.Hour),
	})
	require.ErrorIs(t, err, insurancerepo.ErrAlreadyInsured)

	// Past its deadline, ActiveForShip returns nothing.
	_, ok, err = repo.ActiveForShip(ctx, ship, now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.False(t, ok)

	// Lazy-expire the time-lapsed policy, then a re-insure succeeds.
	require.NoError(t, repo.ExpireActiveForShip(ctx, ship, now.Add(2*time.Hour)))
	p2, err := repo.Create(ctx, domain.InsurancePolicy{
		ShipID: ship, PlayerID: owner, PremiumPaid: 2000, Coverage: 20000,
		ExpiresAt: now.Add(3 * time.Hour),
	})
	require.NoError(t, err)

	// Claim the live policy.
	require.NoError(t, repo.Claim(ctx, p2, now.Add(2*time.Hour)))

	// History: p1 expired, p2 claimed (newest first).
	all, err := repo.ListByPlayer(ctx, owner)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, p2, all[0].ID)
	assert.Equal(t, domain.PolicyClaimed, all[0].Status)
	assert.Equal(t, domain.PolicyExpired, all[1].Status)
}
