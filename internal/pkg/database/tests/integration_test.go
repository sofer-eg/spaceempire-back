package database_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/pkg/database/testdb"
)

// TestIntegration_Database_BootstrapInsertSelectShip verifies that the
// Pool + goose-migrations + testdb stack stands up correctly and that the
// schema laid down by 0001_initial_schema.sql is wired the way callers will
// rely on (FK from ships to players, sensible defaults).
func TestIntegration_Database_BootstrapInsertSelectShip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; testcontainers required")
	}

	ctx := context.Background()
	pool := testdb.Setup(t)

	var playerID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO players (login, password_hash)
		VALUES ($1, $2)
		RETURNING id
	`, "alice", "hash").Scan(&playerID)
	require.NoError(t, err)
	require.NotZero(t, playerID)

	var shipID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO ships (player_id, sector_id, pos_x, pos_y)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, playerID, int64(1), 12.5, -7.0).Scan(&shipID)
	require.NoError(t, err)
	require.NotZero(t, shipID)

	var (
		gotPlayerID int64
		gotSectorID int64
		gotPosX     float64
		gotPosY     float64
		gotHP       int
		gotShield   int
	)
	err = pool.QueryRow(ctx, `
		SELECT player_id, sector_id, pos_x, pos_y, hp, shield
		FROM ships WHERE id = $1
	`, shipID).Scan(&gotPlayerID, &gotSectorID, &gotPosX, &gotPosY, &gotHP, &gotShield)
	require.NoError(t, err)
	require.Equal(t, playerID, gotPlayerID)
	require.Equal(t, int64(1), gotSectorID)
	require.InDelta(t, 12.5, gotPosX, 1e-9)
	require.InDelta(t, -7.0, gotPosY, 1e-9)
	require.Equal(t, 100, gotHP)
	require.Equal(t, 100, gotShield)
}
