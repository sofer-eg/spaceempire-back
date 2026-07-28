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
// `go test -timeout` panics the whole binary and skips every cleanup, so the
// containers started here are labelled for an external sweep: RunLabelKey with
// the id in RunIDEnv, which `make test` and `make test-integration` reap
// automatically at the end of the invocation that stamped it, and
// LabelKey/LabelValue, which `make test-clean` sweeps wholesale by hand
// (including containers from a `go test` run that carried no id). Neither ever
// matches on the image name, so unrelated containers stay out of scope.
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

	// LabelKey/LabelValue mark every container this package starts — the label
	// the manual `make test-clean` sweep matches on. See the package doc for how
	// it and the run label divide the work.
	LabelKey   = "spaceempire.test"
	LabelValue = "true"

	// RunIDEnv carries a per-invocation id from the Makefile; RunLabelKey is the
	// label it is stamped as. It scopes the automatic cleanup to the containers
	// of the run that is finishing, so two runs sharing a docker host do not
	// tear down each other's databases. Empty when tests are run directly, in
	// which case the containers carry only the project label.
	RunIDEnv    = "SE_TEST_RUN_ID"
	RunLabelKey = "spaceempire.test.run"

	// serverMaxConns raises the server-side ceiling above the default 100, and
	// poolMaxConns pins what each pool may take from it — pgxpool would
	// otherwise default to max(4, NumCPU) and make the budget depend on the
	// machine. A package runs at most -parallel (GOMAXPROCS) tests at once; the
	// heaviest of them, internal/app, opens a second pool of its own per test
	// (config.PostgresConfig.MaxConns, 4 there), so the worst case is
	// GOMAXPROCS*8 + 4 — within 200 up to a 24-core runner.
	serverMaxConns = "200"
	poolMaxConns   = 4
	adminMaxConns  = 4
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
	// mainCalled records that the package wired testdb.Main as its TestMain.
	// Without it nothing would ever terminate the container, and the omission
	// is invisible: the tests still pass. Setup refuses to run instead.
	mainCalled atomic.Bool
)

// missingTestMain is what a package sees when it calls Setup without wiring
// Main. Spelled out because the fix is one file the author has no reason to
// know about — integration tests are usually written by copying a neighbour.
const missingTestMain = `testdb.Setup requires this package to declare a TestMain:

    func TestMain(m *testing.M) { testdb.Main(m) }

Add it as testmain_test.go (copy one from a neighbouring package). Without it
the package's Postgres container is never terminated and leaks past the run.
See back/README.md, "Integration tests".`

// Setup returns a pgxpool connected to a private, fully migrated database.
// The database is cloned from the package-wide template; it and the pool are
// released via t.Cleanup, so callers do not need to defer anything.
func Setup(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	if !mainCalled.Load() {
		t.Fatal(missingTestMain)
	}

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
	// (internal/app) hand that string to the application config. Overriding
	// MaxConns is safe in the same respect — it leaves ConnString untouched.
	pool, err := open(ctx, dsn, poolMaxConns)
	require.NoError(t, err, "pgxpool connect")
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx), "ping pool")

	return pool
}

// Main runs a package's tests and terminates the shared container afterwards.
// Packages that call Setup wire it as their TestMain.
func Main(m *testing.M) {
	mainCalled.Store(true)
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
		testcontainers.WithLabels(labels()),
		testcontainers.WithCmdArgs("-c", "max_connections="+serverMaxConns),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		// postgres.Run returns a usable handle alongside the error whenever the
		// container was created — a startup-timeout under load is exactly that
		// case. Dropping it here would leak a running Postgres that nothing
		// afterwards knows about: shutdown only sees a successful instance.
		if container != nil {
			_ = container.Terminate(context.Background())
		}
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
	admin, err := open(ctx, adminDSN, adminMaxConns)
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

// labels are stamped on the container: the project label always, the run label
// when the Makefile supplied a run id.
func labels() map[string]string {
	l := map[string]string{LabelKey: LabelValue}
	if id := os.Getenv(RunIDEnv); id != "" {
		l[RunLabelKey] = id
	}
	return l
}

// open connects a pool with an explicit MaxConns, leaving ConnString intact.
func open(ctx context.Context, dsn string, conns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pool dsn: %w", err)
	}
	cfg.MaxConns = conns
	return pgxpool.NewWithConfig(ctx, cfg)
}

// dsnForDB re-points a Postgres URL at another database on the same server.
// Only the URL form is accepted: url.Parse takes a keyword/value DSN
// ("host=localhost dbname=x") without complaint and parks the whole string in
// Path, which would yield a silently corrupt result rather than an error.
func dsnForDB(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("dsn %q is not a postgres URL", dsn)
	}
	u.Path = "/" + name
	return u.String(), nil
}
