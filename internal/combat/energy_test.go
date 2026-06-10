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
