package balance_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// TestUnit_LoadShipLoadouts_RealConfig loads the converted base-loadout catalog
// (ct_npc_ship_modules) and checks it against the 86 ship-class models: 72
// standard race×type models carry a set, the 14 special models spawn bare. It
// spot-checks the Argon scout (race 1 / type 5) exactly and asserts generator/
// accumulator never appear in a base kit (players buy those themselves).
func TestUnit_LoadShipLoadouts_RealConfig(t *testing.T) {
	cat, err := balance.LoadShipLoadoutsFromFile("../../configs/ship_base_loadout.yaml")
	require.NoError(t, err)

	assert.Equal(t, 86, cat.TotalCount(), "one loadout entry per ship-class model")
	assert.Equal(t, 72, cat.LoadoutCount(), "72 standard race×type models carry a base kit")

	// Argon Разведчик (race 1 / type 5) — the starter ship. Exact set, in order.
	scout := cat.BaseLoadout(1, 5)
	assert.Equal(t, []domain.InstalledEquipment{
		{EquipmentID: 62, Type: "up_engine", Level: 1},
		{EquipmentID: 42, Type: "up_shield", Level: 1},
		{EquipmentID: 71, Type: "up_weapon_control", Level: 1},
		{EquipmentID: 5, Type: "up_launcher", Level: 2},
		{EquipmentID: 14, Type: "up_pro", Level: 4},
	}, scout)

	// A special model (race 100 / type 10, the Hyperion) spawns bare.
	assert.Nil(t, cat.BaseLoadout(100, 10), "special models have no base kit")
	// Unknown race+type also yields nil (no panic).
	assert.Nil(t, cat.BaseLoadout(42, 42))

	// No base kit fits a generator or accumulator — the launcher/pro sit on the
	// hull without the energy chain, exactly like the original spawn.
	for _, rt := range [][2]int{{1, 1}, {1, 5}, {1, 7}, {7, 8}, {8, 8}} {
		for _, m := range cat.BaseLoadout(rt[0], rt[1]) {
			assert.NotEqual(t, "up_generator", m.Type, "race %d type %d", rt[0], rt[1])
			assert.NotEqual(t, "up_accumulator", m.Type, "race %d type %d", rt[0], rt[1])
		}
	}

	// The level clamp is baked into the config: class-7 up_pro is capped at L4
	// (catalog max), and race 7 / type 8 up_turret_control at L1.
	pro := findModule(cat.BaseLoadout(1, 7), "up_pro")
	require.NotNil(t, pro)
	assert.Equal(t, 4, pro.Level, "up_pro clamped to catalog max_level 4 for class 7")

	turret := findModule(cat.BaseLoadout(7, 8), "up_turret_control")
	require.NotNil(t, turret)
	assert.Equal(t, 1, turret.Level, "up_turret_control clamped to L1 for race 7 / type 8")

	// A type-9 transport carries no weapon_control (only launcher/pro/engine/
	// shield), matching ct_npc_ship_modules.
	assert.Nil(t, findModule(cat.BaseLoadout(1, 9), "up_weapon_control"))
}

// TestUnit_ShipLoadouts_BaseLoadoutReturnsCopy verifies BaseLoadout hands back a
// defensive copy — mutating the result must not corrupt the shared catalog.
func TestUnit_ShipLoadouts_BaseLoadoutReturnsCopy(t *testing.T) {
	cat, err := balance.NewShipLoadouts([]balance.ShipLoadout{
		{Race: 1, Type: 5, Modules: []domain.InstalledEquipment{
			{EquipmentID: 62, Type: "up_engine", Level: 1},
		}},
	})
	require.NoError(t, err)

	got := cat.BaseLoadout(1, 5)
	got[0].Level = 99
	assert.Equal(t, 1, cat.BaseLoadout(1, 5)[0].Level, "catalog must be immutable through the returned slice")
}

// TestUnit_NewShipLoadouts_Validation rejects duplicate keys and malformed
// modules.
func TestUnit_NewShipLoadouts_Validation(t *testing.T) {
	_, err := balance.NewShipLoadouts([]balance.ShipLoadout{
		{Race: 1, Type: 5}, {Race: 1, Type: 5},
	})
	assert.ErrorIs(t, err, balance.ErrDuplicateShipLoadout)

	_, err = balance.NewShipLoadouts([]balance.ShipLoadout{
		{Race: 1, Type: 5, Modules: []domain.InstalledEquipment{{Type: "up_engine"}}},
	})
	assert.ErrorIs(t, err, balance.ErrInvalidLoadoutEquipmentID)

	_, err = balance.NewShipLoadouts([]balance.ShipLoadout{
		{Race: 1, Type: 5, Modules: []domain.InstalledEquipment{{EquipmentID: 62}}},
	})
	assert.ErrorIs(t, err, balance.ErrEmptyLoadoutModuleType)
}

func findModule(mods []domain.InstalledEquipment, typ string) *domain.InstalledEquipment {
	for i := range mods {
		if mods[i].Type == typ {
			return &mods[i]
		}
	}
	return nil
}
