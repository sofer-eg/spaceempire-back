package quests_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	questsrepo "spaceempire/back/internal/persistence/quests"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

// TestIntegration_Quests_ProgressAndState exercises the repo against real
// Postgres: Ensure idempotency, Get, SetStep, Complete, ListActive, and the
// PlayerState snapshot read (ship docked + cargo units + cash).
func TestIntegration_Quests_ProgressAndState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := questsrepo.New(pool)

	player := seedPlayer(t, pool, "quester")

	// Ensure is idempotent: two calls leave one active row at step 0.
	require.NoError(t, repo.Ensure(ctx, player, "tutorial", nil))
	require.NoError(t, repo.Ensure(ctx, player, "tutorial", nil))
	got, ok, err := repo.Get(ctx, player, "tutorial")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, got.StepIndex)
	assert.Equal(t, domain.QuestActive, got.Status)

	active, err := repo.ListActive(ctx, 100)
	require.NoError(t, err)
	require.Len(t, active, 1)

	// Advance + complete.
	require.NoError(t, repo.SetStep(ctx, player, "tutorial", 1))
	got, _, _ = repo.Get(ctx, player, "tutorial")
	assert.Equal(t, 1, got.StepIndex)

	require.NoError(t, repo.Complete(ctx, player, "tutorial", 2, time.Now().UTC()))
	got, _, _ = repo.Get(ctx, player, "tutorial")
	assert.Equal(t, domain.QuestCompleted, got.Status)
	assert.Equal(t, 2, got.StepIndex)
	assert.False(t, got.CompletedAt.IsZero())

	active, err = repo.ListActive(ctx, 100)
	require.NoError(t, err)
	assert.Empty(t, active, "completed quest no longer active")
}

func TestIntegration_Quests_PlayerStateSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := questsrepo.New(pool)

	player := seedPlayer(t, pool, "snap")

	// Docked ship (docked_kind/docked_id both set per the 0005 CHECK).
	var shipID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO ships (player_id, sector_id, hp, shield, docked_kind, docked_id)
		 VALUES ($1, 1, 100, 100, 2, 1) RETURNING id`, int64(player)).Scan(&shipID))
	// 7 units of some goods in the ship hold (owner_kind 1 = ship).
	var goods int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM goods_types ORDER BY id LIMIT 1`).Scan(&goods))
	_, err := pool.Exec(ctx,
		`INSERT INTO cargo (goods_type_id, quantity, owner_kind, owner_id) VALUES ($1, 7, 1, $2)`, goods, shipID)
	require.NoError(t, err)

	docked, units, cash, sector, dockedKind, dockedID, err := repo.PlayerState(ctx, player)
	require.NoError(t, err)
	assert.True(t, docked, "ship is docked")
	assert.Equal(t, int64(7), units, "cargo units summed")
	assert.Equal(t, int64(10000), cash, "default starting cash")
	assert.Equal(t, int64(1), sector, "ship sector")
	assert.Equal(t, int16(2), dockedKind, "docked at a station (kind 2)")
	assert.Equal(t, int64(1), dockedID, "docked target id")
}
