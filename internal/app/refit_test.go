package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

func refitFixture(t *testing.T) equipmentRefitter {
	t.Helper()
	classes, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 1, Race: 1, Class: 5, Name: "Разведчик", Speed: 100, Acceleration: 10, Shield: 1000, ShieldCharge: 50, Laser: 40, Radar: 300, CargoBay: 200},
	})
	require.NoError(t, err)
	equip, err := balance.NewEquipments([]balance.Equipment{
		{ID: 60, Type: "up_shield", Position: 1, MaxLevel: 1},
		{ID: 42, Type: "up_engine", Position: 2, MaxLevel: 1},
	})
	require.NoError(t, err)
	return equipmentRefitter{classes: classes, equipment: equip, cfg: ShipSpawnerConfig{SectorID: 1}.withDefaults()}
}

// After a knockoff strips up_shield, Refit recomputes the fit from the reduced
// equipment list: the shield boost is gone (MaxShield back to the class base)
// while the surviving up_engine's speed boost stays. Current Shield is clamped
// to the new maximum.
func TestUnit_EquipmentRefitter_DropsKnockedModuleBoost(t *testing.T) {
	t.Parallel()
	r := refitFixture(t)

	ship := &domain.Ship{
		ShipClassID: 1,
		Equipment:   []domain.InstalledEquipment{{EquipmentID: 42, Type: "up_engine", Level: 1}},
		Shield:      1150, // was boosted by the now-gone up_shield
	}
	r.Refit(ship)

	assert.Equal(t, 1000, ship.MaxShield, "up_shield boost removed → class-base shield")
	assert.InDelta(t, 108.0, ship.MaxSpeed, 0.001, "surviving up_engine keeps its +8% speed")
	assert.Equal(t, 1000, ship.Shield, "current shield clamped down to the new max")
}

// A ship with an unknown class (spacesuit / legacy) has no base stats, so Refit
// leaves it untouched.
func TestUnit_EquipmentRefitter_UnknownClassNoOp(t *testing.T) {
	t.Parallel()
	r := refitFixture(t)

	ship := &domain.Ship{ShipClassID: 999, MaxSpeed: 7, MaxShield: 3}
	r.Refit(ship)

	assert.Equal(t, 7.0, ship.MaxSpeed)
	assert.Equal(t, 3, ship.MaxShield)
}
