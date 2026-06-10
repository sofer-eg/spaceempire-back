// Package testdb provides Postgres test fixtures backed by testcontainers-go.
// Each call to Setup spins up a fresh container with the project schema
// applied, so tests are fully isolated from one another and from any
// developer-local database.
//
// Container reuse strategy: each Setup call creates a new container. For
// suites that touch the schema heavily this is cheap (~1-2s per container
// with shared image cache) and avoids any test-ordering coupling.
package testdb

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"spaceempire/back/internal/pkg/database"
)

const (
	dbName     = "spaceempire_test"
	dbUser     = "test"
	dbPassword = "test"
	pgImage    = "postgres:16-alpine"
)

// Setup launches a Postgres testcontainer, runs the project's goose migrations
// against it, and returns a connected pgxpool. The container and pool are
// torn down via t.Cleanup, so callers do not need to defer anything.
func Setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase(dbName),
		postgres.WithUsername(dbUser),
		postgres.WithPassword(dbPassword),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	require.NoError(t, err, "start postgres container")

	t.Cleanup(func() {
		// Use a fresh context — the test's may already be canceled.
		_ = container.Terminate(context.Background())
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "container connection string")

	require.NoError(t, database.MigrateUp(ctx, dsn), "apply migrations")

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "pgxpool connect")
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx), "ping pool")

	return pool
}
