package combat

import "spaceempire/back/internal/domain"

// LaserBeam is the post-shot record handed to the sector worker so it
// can publish a one-frame visual effect and a damage log entry.
// Coordinates are taken at the time of the shot — caller does not
// extrapolate them across the rest of the tick.
type LaserBeam struct {
	AttackerShipID domain.ShipID
	Target         domain.EntityRef
	From           domain.Vec2
	To             domain.Vec2
	DamageDealt    int  // sum of ShieldAbsorbed + HPAbsorbed
	Killed         bool // target HP reached 0 this shot
}

// FireLaser runs one tick of laser fire from attacker against target.
// Returns the resulting beam and ok=true on hit; ok=false when the shot
// did not happen (out of range, out of energy, no laser module, dead
// target). On ok=false attacker.Energy and target.HP/Shield are
// unchanged.
//
// Caller (sector worker) guarantees attacker and target live in the
// same sector and target.Kind matches EntityKindShip. On a successful
// shot Energy is debited and the target's HP/Shield are mutated through
// ApplyDamage. If beam.Killed, the worker is responsible for clearing
// attacker.AttackTarget; FireLaser itself only reports the outcome.
func FireLaser(attacker, target *domain.Ship) (LaserBeam, bool) {
	if target == nil {
		return LaserBeam{}, false
	}
	ref := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target.ID)}
	return FireLaserAt(attacker, ref, target.Pos, target.HP, target)
}

// FireLaserAt is the kind-agnostic core of FireLaser: it fires at any
// Damageable target located at targetPos with current targetHP, identified
// by targetRef for the beam record. Phase 6.2b uses it for static targets
// (stations, towers, …) so the range/energy/damage rules stay in one place.
// Returns the beam and ok=true on hit; ok=false (no state change) when the
// shot cannot happen (no laser module, dead target, out of energy/range).
func FireLaserAt(attacker *domain.Ship, targetRef domain.EntityRef, targetPos domain.Vec2, targetHP int, target Damageable) (LaserBeam, bool) {
	if attacker == nil || target == nil {
		return LaserBeam{}, false
	}
	if attacker.LaserDamage <= 0 || targetHP <= 0 {
		return LaserBeam{}, false
	}
	if attacker.Energy < attacker.LaserEnergyCost {
		return LaserBeam{}, false
	}
	if targetPos.Sub(attacker.Pos).Length() > attacker.LaserRange {
		return LaserBeam{}, false
	}

	attacker.Energy -= attacker.LaserEnergyCost
	res := ApplyDamage(target, attacker.LaserDamage)
	return LaserBeam{
		AttackerShipID: attacker.ID,
		Target:         targetRef,
		From:           attacker.Pos,
		To:             targetPos,
		DamageDealt:    res.ShieldAbsorbed + res.HPAbsorbed,
		Killed:         res.Killed,
	}, true
}
