package sector

import (
	"context"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// shipsAreFriendly reports whether a and b are allies per the relations
// oracle (same clan or a declared friend). A nil oracle never reports
// friendly, preserving open fire when relations are not wired.
func (w *Worker) shipsAreFriendly(a, b *domain.Ship) bool {
	if w.relations == nil {
		return false
	}
	return w.relations.Get(domain.PlayerRef(a.PlayerID), domain.PlayerRef(b.PlayerID)) == domain.RelationFriend
}

// chargeShields runs the per-tick shield-recharge step for every ship in
// the sector. Mirrors old SP `TO_ShipShieldCharge` (one row per ship,
// `shield += shield_charge`, clamped to max_shield). Ships whose shield
// value changed are marked dirty so the next periodic BatchUpdate carries
// the new value to Postgres.
//
// Docked ships are charged just like flying ones — the SP does not gate
// on `location`, and a parked player should still see their shield refill.
func chargeShields(s *sectorState) {
	for id, ship := range s.ships {
		if combat.ChargeShield(ship) {
			s.markDirty(id)
		}
	}
}

// chargeEnergies runs the per-tick energy-recharge step for every ship
// in the sector. Same dirty-marking contract as chargeShields.
func chargeEnergies(s *sectorState) {
	for id, ship := range s.ships {
		if combat.ChargeEnergy(ship) {
			s.markDirty(id)
		}
	}
}

// fireLasers runs the per-tick laser-fire step for every ship with an
// AttackTarget set. For each shooter:
//   - clears AttackTarget if the target has left the sector or is dead
//   - calls combat.FireLaser; on hit pushes the resulting LaserBeam
//     into s.laserEffects and marks both ships dirty (target took
//     damage, shooter spent energy)
//   - clears AttackTarget when the target died this shot
//
// Same-sector targets only — AttackTarget pointing at a ship that
// belongs to a different sector is dropped silently. Ship targets follow
// the 4.2 path; static targets (station/shipyard/trade-station/pirbase/
// tower) go through fireLaserAtStatic (phase 6.2b); any other kind (e.g. a
// gate) is treated as "no target" and the attack reference is cleared.
func (w *Worker) fireLasers(ctx context.Context, s *sectorState) {
	for id, attacker := range s.ships {
		if attacker.AttackTarget == nil {
			continue
		}
		ref := *attacker.AttackTarget
		if isStaticTargetKind(ref.Kind) {
			w.fireLaserAtStatic(ctx, s, id, attacker, ref)
			continue
		}
		if ref.Kind != domain.EntityKindShip {
			attacker.AttackTarget = nil
			s.markDirty(id)
			continue
		}
		target, ok := s.ships[domain.ShipID(attacker.AttackTarget.ID)]
		if !ok || target.HP <= 0 {
			attacker.AttackTarget = nil
			s.markDirty(id)
			continue
		}
		// Friendly-fire gate (6.2a): never shoot an ally. Drop the engagement
		// so the SPA stops painting it as the current attack target. nil
		// relations oracle → no gating (open fire, pre-6.2a behaviour).
		if w.shipsAreFriendly(attacker, target) {
			attacker.AttackTarget = nil
			s.markDirty(id)
			continue
		}
		beam, hit := combat.FireLaser(attacker, target)
		if !hit {
			continue
		}
		// Attribute the kill for bounty payout (6.3): the shooter is the
		// target's last attacker.
		target.LastAttacker = attacker.PlayerID
		s.addLaserEffect(beam)
		s.markDirty(id)
		s.markDirty(target.ID)
		if beam.Killed {
			attacker.AttackTarget = nil
			// Already dirty above; nothing else to do here. The 4.6
			// kill handler will remove the corpse from the sector.
		}
	}
}
