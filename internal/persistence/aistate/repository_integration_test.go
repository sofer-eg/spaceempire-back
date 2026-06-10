package aistate_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/aistate"
	"spaceempire/back/internal/pkg/database/testdb"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool) domain.PlayerID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ('p', 'h') RETURNING id`).Scan(&id)
	require.NoError(t, err)
	return domain.PlayerID(id)
}

func seedShip(t *testing.T, pool *pgxpool.Pool, pid domain.PlayerID, sector domain.SectorID) domain.ShipID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO ships (player_id, sector_id, hp, shield) VALUES ($1, $2, 100, 100) RETURNING id`,
		int64(pid), int64(sector)).Scan(&id)
	require.NoError(t, err)
	return domain.ShipID(id)
}

// TestIntegration_AIState_UpsertLoadAll round-trips AI state: insert via
// BatchUpsert, then re-upsert the same ship to confirm ON CONFLICT updates
// the kind and json in place rather than duplicating the row.
func TestIntegration_AIState_UpsertLoadAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := aistate.New(pool)

	const sector = domain.SectorID(1)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, sector)

	require.NoError(t, repo.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         ship,
		SectorID:       sector,
		ControllerKind: "circle",
		StateJSON:      []byte(`{"phase":1.5}`),
	}}))

	got, err := repo.LoadAll(ctx, sector)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, ship, got[0].ShipID)
	assert.Equal(t, "circle", got[0].ControllerKind)
	assert.JSONEq(t, `{"phase":1.5}`, string(got[0].StateJSON))

	// Upsert again with advanced state — must update in place.
	require.NoError(t, repo.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         ship,
		SectorID:       sector,
		ControllerKind: "circle",
		StateJSON:      []byte(`{"phase":3.0}`),
	}}))

	got, err = repo.LoadAll(ctx, sector)
	require.NoError(t, err)
	require.Len(t, got, 1, "upsert must not duplicate the row")
	assert.JSONEq(t, `{"phase":3.0}`, string(got[0].StateJSON))
}

// TestIntegration_AIState_EmptyJSONDefaults confirms an empty StateJSON is
// stored as the JSON object "{}" (the jsonb cast would reject an empty
// string).
func TestIntegration_AIState_EmptyJSONDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := aistate.New(pool)

	const sector = domain.SectorID(1)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, sector)

	require.NoError(t, repo.BatchUpsert(ctx, []domain.AIState{{
		ShipID:         ship,
		SectorID:       sector,
		ControllerKind: "stub",
		StateJSON:      nil,
	}}))

	got, err := repo.LoadAll(ctx, sector)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.JSONEq(t, `{}`, string(got[0].StateJSON))
}

// TestIntegration_AIState_CascadeOnShipDelete verifies the ships ON DELETE
// CASCADE removes the AI state when an NPC ship is deleted (the worker's
// kill sweep relies on this — no explicit ai_state Delete exists).
func TestIntegration_AIState_CascadeOnShipDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := aistate.New(pool)

	const sector = domain.SectorID(1)
	pid := seedPlayer(t, pool)
	ship := seedShip(t, pool, pid, sector)

	require.NoError(t, repo.BatchUpsert(ctx, []domain.AIState{{
		ShipID: ship, SectorID: sector, ControllerKind: "circle", StateJSON: []byte(`{}`),
	}}))

	_, err := pool.Exec(ctx, `DELETE FROM ships WHERE id = $1`, int64(ship))
	require.NoError(t, err)

	got, err := repo.LoadAll(ctx, sector)
	require.NoError(t, err)
	assert.Empty(t, got, "ai_state row should cascade-delete with its ship")
}
