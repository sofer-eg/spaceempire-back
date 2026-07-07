package combat

// This file is the analytical companion to the runtime energy/laser model
// (ChargeEnergy + FireLaser, TASK-100.3.25). The sector worker recharges every
// ship (chargeEnergies) and then fires lasers (fireLasers) each tick, so a ship
// firing continuously nets `recharge + energyDelta − laserCost` per tick, while
// at rest it nets `recharge + energyDelta`. energyDelta is the installed
// equipment's steady per-tick draw (Σreverse − Σalways; negative for a base kit
// with no generator). These pure helpers replay exactly that arithmetic so the
// per-class energy calibration can be asserted without spinning a worker. See
// back/docs/specs/energy_model.md.

// SimulateFireTicks returns how many consecutive ticks a ship can fire its laser
// before the pool can no longer pay a shot, starting from a full pool. Each tick
// mirrors the worker order: recharge (clamped to pool), then fire if the pool
// covers laserCost. pool/laserCost ≤ 0 → 0 (no pool, or a free/absent laser).
func SimulateFireTicks(pool, recharge, energyDelta, laserCost int) int {
	if pool <= 0 || laserCost <= 0 {
		return 0
	}
	energy := pool
	net := recharge + energyDelta
	for ticks := 0; ; ticks++ {
		energy += net
		if energy > pool {
			energy = pool
		}
		if energy < laserCost {
			return ticks
		}
		energy -= laserCost
	}
}

// SimulateIdleRecoverTicks returns how many ticks a ship at empty needs to
// refill its pool with the laser silent (net = recharge + energyDelta per tick),
// and ok=false when the net rate is ≤ 0 — the "0-lock" case where the always-drain
// out-paces recharge and the pool can never recover. A base kit (no generator)
// must never 0-lock: recharge must beat the always-drain.
func SimulateIdleRecoverTicks(pool, recharge, energyDelta int) (int, bool) {
	net := recharge + energyDelta
	if pool <= 0 || net <= 0 {
		return 0, net > 0
	}
	energy := 0
	for ticks := 1; ; ticks++ {
		energy += net
		if energy >= pool {
			return ticks, true
		}
	}
}
