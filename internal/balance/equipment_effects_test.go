package balance_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

func base() balance.ShipStats {
	return balance.ShipStats{
		MaxSpeed:       100,
		Acceleration:   10,
		MaxShield:      1000,
		ShieldRecharge: 100,
		MaxEnergy:      200,
		EnergyRecharge: 4,
		LaserDamage:    50,
		RadarRange:     3000,
	}
}

func TestUnit_ApplyEquipmentEffects_ScannerWidensRadar(t *testing.T) {
	// up_scanner widens the personal radar +40% per level (phase 10.20 L3).
	got := balance.ApplyEquipmentEffects(base(), []domain.InstalledEquipment{
		{Type: "up_scanner", Level: 2},
	})
	require.InDelta(t, 3000+3000*0.4*2, got.RadarRange, 0.001) // 5400
	require.Equal(t, base().MaxSpeed, got.MaxSpeed, "scanner touches only radar")
}

func TestUnit_ApplyEquipmentEffects_EngineAndShieldBoostStats(t *testing.T) {
	got := balance.ApplyEquipmentEffects(base(), []domain.InstalledEquipment{
		{Type: "up_engine", Level: 2},
		{Type: "up_shield", Level: 1},
	})
	// engine: +8%*2 of speed/accel; shield: +15% maxShield, +10% recharge.
	require.InDelta(t, 116, got.MaxSpeed, 0.001)
	require.InDelta(t, 11.6, got.Acceleration, 0.001)
	require.Equal(t, 1150, got.MaxShield)
	require.Equal(t, 110, got.ShieldRecharge)
}

func TestUnit_ApplyEquipmentEffects_ShieldAndProStackOffBaseline(t *testing.T) {
	got := balance.ApplyEquipmentEffects(base(), []domain.InstalledEquipment{
		{Type: "up_shield", Level: 1}, // +15% of 1000 = 150
		{Type: "up_pro", Level: 1},    // +10% of 1000 = 100
	})
	require.Equal(t, 1250, got.MaxShield) // 1000 + 150 + 100, both off base
}

func TestUnit_ApplyEquipmentEffects_EnergyAndLaser(t *testing.T) {
	got := balance.ApplyEquipmentEffects(base(), []domain.InstalledEquipment{
		{Type: "up_accumulator", Level: 1}, // +25% of 200 = 50
		{Type: "up_generator", Level: 1},   // +25% of 4 = 1 (rounded)
		{Type: "up_lb", Level: 1},          // +10% of 50 = 5
	})
	require.Equal(t, 250, got.MaxEnergy)
	require.Equal(t, 5, got.EnergyRecharge)
	require.Equal(t, 55, got.LaserDamage)
}

func TestUnit_ApplyEquipmentEffects_CapabilityModulesDoNotChangeStats(t *testing.T) {
	got := balance.ApplyEquipmentEffects(base(), []domain.InstalledEquipment{
		{Type: "up_drill", Level: 2},
		{Type: "up_jump_drive", Level: 1},
		{Type: "up_hide", Level: 1},
		{Type: "up_drone_control", Level: 5},
	})
	require.Equal(t, base(), got)
}

func TestUnit_InstallPrice_ScalesWithLevel(t *testing.T) {
	e := balance.Equipment{Price: 1000, PricePerLevel: 200}
	require.Equal(t, int64(1200), balance.InstallPrice(e, 1))
	require.Equal(t, int64(1600), balance.InstallPrice(e, 3))
}

func energyCatalog(t *testing.T) *balance.Equipments {
	t.Helper()
	c, err := balance.NewEquipments([]balance.Equipment{
		{ID: 1, Type: "up_generator", EnergyUseType: "reverse", EnergyUsage: 100},
		{ID: 2, Type: "up_pro", EnergyUseType: "always", EnergyUsage: 80},
		{ID: 3, Type: "up_hide", EnergyUseType: "always", EnergyUsage: 30},
		{ID: 4, Type: "up_accumulator", EnergyUseType: "hold", EnergyUsage: 100},
		{ID: 5, Type: "up_launcher", EnergyUseType: "action", EnergyUsage: 100},
	})
	require.NoError(t, err)
	return c
}

