package combat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// TestUnit_ChargeShield_BelowMax: regular charge cycle — shield strictly
// below max, gets bumped by ShieldRecharge, return true.
func TestUnit_ChargeShield_BelowMax(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 50, MaxShield: 100, ShieldRecharge: 10}
	require.True(t, combat.ChargeShield(ship))
	require.Equal(t, 60, ship.Shield)
}

// TestUnit_ChargeShield_AtMax: shield already full — no-op, return false.
func TestUnit_ChargeShield_AtMax(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 100, MaxShield: 100, ShieldRecharge: 10}
	require.False(t, combat.ChargeShield(ship))
	require.Equal(t, 100, ship.Shield)
}

// TestUnit_ChargeShield_Clamps: charge would overshoot — clamp to max,
// return true (value did change).
func TestUnit_ChargeShield_Clamps(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 95, MaxShield: 100, ShieldRecharge: 10}
	require.True(t, combat.ChargeShield(ship))
	require.Equal(t, 100, ship.Shield)
}

// TestUnit_ChargeShield_NoShieldModule: ships without a shield module
// (MaxShield==0) are skipped entirely — matches the old up_shield=0
// branch in TO_ShipShieldCharge.
func TestUnit_ChargeShield_NoShieldModule(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 0, MaxShield: 0, ShieldRecharge: 10}
	require.False(t, combat.ChargeShield(ship))
	require.Equal(t, 0, ship.Shield)
}

// TestUnit_ChargeShield_AboveMaxClampsDown: defensive clamp for ships
// that arrive with Shield>MaxShield (e.g. after a class change). Returns
// true to flag the worker dirty so the corrected value persists.
func TestUnit_ChargeShield_AboveMaxClampsDown(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 150, MaxShield: 100, ShieldRecharge: 10}
	require.True(t, combat.ChargeShield(ship))
	require.Equal(t, 100, ship.Shield)
}

// TestUnit_ChargeShield_NilSafe: nil pointer must not panic — worker
// tick walks a map, but defensive nil-check protects future callers.
func TestUnit_ChargeShield_NilSafe(t *testing.T) {
	t.Parallel()
	require.False(t, combat.ChargeShield(nil))
}
