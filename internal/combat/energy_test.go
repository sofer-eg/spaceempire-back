package combat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

func TestUnit_ChargeEnergy_BelowMax(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Energy: 50, MaxEnergy: 100, EnergyRecharge: 10}
	require.True(t, combat.ChargeEnergy(ship))
	require.Equal(t, 60, ship.Energy)
}

func TestUnit_ChargeEnergy_AtMax(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Energy: 100, MaxEnergy: 100, EnergyRecharge: 10}
	require.False(t, combat.ChargeEnergy(ship))
	require.Equal(t, 100, ship.Energy)
}

func TestUnit_ChargeEnergy_Clamps(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Energy: 95, MaxEnergy: 100, EnergyRecharge: 10}
	require.True(t, combat.ChargeEnergy(ship))
	require.Equal(t, 100, ship.Energy)
}

func TestUnit_ChargeEnergy_NoPowerplant(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{Energy: 0, MaxEnergy: 0, EnergyRecharge: 10}
	require.False(t, combat.ChargeEnergy(ship))
	require.Equal(t, 0, ship.Energy)
}

func TestUnit_ChargeEnergy_NilSafe(t *testing.T) {
	t.Parallel()
	require.False(t, combat.ChargeEnergy(nil))
}

func TestUnit_ChargeEnergy_AlwaysModuleDrains(t *testing.T) {
	t.Parallel()
	// An "always" consumer (negative EnergyDelta) outpacing the powerplant
	// drains the pool: recharge 2 − usage 100 = −98 per tick.
	ship := &domain.Ship{Energy: 200, MaxEnergy: 200, EnergyRecharge: 2, EnergyDelta: -100}
	require.True(t, combat.ChargeEnergy(ship))
	require.Equal(t, 102, ship.Energy)
}

func TestUnit_ChargeEnergy_ReverseModuleAdds(t *testing.T) {
	t.Parallel()
	// A "reverse" generator (positive EnergyDelta) adds on top of recharge.
	ship := &domain.Ship{Energy: 50, MaxEnergy: 1000, EnergyRecharge: 2, EnergyDelta: 100}
	require.True(t, combat.ChargeEnergy(ship))
	require.Equal(t, 152, ship.Energy)
}

func TestUnit_ChargeEnergy_DrainClampsAtZero(t *testing.T) {
	t.Parallel()
	// Net negative below the floor clamps to 0 (module then counts as unpowered).
	ship := &domain.Ship{Energy: 40, MaxEnergy: 200, EnergyRecharge: 0, EnergyDelta: -100}
	require.True(t, combat.ChargeEnergy(ship))
	require.Equal(t, 0, ship.Energy)
	// Already at 0 with a negative net: no change, no dirty mark.
	require.False(t, combat.ChargeEnergy(ship))
	require.Equal(t, 0, ship.Energy)
}
