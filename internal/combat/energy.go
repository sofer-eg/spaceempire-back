package combat

import "spaceempire/back/internal/domain"

// ChargeEnergy bumps ship.Energy by EnergyRecharge units per tick,
// clamped to MaxEnergy. Mirrors ChargeShield's contract.
//
// Returns true when Energy changed so the sector worker marks the ship
// dirty for the next snapshot. MaxEnergy==0 → ship has no powerplant,
// returns false. Energy>MaxEnergy is clamped down, returns true.
func ChargeEnergy(ship *domain.Ship) bool {
	if ship == nil || ship.MaxEnergy <= 0 {
		return false
	}
	if ship.Energy >= ship.MaxEnergy {
		if ship.Energy > ship.MaxEnergy {
			ship.Energy = ship.MaxEnergy
			return true
		}
		return false
	}
	ship.Energy += ship.EnergyRecharge
	if ship.Energy > ship.MaxEnergy {
		ship.Energy = ship.MaxEnergy
	}
	return true
}
