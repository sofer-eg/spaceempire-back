package combat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// TestUnit_ApplyDamage_ShieldOnly: damage fully absorbed by the shield,
// HP untouched.
func TestUnit_ApplyDamage_ShieldOnly(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 100, HP: 50, MaxHP: 50, MaxShield: 100}
	res := combat.ApplyDamage(ship, 40)
	require.Equal(t, 60, ship.Shield)
	require.Equal(t, 50, ship.HP)
	require.Equal(t, domain.DamageResult{ShieldAbsorbed: 40}, res)
}

// TestUnit_ApplyDamage_ShieldPlusHP: damage exceeds shield — shield
// drains to zero, remainder eats HP.
func TestUnit_ApplyDamage_ShieldPlusHP(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 30, HP: 100, MaxHP: 100, MaxShield: 100}
	res := combat.ApplyDamage(ship, 50)
	require.Equal(t, 0, ship.Shield)
	require.Equal(t, 80, ship.HP)
	require.Equal(t, domain.DamageResult{ShieldAbsorbed: 30, HPAbsorbed: 20}, res)
}

// TestUnit_ApplyDamage_KillsShip: damage > shield + HP — both zeroed,
// overkill recorded, Killed flag set.
func TestUnit_ApplyDamage_KillsShip(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 0, HP: 20, MaxHP: 100, MaxShield: 100}
	res := combat.ApplyDamage(ship, 50)
	require.Equal(t, 0, ship.Shield)
	require.Equal(t, 0, ship.HP)
	require.Equal(t, domain.DamageResult{HPAbsorbed: 20, Overkill: 30, Killed: true}, res)
}

// TestUnit_ApplyDamage_NoShield_GoesStraightToHP: ships without a shield
// module take direct HP damage.
func TestUnit_ApplyDamage_NoShield_GoesStraightToHP(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 0, HP: 100, MaxHP: 100, MaxShield: 0}
	res := combat.ApplyDamage(ship, 30)
	require.Equal(t, 0, ship.Shield)
	require.Equal(t, 70, ship.HP)
	require.Equal(t, domain.DamageResult{HPAbsorbed: 30}, res)
}

// TestUnit_ApplyDamage_Zero: dmg<=0 is a no-op, target untouched.
func TestUnit_ApplyDamage_Zero(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 100, HP: 50, MaxHP: 50, MaxShield: 100}
	res := combat.ApplyDamage(ship, 0)
	require.Equal(t, 100, ship.Shield)
	require.Equal(t, 50, ship.HP)
	require.Equal(t, domain.DamageResult{}, res)
}

// TestUnit_ApplyDamage_Negative: negative dmg is a no-op (no healing).
func TestUnit_ApplyDamage_Negative(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 100, HP: 50, MaxHP: 50, MaxShield: 100}
	res := combat.ApplyDamage(ship, -10)
	require.Equal(t, 100, ship.Shield)
	require.Equal(t, 50, ship.HP)
	require.Equal(t, domain.DamageResult{}, res)
}

// TestUnit_ApplyDamage_AlreadyDead: HP already 0 — a second hit does
// not set Killed (transition didn't happen this call).
func TestUnit_ApplyDamage_AlreadyDead(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Shield: 0, HP: 0, MaxHP: 50, MaxShield: 50}
	res := combat.ApplyDamage(ship, 10)
	require.False(t, res.Killed)
	require.Equal(t, 10, res.Overkill)
	require.Equal(t, 0, ship.HP)
}

// TestUnit_ApplyDamage_NilTarget: nil target is a no-op, zero result.
func TestUnit_ApplyDamage_NilTarget(t *testing.T) {
	t.Parallel()
	res := combat.ApplyDamage(nil, 50)
	require.Equal(t, domain.DamageResult{}, res)
}
