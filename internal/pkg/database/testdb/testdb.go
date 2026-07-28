// Package testdb provides Postgres test fixtures backed by testcontainers-go.
//
// Container strategy: one container per test binary (i.e. per package), started
// lazily on the first Setup call. The project's goose migrations run once, into
// a template database; every Setup then clones that template with
// CREATE DATABASE ... TEMPLATE, which Postgres serves as a file copy — tens of
// milliseconds against ~500ms for a migration replay and ~2s for a container
// start. Each test still gets a private database, so full isolation and
// t.Parallel() are preserved.
//
// Packages using Setup must declare
//
//	func TestMain(m *testing.M) { testdb.Main(m) }
//
// so the shared container is terminated after the last test. A run killed by
// `go test -timeout` panics the whole binary and skips every cleanup; the
// containers started here carry the LabelKey/LabelValue label so that `make
// test-clean` can reap those leftovers without touching unrelated containers.
package testdb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
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
	// templateDB holds the migrated schema. Nothing connects to it after the
	// migration: CREATE DATABASE ... TEMPLATE refuses to run while the source
	// database has sessions.
	templateDB = "spaceempire_tmpl"
	adminDB    = "postgres"
	dbUser     = "test"
	dbPassword = "test"
	pgImage    = "postgres:16-alpine"

	// LabelKey/LabelValue mark every container this package starts. `make
	// test-clean` reaps leaked containers strictly by this label, so unrelated
	// local databases are never touched.
	LabelKey   = "spaceempire.test"
	LabelValue = "true"

	// maxConns raises the server-side connection ceiling above the default 100:
	// a package may run up to GOMAXPROCS tests in parallel, each holding its own
	// pgxpool (default max is NumCPU), plus the pools an app-level test opens.
	maxConns = "200"
)

// shared is the per-process container and its admin pool.
type shared struct {
	container *postgres.PostgresContainer
	// templateDSN points at templateDB; per-test DSNs are derived from it.
	templateDSN string
	// admin is connected to the maintenance database, from which CREATE/DROP
	// DATABASE are issued (neither can run while connected to the target).
	admin *pgxpool.Pool
}

var (
	initOnce sync.Once
	instance *shared
	initErr  error
	dbSeq    atomic.Int64
)

// Setup returns a pgxpool connected to a private, fully migrated database.
// The database is cloned from the package-wide template; it and the pool are
// released via t.Cleanup, so callers do not need to defer anything.
func Setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	s, err := start(ctx)
	require.NoError(t, err, "start shared postgres container")

	name := fmt.Sprintf("testdb_%d", dbSeq.Add(1))
	_, err = s.admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateDB))
	require.NoError(t, err, "clone template database")
	t.Cleanup(func() {
		// Best effort: the container goes away at the end of the run anyway.
		// FORCE terminates sessions a test may have left behind.
		_, _ = s.admin.Exec(context.Background(), fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})

	dsn, err := dsnForDB(s.templateDSN, name)
	require.NoError(t, err, "build test dsn")

	// The DSN is rewritten as a string rather than by mutating a parsed config:
	// pgxpool.Config.ConnString() returns whatever was parsed, and callers
	// (internal/app) hand that string to the application config.
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "pgxpool connect")
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx), "ping pool")

	return pool
}

// Main runs a package's tests and terminates the shared container afterwards.
// Packages that call Setup wire it as their TestMain.
func Main(m *testing.M) {
	code := m.Run()
	shutdown()
	os.Exit(code)
}

// start brings up the container and migrates the template database once.
func start(ctx context.Context) (*shared, error) {
	initOnce.Do(func() { instance, initErr = launch(ctx) })
	return instance, initErr
}

func launch(ctx context.Context) (*shared, error) {
	container, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase(templateDB),
		postgres.WithUsername(dbUser),
		postgres.WithPassword(dbPassword),
		testcontainers.WithLabels(map[string]string{LabelKey: LabelValue}),
		testcontainers.WithCmdArgs("-c", "max_connections="+maxConns),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}

	s, err := prepare(ctx, container)
	if err != nil {
		_ = container.Terminate(context.Background())
		return nil, err
	}
	return s, nil
}

func prepare(ctx context.Context, container *postgres.PostgresContainer) (*shared, error) {
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, fmt.Errorf("container connection string: %w", err)
	}

	// MigrateUp opens and closes its own database handle, leaving the template
	// without sessions — the precondition for cloning it.
	if err := database.MigrateUp(ctx, dsn); err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	adminDSN, err := dsnForDB(dsn, adminDB)
	if err != nil {
		return nil, err
	}
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		return nil, fmt.Errorf("connect admin pool: %w", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		return nil, fmt.Errorf("ping admin pool: %w", err)
	}

	return &shared{container: container, templateDSN: dsn, admin: admin}, nil
}

func shutdown() {
	if instance == nil {
		return
	}
	instance.admin.Close()
	_ = instance.container.Terminate(context.Background())
	instance = nil
}

// dsnForDB re-points a Postgres URL at another database on the same server.
func dsnForDB(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
