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
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, cfg, logger) }()

	deadline := time.Now().Add(2 * time.Second)
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	var resp *http.Response
	for time.Now().Before(deadline) {
		var err error
		resp, err = http.Get(url) //nolint:noctx // short-lived test client
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if resp == nil {
		t.Fatal("server never responded on /healthz")
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancel")
	}
}
