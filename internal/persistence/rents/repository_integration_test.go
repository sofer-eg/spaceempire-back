package rents_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	rentsrepo "spaceempire/back/internal/persistence/rents"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

// seedStation inserts a player-owned station in sector 1 (seeded by migration
// 0002) and returns its EntityRef.
func seedStation(t *testing.T, pool *pgxpool.Pool, owner domain.PlayerID) domain.EntityRef {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO stations (owner_id, sector_id, pos_x, pos_y) VALUES ($1, 1, 0, 0) RETURNING id`,
		int64(owner)).Scan(&id))
	return domain.EntityRef{Kind: domain.EntityKindStation, ID: id}
}

// TestIntegration_Rents_BillingAndConfiscation exercises the full rent
// persistence surface against real Postgres: stations.PlayerOwned /
// ClearOwner, rents.Ensure (ON CONFLICT idempotency), Due (FOR UPDATE SKIP
// LOCKED), MarkPaid / MarkUnpaid, and Delete.
func TestIntegration_Rents_BillingAndConfiscation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	rents := rentsrepo.New(pool)
	stations := stationsrepo.New(pool)
	now := time.Now().UTC()

	owner := seedPlayer(t, pool, "stationlord")
	station := seedStation(t, pool, owner)

	// PlayerOwned finds the owned station.
	owned, err := stations.PlayerOwned(ctx)
	require.NoError(t, err)
	require.Len(t, owned, 1)
	assert.Equal(t, station, owned[0].Ref)
	assert.Equal(t, owner, owned[0].Owner)

	// Ensure is idempotent (ON CONFLICT DO NOTHING): the second call with a
	// different amount/due does not overwrite or duplicate.
	require.NoError(t, rents.Ensure(ctx, owner, station, 5000, now.Add(-time.Minute)))
	require.NoError(t, rents.Ensure(ctx, owner, station, 9999, now.Add(time.Hour)))
	list, err := rents.ListByPayer(ctx, owner)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, int64(5000), list[0].AmountPerPeriod)
	id := list[0].ID

	// Due returns the past-due rent.
	due, err := rents.Due(ctx, now, 100)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, id, due[0].ID)

	// A missed payment bumps unpaid_periods and advances the schedule.
	require.NoError(t, rents.MarkUnpaid(ctx, id, 1, now.Add(time.Hour)))
	notDue, err := rents.Due(ctx, now, 100)
	require.NoError(t, err)
	assert.Empty(t, notDue, "rescheduled into the future")
	list, _ = rents.ListByPayer(ctx, owner)
	assert.Equal(t, 1, list[0].UnpaidPeriods)

	// A successful charge resets the counter.
	require.NoError(t, rents.MarkPaid(ctx, id, now, now.Add(2*time.Hour)))
	list, _ = rents.ListByPayer(ctx, owner)
	assert.Equal(t, 0, list[0].UnpaidPeriods)
	assert.False(t, list[0].LastPaidAt.IsZero())

	// Confiscation: clear the owner and delete the rent.
	require.NoError(t, stations.ClearOwner(ctx, station))
	require.NoError(t, rents.Delete(ctx, id))
	owned, err = stations.PlayerOwned(ctx)
	require.NoError(t, err)
	assert.Empty(t, owned, "owner cleared")
	list, _ = rents.ListByPayer(ctx, owner)
	assert.Empty(t, list, "rent deleted")
}
