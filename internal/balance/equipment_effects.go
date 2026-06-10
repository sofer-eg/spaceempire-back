package balance

import (
	"fmt"
	"math"

	"spaceempire/back/internal/domain"
)

// ShipStats is the subset of ship characteristics equipment can modify
// (phase 10.14). The app builds the base from the ship's class + spawn config;
// ApplyEquipmentEffects folds the installed modules on top. Keeping the math
// here (pure, no DB) makes the effect model unit-testable. See
// back/docs/specs/equipment_effects.md.
type ShipStats struct {
	MaxSpeed       float64
	Acceleration   float64
	MaxShield      int
	ShieldRecharge int
	MaxEnergy      int
	EnergyRecharge int
	LaserDamage    int
	// RadarRange is the personal radar radius (phase 10.20). Base comes from the
	// ship class; up_scanner widens it via ApplyEquipmentEffects (L3).
	RadarRange float64
}

// ApplyEquipmentEffects returns base with every stat module's additive boost
// folded in. Each boost is a fraction of the corresponding base field per
// install level (see the spec table). Capability modules (up_drill, up_scanner,
// up_jump_drive, up_hide, up_launcher, up_drone_control, …) are absent from the
// switch and leave the stats unchanged — their effect is unlocking a subsystem,
// represented by their mere presence in the equipment list.
//
// Each module reads base.X (not the accumulating out.X) so two modules that
// target the same field (e.g. up_shield + up_pro on MaxShield) stack additively
// off the baseline rather than compounding.
func ApplyEquipmentEffects(base ShipStats, eq []domain.InstalledEquipment) ShipStats {
	out := base
	for _, m := range eq {
		l := float64(m.Level)
		if l < 1 {
			l = 1
		}
		switch m.Type {
		case "up_engine":
			out.MaxSpeed += base.MaxSpeed * 0.08 * l
			out.Acceleration += base.Acceleration * 0.08 * l
		case "up_shield":
			out.MaxShield += int(math.Round(float64(base.MaxShield) * 0.15 * l))
			out.ShieldRecharge += int(math.Round(float64(base.ShieldRecharge) * 0.10 * l))
		case "up_pro":
			out.MaxShield += int(math.Round(float64(base.MaxShield) * 0.10 * l))
		case "up_generator":
			out.EnergyRecharge += int(math.Round(float64(base.EnergyRecharge) * 0.25 * l))
		case "up_accumulator":
			out.MaxEnergy += int(math.Round(float64(base.MaxEnergy) * 0.25 * l))
		case "up_lb":
			out.LaserDamage += int(math.Round(float64(base.LaserDamage) * 0.10 * l))
		case "up_weapon_control", "up_turret_control":
			out.LaserDamage += int(math.Round(float64(base.LaserDamage) * 0.08 * l))
		case "up_scanner":
			// Phase 10.20 L3: the scanner widens the personal radar +40 %/level.
			out.RadarRange += base.RadarRange * 0.4 * l
		}
	}
	return out
}

// InstallPrice is the credit cost of fitting an equipment row at the given
// level: price + level*price_per_level (matches the task spec / original
// pricing). level is clamped to >=1.
func InstallPrice(e Equipment, level int) int64 {
	if level < 1 {
		level = 1
	}
	return e.Price + int64(level)*e.PricePerLevel
}

// Reputation is the player's standing on the three rating axes a module may
// gate on (phase 10.3.4). The app maps players.Reputation onto it so the pure
// catalog layer stays free of the persistence import.
type Reputation struct {
	War   int
	Trade int
	Race  int
}

// ResolveInstall validates a fit request against the catalog and the ship's
// already-installed modules, returning the catalog row to install. It checks
// (see spec): ship class, race, rank thresholds, level bounds, one-per-type
// slot, and the energy-chain dependency. The rank gate compares the player's
// reputation against the row's min_war_rate / min_trade_rate / min_race_rate.
func (c *Equipments) ResolveInstall(id domain.EquipmentID, shipClassNum, shipRace, level int, installed []domain.InstalledEquipment, rep Reputation) (Equipment, error) {
	e, ok := c.GetEquipment(id)
	if !ok {
		return Equipment{}, fmt.Errorf("%w: %d", ErrEquipmentNotFound, id)
	}
	if e.ShipClass != 0 && e.ShipClass != shipClassNum {
		return Equipment{}, fmt.Errorf("%w: row class %d, ship class %d", ErrEquipmentWrongClass, e.ShipClass, shipClassNum)
	}
	if e.Race != 0 && e.Race != shipRace {
		return Equipment{}, fmt.Errorf("%w: row race %d, ship race %d", ErrEquipmentWrongRace, e.Race, shipRace)
	}
	if rep.War < e.MinWarRate || rep.Trade < e.MinTradeRate || rep.Race < e.MinRaceRate {
		return Equipment{}, fmt.Errorf("%w: need war>=%d trade>=%d race>=%d, have %d/%d/%d",
			ErrRankTooLow, e.MinWarRate, e.MinTradeRate, e.MinRaceRate, rep.War, rep.Trade, rep.Race)
	}
	maxLvl := e.MaxLevel
	if maxLvl < 1 {
		maxLvl = 1
	}
	if level < 1 || level > maxLvl {
		return Equipment{}, fmt.Errorf("%w: %d (max %d)", ErrEquipmentLevel, level, maxLvl)
	}
	if hasEquipmentType(installed, e.Type) {
		return Equipment{}, fmt.Errorf("%w: %s", ErrEquipmentAlreadyInstalled, e.Type)
	}
	if dep := e.Dependance; dep != "" && dep != "none" && !hasEquipmentType(installed, dep) {
		return Equipment{}, fmt.Errorf("%w: %s needs %s", ErrEquipmentDependency, e.Type, dep)
	}
	return e, nil
}

// hasEquipmentType reports whether a module of the given type is installed.
func hasEquipmentType(installed []domain.InstalledEquipment, typ string) bool {
	for _, m := range installed {
		if m.Type == typ {
			return true
		}
	}
	return false
}
