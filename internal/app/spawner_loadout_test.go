package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// realCatalogs loads the shipped ship-class / equipment / base-loadout configs
// so the spawn+buy tests exercise the real ct_npc_ship_modules resolution, not a
// hand-built fixture.
func realCatalogs(t *testing.T) (*balance.ShipClasses, *balance.Equipments, *balance.ShipLoadouts) {
	t.Helper()
	classes, err := balance.LoadShipClassesFromFile("../../configs/ship_classes.yaml")
	require.NoError(t, err)
	equipment, err := balance.LoadEquipmentFromFile("../../configs/equipment.yaml")
	require.NoError(t, err)
	loadouts, err := balance.LoadShipLoadoutsFromFile("../../configs/ship_base_loadout.yaml")
	require.NoError(t, err)
	return classes, equipment, loadouts
}

func hasType(eq []domain.InstalledEquipment, typ string) bool {
	for _, m := range eq {
		if m.Type == typ {
			return true
		}
	}
	return false
}

// TestUnit_BuildStarterShip_ArgonScoutLoadout is the AC #5 spawn check: an
// Argon player (race 1) spawns their M5 Разведчик fitted with exactly the
// ct_npc_ship_modules base kit — up_engine 1, up_shield 1, up_weapon_control 1,
// up_launcher 2, up_pro 4 — with generator/accumulator absent, and the folded
// stats applied (shield/speed/laser boosted, ship fresh-charged).
func TestUnit_BuildStarterShip_ArgonScoutLoadout(t *testing.T) {
	classes, equipment, loadouts := realCatalogs(t)
	cfg := ShipSpawnerConfig{SectorID: 1}.withDefaults()
	s := &shipSpawner{cfg: cfg, classes: classes, equipment: equipment, loadouts: loadouts}

	cls, ok := classes.ScoutForRace(1)
	require.True(t, ok)

	ship := s.buildStarterShip(1, 1, "Разведчик", 1, domain.Vec2{X: 10, Y: 20})

	assert.Equal(t, cls.ID, ship.ShipClassID)
	assert.Equal(t, []domain.InstalledEquipment{
		{EquipmentID: 62, Type: "up_engine", Level: 1},
		{EquipmentID: 42, Type: "up_shield", Level: 1},
		{EquipmentID: 71, Type: "up_weapon_control", Level: 1},
		{EquipmentID: 5, Type: "up_launcher", Level: 2},
		{EquipmentID: 14, Type: "up_pro", Level: 4},
	}, ship.Equipment)
	assert.False(t, hasType(ship.Equipment, "up_generator"), "no generator in the base kit")
	assert.False(t, hasType(ship.Equipment, "up_accumulator"), "no accumulator in the base kit")

	// Stats are folded, not bypassed: shield (up_shield +15% + up_pro +40%),
	// speed (up_engine +8%) and laser (up_weapon_control +8%) all exceed the bare
	// class baseline.
	base := baseShipStats(cls, cfg)
	assert.Greater(t, ship.MaxShield, base.MaxShield, "up_shield+up_pro raised max shield")
	assert.Greater(t, ship.MaxSpeed, base.MaxSpeed, "up_engine raised max speed")
	assert.Greater(t, ship.LaserDamage, base.LaserDamage, "up_weapon_control raised laser")

	// Fresh ship arrives fully charged; the two "always" modules (up_shield,
	// up_pro) give a −200 per-tick energy delta (100 each), like the outfit path.
	assert.Equal(t, ship.MaxShield, ship.Shield)
	assert.Equal(t, ship.MaxEnergy, ship.Energy)
	assert.Equal(t, -200, ship.EnergyDelta)
}

// TestUnit_BuildStarterShip_NoLoadoutNilDeps keeps the pre-task behaviour: with
// no loadout/equipment catalog wired the starter spawns bare (Equipment nil).
func TestUnit_BuildStarterShip_NoLoadoutNilDeps(t *testing.T) {
	classes, _, _ := realCatalogs(t)
	s := &shipSpawner{cfg: ShipSpawnerConfig{SectorID: 1}.withDefaults(), classes: classes}

	ship := s.buildStarterShip(1, 1, "Разведчик", 1, domain.Vec2{})
	assert.Nil(t, ship.Equipment, "no loadouts wired → bare ship")
}

// TestUnit_BuildPurchasedShip_FoldsBaseLoadout covers the buy path (AC #3) and a
// couple of non-scout models: a carrier (turret L3) and a transport (no weapon
// control), both fitted with their base kit, folded, and free of gen/accumulator.
func TestUnit_BuildPurchasedShip_FoldsBaseLoadout(t *testing.T) {
	classes, equipment, loadouts := realCatalogs(t)
	cfg := ShipSpawnerConfig{}.withDefaults()
	srv := &outfitServer{classes: classes, equipment: equipment, loadouts: loadouts, cfg: cfg}

	// Argon Колосс (M1 carrier, class 1 / race1 type1, id 73): full six-module
	// kit incl. up_turret_control L3; three "always" modules → −300 delta.
	carrier, ok := classes.GetShipClass(73)
	require.True(t, ok)
	got := srv.buildPurchasedShip(1, carrier, 1, 1, domain.Vec2{}, 100)
	assert.Equal(t, []domain.InstalledEquipment{
		{EquipmentID: 58, Type: "up_engine", Level: 1},
		{EquipmentID: 38, Type: "up_shield", Level: 1},
		{EquipmentID: 67, Type: "up_weapon_control", Level: 1},
		{EquipmentID: 113, Type: "up_turret_control", Level: 3},
		{EquipmentID: 1, Type: "up_launcher", Level: 5},
		{EquipmentID: 10, Type: "up_pro", Level: 5},
	}, got.Equipment)
	assert.False(t, hasType(got.Equipment, "up_generator"))
	assert.False(t, hasType(got.Equipment, "up_accumulator"))
	assert.Equal(t, -300, got.EnergyDelta, "up_shield + up_turret_control + up_pro drain 100 each")
	assert.Equal(t, got.MaxShield, got.Shield, "bought ship arrives fully charged")
	assert.Greater(t, got.MaxShield, baseShipStats(carrier, cfg).MaxShield)

	// Argon Меркурий (transport, class 9 / race1 type9, id 81): no
	// up_weapon_control, so laser is unboosted; only up_shield/up_pro drain.
	transport, ok := classes.GetShipClass(81)
	require.True(t, ok)
	gotT := srv.buildPurchasedShip(1, transport, 1, 1, domain.Vec2{}, 100)
	assert.False(t, hasType(gotT.Equipment, "up_weapon_control"), "type 9 has no weapon control")
	assert.False(t, hasType(gotT.Equipment, "up_turret_control"))
	assert.Equal(t, baseShipStats(transport, cfg).LaserDamage, gotT.LaserDamage, "no weapon control → laser unchanged")
	assert.Equal(t, -200, gotT.EnergyDelta)
}
