package combat_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// laserEnergyCost mirrors the spawn default ShipSpawnerConfig.StartLaserECost — a
// laser shot costs 5 energy (TASK-100.3.25 keeps it fixed; the pool/recharge are
// what calibrate fire duration).
const laserEnergyCost = 5

// energyModel loads the three shipped catalogs the energy calibration spans.
func energyModel(t *testing.T) (*balance.ShipClasses, *balance.Equipments, *balance.ShipLoadouts) {
	t.Helper()
	classes, err := balance.LoadShipClassesFromFile(filepath.Join("..", "..", "configs", "ship_classes.yaml"))
	require.NoError(t, err)
	equipment, err := balance.LoadEquipmentFromFile(filepath.Join("..", "..", "configs", "equipment.yaml"))
	require.NoError(t, err)
	loadouts, err := balance.LoadShipLoadoutsFromFile(filepath.Join("..", "..", "configs", "ship_base_loadout.yaml"))
	require.NoError(t, err)
	return classes, equipment, loadouts
}

// argonShip returns the Argon (race 1) ship of the given per-race type. For
// Argon type == gameplay class number (1..9), so this indexes the nine standard
// classes for the fire-duration table.
func argonShip(t *testing.T, classes *balance.ShipClasses, shipType int) balance.ShipClass {
	t.Helper()
	for _, c := range classes.ShipClassesByRace(1) {
		if c.Type == shipType {
			return c
		}
	}
	t.Fatalf("no Argon ship of type %d", shipType)
	return balance.ShipClass{}
}

// TestUnit_EnergyModel_PerClassFireDuration is the AC #14 calibration check: a
// ship firing its base-kit laser continuously lasts ~its per-class target number
// of ticks (±15%). The base kit carries no generator, so energyDelta is the
// negative always-drain and the pool depletes at recharge+delta−laserCost/tick.
func TestUnit_EnergyModel_PerClassFireDuration(t *testing.T) {
	classes, equipment, loadouts := energyModel(t)

	// Per-race-type (== Argon class number) target continuous-fire ticks.
	targets := map[int]int{
		1: 100, // M1 Носитель
		2: 100, // M2 Эсминец
		3: 25,  // M3 тяжёлый истребитель
		4: 15,  // M4 истребитель
		5: 10,  // M5 разведчик
		6: 50,  // M6 корвет
		7: 60,  // TL супертранспорт
		8: 60,  // XX специальный
		9: 15,  // TS транспорт
	}
	for shipType, target := range targets {
		cls := argonShip(t, classes, shipType)
		require.Positive(t, cls.MaxEnergy, "class %d must have a per-class pool", shipType)

		lo := loadouts.BaseLoadout(cls.Race, cls.Type)
		require.NotEmpty(t, lo, "class %d must have a base loadout", shipType)
		delta := equipment.EnergyDelta(lo)

		got := combat.SimulateFireTicks(cls.MaxEnergy, cls.EnergyRecharge, delta, laserEnergyCost)
		tol := 0.15 * float64(target)
		assert.InDeltaf(t, target, got, tol,
			"class %d (%s) fire duration %d ticks, want %d ±15%% (pool=%d recharge=%d delta=%d)",
			shipType, cls.Name, got, target, cls.MaxEnergy, cls.EnergyRecharge, delta)
	}
}

// TestUnit_EnergyModel_AccumulatorDoublesFireDuration checks AC #12: each
// up_accumulator level doubles the pool and thus (roughly) the fire duration —
// scout 10 → 20 → 40 → 80 ticks — folded through the real ApplyEquipmentEffects.
func TestUnit_EnergyModel_AccumulatorDoublesFireDuration(t *testing.T) {
	classes, equipment, loadouts := energyModel(t)
	scout := argonShip(t, classes, 5)
	lo := loadouts.BaseLoadout(scout.Race, scout.Type)
	delta := equipment.EnergyDelta(lo) // accumulator is `hold` → does not change delta

	base := balance.ShipStats{MaxEnergy: scout.MaxEnergy, EnergyRecharge: scout.EnergyRecharge}
	prev := combat.SimulateFireTicks(scout.MaxEnergy, scout.EnergyRecharge, delta, laserEnergyCost)

	// Level 1..3 (catalog max_level) each roughly double the previous duration.
	for level := 1; level <= 3; level++ {
		fit := append(append([]domain.InstalledEquipment{}, lo...),
			domain.InstalledEquipment{Type: "up_accumulator", Level: level})
		eff := balance.ApplyEquipmentEffects(base, fit)
		require.Equal(t, scout.MaxEnergy<<level, eff.MaxEnergy, "level %d must ×2^level the pool", level)

		got := combat.SimulateFireTicks(eff.MaxEnergy, scout.EnergyRecharge, delta, laserEnergyCost)
		assert.InDeltaf(t, 2*prev, got, 0.20*float64(2*prev),
			"accumulator L%d fire duration %d, want ~%d (double of %d)", level, got, 2*prev, prev)
		prev = got
	}
}

// TestUnit_EnergyModel_IdleRecoversNo0Lock checks AC #11: with the laser silent,
// every base kit recharges from empty (recharge out-paces always-drain) in a
// sane window — the pool never sticks at 0.
func TestUnit_EnergyModel_IdleRecoversNo0Lock(t *testing.T) {
	classes, equipment, loadouts := energyModel(t)
	for shipType := 1; shipType <= 9; shipType++ {
		cls := argonShip(t, classes, shipType)
		lo := loadouts.BaseLoadout(cls.Race, cls.Type)
		delta := equipment.EnergyDelta(lo)

		ticks, ok := combat.SimulateIdleRecoverTicks(cls.MaxEnergy, cls.EnergyRecharge, delta)
		require.Truef(t, ok, "class %d must not 0-lock (recharge=%d delta=%d)", shipType, cls.EnergyRecharge, delta)
		assert.Positive(t, ticks)
		assert.LessOrEqualf(t, ticks, 60, "class %d idle recovery %d ticks is too slow", shipType, ticks)
	}
}
