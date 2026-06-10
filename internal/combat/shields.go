// Package combat hosts the per-tick combat domain: shield recharge,
// damage application, and (in later phases) lasers / missiles / drones /
// laser-towers / kill handlers. All functions are pure transforms over
// domain entities; they never touch persistence directly — sector
// workers call them inside their tick goroutine and the worker's
// dirty-set + immediate-event writes carry results to Postgres.
package combat

import "spaceempire/back/internal/domain"

// ChargeShield bumps ship.Shield by ship.ShieldRecharge units, clamped to
// ship.MaxShield. Mirrors the per-tick `set ship_shield = ship_shield +
// ship_shield_charge` step from old SP `TO_ShipShieldCharge`
// (starwind/sql/db.sql:33961).
//
// Returns true when ship.Shield changed — sector workers use it to
// mark the ship dirty for the next periodic snapshot. A ship with
// MaxShield==0 has no shield module: Shield stays 0, no charge applied,
// return false (matches the old `up_shield=0` branch).
//
// A ship arriving with Shield>MaxShield (e.g. after a class downgrade,
// not in scope yet but cheap to handle) is clamped down on this call,
// returning true.
func ChargeShield(ship *domain.Ship) bool {
	if ship == nil || ship.MaxShield <= 0 {
		return false
	}
	if ship.Shield >= ship.MaxShield {
		if ship.Shield > ship.MaxShield {
			ship.Shield = ship.MaxShield
			return true
		}
		return false
	}
	ship.Shield += ship.ShieldRecharge
	if ship.Shield > ship.MaxShield {
		ship.Shield = ship.MaxShield
	}
	return true
}