func TestUnit_EnergyDelta(t *testing.T) {
	c := energyCatalog(t)

	// reverse generator adds, always consumers drain; hold/action contribute 0.
	require.Equal(t, 100, c.EnergyDelta([]domain.InstalledEquipment{{EquipmentID: 1, Type: "up_generator"}}))
	require.Equal(t, -80, c.EnergyDelta([]domain.InstalledEquipment{{EquipmentID: 2, Type: "up_pro"}}))

	// generator (+100) − pro (80) − hide (30) = −10; accumulator (hold) and
	// launcher (action) do not enter the steady delta.
	got := c.EnergyDelta([]domain.InstalledEquipment{
		{EquipmentID: 1, Type: "up_generator"},
		{EquipmentID: 2, Type: "up_pro"},
		{EquipmentID: 3, Type: "up_hide"},
		{EquipmentID: 4, Type: "up_accumulator"},
		{EquipmentID: 5, Type: "up_launcher"},
	})
	require.Equal(t, -10, got)

	// unknown ids and an empty list are skipped (0).
	require.Equal(t, 0, c.EnergyDelta(nil))
	require.Equal(t, 0, c.EnergyDelta([]domain.InstalledEquipment{{EquipmentID: 999, Type: "up_pro"}}))
}

func equipCatalog(t *testing.T) *balance.Equipments {
	t.Helper()
	c, err := balance.NewEquipments([]balance.Equipment{
		{ID: 10, Type: "up_accumulator", MaxLevel: 1, Race: 0, ShipClass: 5, Price: 100, Dependance: "up_generator"},
		{ID: 11, Type: "up_generator", MaxLevel: 1, Race: 0, ShipClass: 5, Price: 100, Dependance: "none"},
		{ID: 12, Type: "up_shield", MaxLevel: 3, Race: 0, ShipClass: 5, Price: 100, Dependance: "up_accumulator"},
		{ID: 13, Type: "up_hide", MaxLevel: 1, Race: 6, ShipClass: 0, Price: 100, Dependance: "none"},
		{ID: 14, Type: "up_engine", MaxLevel: 1, Race: 0, ShipClass: 4, Price: 100, Dependance: "none"},
		{ID: 15, Type: "up_lb", MaxLevel: 1, Race: 0, ShipClass: 5, Price: 100, Dependance: "none", MinWarRate: 100},
	})
	require.NoError(t, err)
	return c
}

func TestUnit_ResolveInstall_OK(t *testing.T) {
	c := equipCatalog(t)
	installed := []domain.InstalledEquipment{
		{Type: "up_generator", Level: 1},
		{Type: "up_accumulator", Level: 1},
	}
	// up_shield (no rank threshold) installs at zero reputation, as before.
	e, err := c.ResolveInstall(12, 5, 1, 2, installed, balance.Reputation{}) // up_shield lvl2, class 5, race 1
	require.NoError(t, err)
	require.Equal(t, "up_shield", e.Type)
}

func TestUnit_ResolveInstall_RankGate(t *testing.T) {
	c := equipCatalog(t)
	// up_lb (id 15) requires war_rate >= 100.
	_, err := c.ResolveInstall(15, 5, 0, 1, nil, balance.Reputation{War: 50})
	require.ErrorIs(t, err, balance.ErrRankTooLow)

	// Meeting the threshold exactly installs.
	e, err := c.ResolveInstall(15, 5, 0, 1, nil, balance.Reputation{War: 100})
	require.NoError(t, err)
	require.Equal(t, "up_lb", e.Type)
}

func TestUnit_ResolveInstall_Errors(t *testing.T) {
	c := equipCatalog(t)
	cases := []struct {
		name      string
		id        domain.EquipmentID
		class     int
		race      int
		level     int
		installed []domain.InstalledEquipment
		rep       balance.Reputation
		wantErr   error
	}{
		{"not found", 999, 5, 1, 1, nil, balance.Reputation{}, balance.ErrEquipmentNotFound},
		{"wrong class", 14, 5, 1, 1, nil, balance.Reputation{}, balance.ErrEquipmentWrongClass}, // engine is class 4
		{"wrong race", 13, 0, 1, 1, nil, balance.Reputation{}, balance.ErrEquipmentWrongRace},   // hide is race 6
		{"rank too low", 15, 5, 0, 1, nil, balance.Reputation{War: 99}, balance.ErrRankTooLow},  // up_lb needs war>=100
		{"level too high", 12, 5, 1, 4, depInstalled(), balance.Reputation{}, balance.ErrEquipmentLevel},
		{"level zero", 11, 5, 1, 0, nil, balance.Reputation{}, balance.ErrEquipmentLevel},
		{"missing dependency", 12, 5, 1, 1, nil, balance.Reputation{}, balance.ErrEquipmentDependency}, // shield needs accumulator
		{"already installed", 11, 5, 1, 1, []domain.InstalledEquipment{{Type: "up_generator", Level: 1}}, balance.Reputation{}, balance.ErrEquipmentAlreadyInstalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.ResolveInstall(tc.id, tc.class, tc.race, tc.level, tc.installed, tc.rep)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func depInstalled() []domain.InstalledEquipment {
	return []domain.InstalledEquipment{
		{Type: "up_generator", Level: 1},
		{Type: "up_accumulator", Level: 1},
	}
}
