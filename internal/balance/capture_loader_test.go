package balance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "capture.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// A fully-specified capture.yaml is read verbatim.
func TestUnit_LoadCaptureConfig_ReadsFields(t *testing.T) {
	t.Parallel()
	path := writeTemp(t, `capture:
  knock_critical_shield_charge: 0.25
  knock_critical_hull_integrity: 0.6
  knock_external_base: 0.3
  knock_internal_base: 0.15
`)
	cfg, err := balance.LoadCaptureConfigFromFile(path)
	require.NoError(t, err)
	assert.InDelta(t, 0.25, cfg.KnockCriticalShieldCharge, 1e-9)
	assert.InDelta(t, 0.6, cfg.KnockCriticalHullIntegrity, 1e-9)
	assert.InDelta(t, 0.3, cfg.KnockExternalBase, 1e-9)
	assert.InDelta(t, 0.15, cfg.KnockInternalBase, 1e-9)
}

// Missing fields fall back to the faithful DestroyModule defaults (ЧТЗ §5.1).
func TestUnit_LoadCaptureConfig_FillsDefaults(t *testing.T) {
	t.Parallel()
	path := writeTemp(t, "capture:\n  knock_external_base: 0.3\n")
	cfg, err := balance.LoadCaptureConfigFromFile(path)
	require.NoError(t, err)
	assert.InDelta(t, 0.3, cfg.KnockExternalBase, 1e-9, "explicit field kept")
	assert.InDelta(t, 0.2, cfg.KnockCriticalShieldCharge, 1e-9, "missing → default")
	assert.InDelta(t, 0.7, cfg.KnockCriticalHullIntegrity, 1e-9, "missing → default")
	assert.InDelta(t, 0.1, cfg.KnockInternalBase, 1e-9, "missing → default")
}

// The in-tree configs/capture.yaml loads and matches the faithful profile.
func TestUnit_LoadCaptureConfig_RepoConfig(t *testing.T) {
	t.Parallel()
	cfg, err := balance.LoadCaptureConfigFromFile("../../configs/capture.yaml")
	require.NoError(t, err)
	assert.Equal(t, balance.DefaultCaptureConfig(), cfg)
}
