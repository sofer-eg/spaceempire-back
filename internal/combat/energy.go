package combat

import "spaceempire/back/internal/domain"

// ChargeEnergy moves ship.Energy by its net per-tick rate, clamped to
// [0, MaxEnergy]. The net rate is the powerplant recharge (EnergyRecharge,
// boosted by up_generator) plus the installed-equipment delta (EnergyDelta:
// reverse generators add, always consumers drain — phase 10.3.1). Mirrors
// ChargeShield's contract.
//
// Returns true when Energy changed so the sector worker marks the ship dirty
// for the next snapshot. MaxEnergy==0 → ship has no powerplant, returns false.
// A negative net rate drains the pool toward 0 (an always-module ship with no
// generator); the clamp keeps it non-negative, and capability gates treat
// Energy==0 as "module unpowered" (e.g. stealth surfaces).
func ChargeEnergy(ship *domain.Ship) bool {
	if ship == nil || ship.MaxEnergy <= 0 {
		return false
	}
	before := ship.Energy
	ship.Energy += ship.EnergyRecharge + ship.EnergyDelta
	if ship.Energy > ship.MaxEnergy {
		ship.Energy = ship.MaxEnergy
	}
	if ship.Energy < 0 {
		ship.Energy = 0
	}
	return ship.Energy != before
}
