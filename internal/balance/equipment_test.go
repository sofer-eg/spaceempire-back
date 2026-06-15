package balance_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
)

func TestUnit_NewEquipments_BuildsAndLooksUp(t *testing.T) {
	cat, err := balance.NewEquipments([]balance.Equipment{
		{ID: 1, Type: "up_engine", Description: "Двигатель", ShipClass: 5},
		{ID: 2, Type: "up_engine", Description: "Двигатель", ShipClass: 6},
		{ID: 3, Type: "up_shield", Description: "Проектор", ShipClass: 5},
	})
	require.NoError(t, err)
	require.Equal(t, 3, cat.EquipmentCount())

	e, ok := cat.GetEquipment(2)
	require.True(t, ok)
	assert.Equal(t, "up_engine", e.Type)
	assert.Equal(t, 6, e.ShipClass)

	assert.Len(t, cat.EquipmentByType("up_engine"), 2)
	assert.Len(t, cat.EquipmentByShipClass(5), 2)

	_, ok = cat.GetEquipment(999)
	assert.False(t, ok)
}

func TestUnit_NewEquipments_Rejects(t *testing.T) {
	_, err := balance.NewEquipments([]balance.Equipment{{ID: 0, Type: "x"}})
	require.ErrorIs(t, err, balance.ErrInvalidEquipmentID)

	_, err = balance.NewEquipments([]balance.Equipment{{ID: 1, Type: ""}})
	require.ErrorIs(t, err, balance.ErrEmptyEquipmentType)

	_, err = balance.NewEquipments([]balance.Equipment{{ID: 1, Type: "a"}, {ID: 1, Type: "b"}})
	require.ErrorIs(t, err, balance.ErrDuplicateEquipmentID)
}

// TestUnit_LoadEquipment_RealConfig loads the converted catalog and checks it
// against ct_updates plus the X-BTF gap upgrades: 141 rows, 24 distinct types
// (the +2 are up_rudder phase 10.3.15 and up_cargobay phase 10.3.16), with
// spot-checks of the core modules and the up_accumulator→up_generator energy
// dependency.
func TestUnit_LoadEquipment_RealConfig(t *testing.T) {
	cat, err := balance.LoadEquipmentFromFile("../../configs/equipment.yaml")
	require.NoError(t, err)
	require.Equal(t, 141, cat.EquipmentCount())

	seenTypes := map[string]bool{}
	for _, e := range cat.AllEquipment() {
		seenTypes[e.Type] = true
	}
	assert.Len(t, seenTypes, 24, "24 distinct equipment types")
	for _, want := range []string{"up_engine", "up_shield", "up_drill", "up_jump_drive", "up_generator", "up_accumulator", "up_rudder", "up_cargobay"} {
		assert.Truef(t, seenTypes[want], "type %s must be present", want)
	}

	// id 95 = up_drill M1 (class 1): price 1440000, depends on up_accumulator.
	e, ok := cat.GetEquipment(95)
	require.True(t, ok)
	assert.Equal(t, "up_drill", e.Type)
	assert.Equal(t, "Установка для добычи руды", e.Description)
	assert.EqualValues(t, 1440000, e.Price)
	assert.Equal(t, "up_accumulator", e.Dependance)

	// Energy chain: up_generator produces (reverse, no dependency); the
	// accumulator holds and depends on the generator.
	for _, gen := range cat.EquipmentByType("up_generator") {
		assert.Equal(t, "reverse", gen.EnergyUseType)
		assert.Equal(t, "none", gen.Dependance)
	}
	for _, acc := range cat.EquipmentByType("up_accumulator") {
		assert.Equal(t, "hold", acc.EnergyUseType)
		assert.Equal(t, "up_generator", acc.Dependance)
	}
}
