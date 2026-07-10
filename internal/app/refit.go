package app

import (
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// equipmentRefitter recomputes a ship's folded stat fields from its Equipment
// list, over the ship-class base stats and the equipment catalog. It implements
// sector.Refitter (TASK-100.3.9.1): the sector worker calls Refit after a module
// is knocked off in combat so the ship loses that module's stat boost — the same
// fold shipyard install/uninstall runs, but applied to the live worker copy.
type equipmentRefitter struct {
	classes   *balance.ShipClasses
	equipment *balance.Equipments
	cfg       ShipSpawnerConfig
}

// Refit mutates ship in place with the stats recomputed from ship.Equipment. A
// ship with an unknown class (spacesuit / legacy, no base stats) is left
// untouched. Shield/Energy are clamped down to the possibly-lowered maxima, as
// UpdateShipEquipmentCommand does.
func (r equipmentRefitter) Refit(ship *domain.Ship) {
	cls, ok := r.classes.GetShipClass(ship.ShipClassID)
	if !ok {
		return
	}
	eff := balance.ApplyEquipmentEffects(baseShipStats(cls, r.cfg), ship.Equipment)
	ship.MaxSpeed = eff.MaxSpeed
	ship.Acceleration = eff.Acceleration
	ship.MaxShield = eff.MaxShield
	ship.ShieldRecharge = eff.ShieldRecharge
	ship.MaxEnergy = eff.MaxEnergy
	ship.EnergyRecharge = eff.EnergyRecharge
	ship.EnergyDelta = r.equipment.EnergyDelta(ship.Equipment)
	ship.LaserDamage = eff.LaserDamage
	ship.RadarRange = eff.RadarRange
	ship.TurnRate = eff.TurnRate
	ship.CargoBay = eff.CargoBay
	if ship.Shield > ship.MaxShield {
		ship.Shield = ship.MaxShield
	}
	if ship.Energy > ship.MaxEnergy {
		ship.Energy = ship.MaxEnergy
	}
}
