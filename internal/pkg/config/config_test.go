package config_test

import (
	"testing"

	"spaceempire/back/internal/pkg/config"
)

func TestUnit_Load_DefaultPort(t *testing.T) {
	t.Setenv("SE_SERVER_PORT", "")
	t.Setenv("CONFIG_PATH", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Fatalf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
}

func TestUnit_Load_EnvOverride(t *testing.T) {
	t.Setenv("SE_SERVER_PORT", "9090")
	t.Setenv("CONFIG_PATH", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Fatalf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
}

func TestUnit_Load_SectorDefaults(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("SE_SECTOR_TICK_INTERVAL", "")
	t.Setenv("SE_SECTOR_INBOX_CAPACITY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Sector.TickInterval.String() != "3s" {
		t.Fatalf("Sector.TickInterval = %s, want 3s", cfg.Sector.TickInterval)
	}
	if cfg.Sector.InboxCapacity != 256 {
		t.Fatalf("Sector.InboxCapacity = %d, want 256", cfg.Sector.InboxCapacity)
	}
}

func TestUnit_Load_SectorEnvOverride(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("SE_SECTOR_TICK_INTERVAL", "250ms")
	t.Setenv("SE_SECTOR_INBOX_CAPACITY", "512")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Sector.TickInterval.String() != "250ms" {
		t.Fatalf("Sector.TickInterval = %s, want 250ms", cfg.Sector.TickInterval)
	}
	if cfg.Sector.InboxCapacity != 512 {
		t.Fatalf("Sector.InboxCapacity = %d, want 512", cfg.Sector.InboxCapacity)
	}
}
