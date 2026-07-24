package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

// TestUnit_Load_QuestDefaults locks the TASK-89 v1.2 procedural-quest defaults
// (SRS §7.5), including the time.Duration and float64 tags whose parse errors
// would otherwise only surface at runtime.
func TestUnit_Load_QuestDefaults(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	q := cfg.Quest
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"TargetRadius", q.TargetRadius, 3},
		{"GoodsRadius", q.GoodsRadius, 3},
		{"DocksMin", q.DocksMin, 10},
		{"DocksMax", q.DocksMax, 20},
		{"JumpsMin", q.JumpsMin, 10},
		{"JumpsMax", q.JumpsMax, 20},
		{"MaxPendingOffers", q.MaxPendingOffers, 3},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Fatalf("Quest.%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if q.StoryShare != 0.25 {
		t.Fatalf("Quest.StoryShare = %v, want 0.25", q.StoryShare)
	}
	if q.OfferTTL.String() != "24h0m0s" {
		t.Fatalf("Quest.OfferTTL = %s, want 24h0m0s", q.OfferTTL)
	}
	if q.RewardBase != 2000 || q.RewardPerHop != 1500 || q.RewardPerUnit != 300 || q.RewardPerEnemy != 4000 {
		t.Fatalf("Quest reward coeffs = (%d,%d,%d,%d), want (2000,1500,300,4000)",
			q.RewardBase, q.RewardPerHop, q.RewardPerUnit, q.RewardPerEnemy)
	}
}

// TestUnit_Load_QuestMinThresholdValidation locks S3: a YAML override that sets
// DocksMin/JumpsMin to 0 is rejected at load rather than silently misbehaving in
// the pacer (a 0 threshold is treated as "not yet rolled" and re-rolled forever).
func TestUnit_Load_QuestMinThresholdValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{"docks_min_zero", "quest:\n  docks_min: 0\n", "DocksMin"},
		{"jumps_min_zero", "quest:\n  jumps_min: 0\n", "JumpsMin"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("CONFIG_PATH", path)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load: expected error for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}
