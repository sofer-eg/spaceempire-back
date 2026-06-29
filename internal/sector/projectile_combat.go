package sector

import (
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// isProjectileTargetKind is the targetable set for in-flight projectiles — the
// single, isolated extension point that makes a snared object shoot-downable
// (ЧТЗ doc-1 §3 FR-008, risk R-04). Today it holds only torpedoes: unlike a
// fire-and-forget missile, a torpedo is a persistent combat object with its own
// HP, so a weapon can lock onto and destroy it. Keeping the projectile-as-target
// logic here — rather than spreading a torpedo branch across every weapon —
// means a future shoot-downable projectile is wired by extending this predicate
// plus fireLaserAtProjectile alone. It mirrors isStaticTargetKind /
// fireLaserAtStatic (phase 6.2b), the precedent for adding a non-ship object to
// the set of things a weapon can hit.
func isProjectileTargetKind(k domain.EntityKind) bool {
	return k == domain.EntityKindTorpedo
}

// fireLaserAtProjectile runs one tick of laser fire from attacker at a
// shoot-downable projectile (the isProjectileTargetKind set). It is the
// projectile counterpart of fireLaserAtStatic: it resolves the live torpedo,
// routes laser damage into its TakeDamage (HP only — a torpedo has no shield),
// and marks it dirty for the persistence batch.
//
// It deliberately does NOT reap a torpedo it drops to HP<=0. Every torpedo
// end-of-life — shoot-down included — is handled in one place, tickTorpedos,
// which runs right after fireLasers and emits impact(killed) with no splash
// (ЧТЗ §5.3, FR-008) — exactly as sweepKilledShips reaps a laser-killed ship
// rather than the laser doing it. On a killing beam the engagement is dropped
// (the torpedo is about to be reaped); a vanished/already-dead target also
// drops the engagement so the SPA stops painting it.
func (s *sectorState) fireLaserAtProjectile(attackerID domain.ShipID, attacker *domain.Ship, ref domain.EntityRef) {
	t, ok := s.torpedos[domain.TorpedoID(ref.ID)]
	if !ok || t.HP <= 0 {
		attacker.AttackTarget = nil
		s.markDirty(attackerID)
		return
	}
	beam, hit := combat.FireLaserAt(attacker, ref, t.Pos, t.HP, t)
	if !hit {
		return
	}
	s.addLaserEffect(beam)
	s.markDirty(attackerID)
	s.markTorpedoDirty(t.ID)
	if beam.Killed {
		// HP hit 0 — drop the engagement now; tickTorpedos reaps the torpedo
		// this same tick (it runs after fireLasers) with impact(killed), no splash.
		attacker.AttackTarget = nil
	}
}
