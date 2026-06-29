package balance_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
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
// against ct_updates plus the X-BTF gap upgrades: 143 rows, 26 distinct types
// (the +4 are up_rudder phase 10.3.15, up_cargobay phase 10.3.16,
// up_ore_scanner phase 10.3.19 and up_transporter phase 10.3.18), with
// spot-checks of the core modules and the up_accumulator→up_generator
// energy dependency.
func TestUnit_LoadEquipment_RealConfig(t *testing.T) {
	cat, err := balance.LoadEquipmentFromFile("../../configs/equipment.yaml")
	require.NoError(t, err)
	require.Equal(t, 143, cat.EquipmentCount())

	seenTypes := map[string]bool{}
	for _, e := range cat.AllEquipment() {
		seenTypes[e.Type] = true
	}
	assert.Len(t, seenTypes, 26, "26 distinct equipment types")
	for _, want := range []string{"up_engine", "up_shield", "up_drill", "up_jump_drive", "up_generator", "up_accumulator", "up_rudder", "up_cargobay", "up_ore_scanner", "up_transporter"} {
		assert.Truef(t, seenTypes[want], "type %s must be present", want)
	}

	// id 142 = up_ore_scanner (any class): a passive info module — single level,
	// no energy draw, no dependency (phase 10.3.19).
	ore, ok := cat.GetEquipment(142)
	require.True(t, ok)
	assert.Equal(t, "up_ore_scanner", ore.Type)
	assert.Equal(t, 0, ore.ShipClass, "available to any ship class")
	assert.Equal(t, 1, ore.MaxLevel)

	// id 143 = up_transporter (any class): an action module — spends energy on
	// each cargo teleport (phase 10.3.18).
	tr, ok := cat.GetEquipment(143)
	require.True(t, ok)
	assert.Equal(t, "up_transporter", tr.Type)
	assert.Equal(t, "action", tr.EnergyUseType)
	assert.Equal(t, 50, tr.EnergyUsage)

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

// TestUnit_LoadEquipment_TorpedoLauncherWarGate verifies the rank gate of the
// torpedo launcher (ids 123-128, one row per ship class): in StarWind torpedoes
// were gated on military status (war_rate), the only axis ever enforced, while
// min_race_rate was dead config (ЧТЗ doc-1 C-06, TASK-100.3.14). So every row
// must gate min_war_rate=2 (not min_race_rate). Enforcement is the generic
// ResolveInstall path (gatedInstall→422 ErrRankTooLow at the handler).
func TestUnit_LoadEquipment_TorpedoLauncherWarGate(t *testing.T) {
	cat, err := balance.LoadEquipmentFromFile("../../configs/equipment.yaml")
	require.NoError(t, err)

	rows := cat.EquipmentByType("up_torpedo_launcher")
	require.Len(t, rows, 6, "one up_torpedo_launcher row per ship class (ids 123-128)")

	// The launcher depends on up_accumulator; satisfy that so the rank gate is
	// what the install resolves on.
	installed := []domain.InstalledEquipment{{Type: "up_accumulator"}}
	for _, row := range rows {
		assert.Equalf(t, 2, row.MinWarRate, "row %d must gate min_war_rate=2", row.ID)
		assert.Equalf(t, 0, row.MinRaceRate, "row %d must not gate min_race_rate", row.ID)

		_, err := cat.ResolveInstall(row.ID, row.ShipClass, 0, 1, installed, balance.Reputation{War: 2})
		require.NoErrorf(t, err, "row %d must install at war_rate=2", row.ID)

		_, err = cat.ResolveInstall(row.ID, row.ShipClass, 0, 1, installed, balance.Reputation{War: 1})
		require.ErrorIsf(t, err, balance.ErrRankTooLow, "row %d must reject war_rate=1", row.ID)
	}
}
