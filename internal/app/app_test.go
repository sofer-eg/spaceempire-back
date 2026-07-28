package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"spaceempire/back/internal/app"
	"spaceempire/back/internal/pkg/config"
	"spaceempire/back/internal/pkg/database/testdb"
)

const (
	// healthzBudget bounds the wait for a freshly started app to answer
	// /healthz. It is an upper bound on "the boot is wedged", not an expected
	// duration: the poll loop below leaves the moment the server answers.
	//
	// The previous hard 2 s failed every single run under -race (TASK-150).
	// Cold start loads six balance YAMLs, opens the pool, then loads every
	// sector's statics and spawns their NPCs — and -race multiplies all of it,
	// while the sibling integration tests in this package each hold their own
	// Postgres testcontainer in parallel. The budget is deliberately far above
	// the observed cold start, yet far below the package's `-timeout 180s`, so
	// a genuine hang still fails here with a readable message instead of a
	// whole-binary timeout panic.
	healthzBudget = 60 * time.Second

	// shutdownBudget bounds the wait for app.Run to return after ctx cancel.
	// Run shuts the HTTP server down (Server.ShutdownTimeout below) and then
	// joins the worker goroutines — measured at ~0.8 s, so this is a 6x margin.
	//
	// Deliberately NOT aligned with healthzBudget above: cold start opens the
	// pool and spawns every sector's NPCs, while shutdown is bounded by
	// ShutdownTimeout plus a goroutine join. Different quantities, different
	// budgets. Keeping this one tight is part of what the test asserts — a
	// regression that made graceful shutdown take 20 s should fail here rather
	// than pass unnoticed. Raise it only with a measurement showing this budget
	// is itself what flakes.
	shutdownBudget = 5 * time.Second
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func TestIntegration_App_StartsAndShutsDownGracefully(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	dsn := pool.Config().ConnString()

	port := freePort(t)
	cfg := &config.Config{
		Server:   config.ServerConfig{Port: port, ShutdownTimeout: 2 * time.Second},
		Sector:   config.SectorConfig{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		Postgres: config.PostgresConfig{DSN: dsn, MaxConns: 4, ConnTimeout: 5 * time.Second},
		Auth:     config.AuthConfig{SessionTTL: time.Hour, BcryptCost: 4},
		Balance: config.BalanceConfig{
			Path:             "../../configs/balance.yaml",
			ShipClassesPath:  "../../configs/ship_classes.yaml",
			StationTypesPath: "../../configs/station_types.yaml",
			EquipmentPath:    "../../configs/equipment.yaml",
			ShipLoadoutPath:  "../../configs/ship_base_loadout.yaml",
			CapturePath:      "../../configs/capture.yaml",
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, cfg, logger) }()

	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	started := time.Now()
	deadline := started.Add(healthzBudget)
	var resp *http.Response
	for {
		// A Run that has already given up (bad DSN, migration failure, port
		// clash) must report its own error. Without this the loop would keep
		// polling a dead listener until the deadline and blame "never
		// responded", hiding the real cause.
		select {
		case err := <-done:
			t.Fatalf("Run returned before /healthz answered: %v", err)
		default:
		}

		var err error
		resp, err = http.Get(url) //nolint:noctx // short-lived test client
		if err == nil {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("server never responded on /healthz within %s (last error: %v)", healthzBudget, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("healthz answered after %s", time.Since(started).Round(time.Millisecond))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	canceledAt := time.Now()
	cancel()
	select {
	case err := <-done:
		t.Logf("Run returned %s after cancel", time.Since(canceledAt).Round(time.Millisecond))
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(shutdownBudget):
		t.Fatalf("Run did not return within %s of ctx cancel", shutdownBudget)
	}
}
