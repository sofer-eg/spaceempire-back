package sector

import (
	"math"

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
// plus fireLaserAtProjectile alone. It mirrors IsStaticTargetKind /
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

// Point defence (TASK-112, ЧТЗ doc-1 §3 FR-008 / risk R-04). Making a torpedo
// shoot-downable in TASK-100.3.5.6 built the mechanism; nothing ever aimed at one,
// because drones and towers only ever scanned s.ships. These helpers are the
// candidate-set half of that gap, kept here next to isProjectileTargetKind so the
// projectile-as-target logic stays in one file.
//
// Hostility rule, decided for this task and applied here rather than in
// fireLaserAtProjectile:
//
//   - AUTOMATIC fire is hostility-gated. Point defence that shot down its own
//     side's torpedoes would be worse than no point defence, and a drone cannot
//     ask its owner what it meant.
//   - ORDERED fire (a player pointing a laser at a torpedo through AttackCommand)
//     stays unselective, consistent with splash friendly-fire (R-02) — and it is
//     the only way to abort your own torpedo, which is a legitimate move.
//
// nearestHostileTorpedo returns the closest live torpedo within radius of from
// that hostile reports as an enemy, or nil.
func nearestHostileTorpedo(
	s *sectorState,
	from domain.Vec2,
	radius float64,
	hostile func(t *domain.Torpedo) bool,
) *domain.Torpedo {
	if hostile == nil {
		return nil
	}
	r2 := radius * radius
	var best *domain.Torpedo
	bestSq := math.MaxFloat64
	for _, t := range s.torpedos {
		if t.HP <= 0 || !hostile(t) {
			continue
		}
		diff := t.Pos.Sub(from)
		sq := diff.X*diff.X + diff.Y*diff.Y
		if sq > r2 || sq >= bestSq {
			continue
		}
		best, bestSq = t, sq
	}
	return best
}

// acquireDroneTorpedo is the drone half of point defence: with no live ship
// target, a drone engages the nearest incoming hostile torpedo inside the same
// detection radius it uses for ships. A nil relations oracle (tests without 6.2a
// wiring) means no auto-acquire at all, exactly as for ships.
func (w *Worker) acquireDroneTorpedo(s *sectorState, d *domain.Drone) *domain.Torpedo {
	if w.relations == nil {
		return nil
	}
	return nearestHostileTorpedo(s, d.Pos, droneSpec.FireRange*droneAcquireFactor,
		func(t *domain.Torpedo) bool {
			// A player is never hostile to themselves, so the oracle already
			// excludes the drone's own torpedoes.
			return w.relations.IsHostile(domain.PlayerRef(d.PlayerID), domain.PlayerRef(t.PlayerID))
		})
}

// acquireTowerTorpedo is the tower half: a laser tower with no hostile ship in
// range shoots at an incoming hostile torpedo instead.
//
// Both hostility predicates take the attacking SHIP, while a torpedo carries only
// its owner, so the launching ship (Torpedo.OwnerShipID) stands in for it. A
// torpedo whose launcher has already left the sector or died is left alone: the
// tower has nothing to judge, and guessing "any ship of that player" would let a
// tower open fire on the strength of an unrelated hull.
func (w *Worker) acquireTowerTorpedo(s *sectorState, t domain.LaserTower) *domain.Torpedo {
	var judge func(launcher *domain.Ship) bool
	switch {
	case t.OwnerID != nil:
		if w.hostile == nil {
			return nil
		}
		owner := *t.OwnerID
		judge = func(launcher *domain.Ship) bool { return w.hostile(&owner, launcher) }
	case w.raceHostile != nil:
		race := t.Race
		judge = func(launcher *domain.Ship) bool { return w.raceHostile(race, launcher) }
	default:
		// A race tower with no race predicate wired stays passive, exactly as in
		// tickTowers.
		return nil
	}
	return nearestHostileTorpedo(s, t.Pos, towerSpec.Range, func(tp *domain.Torpedo) bool {
		launcher, ok := s.ships[tp.OwnerShipID]
		if !ok {
			return false
		}
		return judge(launcher)
	})
}

// fireTowerAtTorpedo applies one tower shot to a torpedo: HP-only damage (a
// torpedo has no shield) plus the beam effect the SPA draws. Like
// fireLaserAtProjectile it does NOT reap a killed torpedo — tickTorpedos owns every
// torpedo end-of-life and emits impact(killed) without splash (ЧТЗ §5.3).
func (w *Worker) fireTowerAtTorpedo(s *sectorState, t domain.LaserTower, tp *domain.Torpedo) {
	res := tp.TakeDamage(towerSpec.Damage)
	s.markTorpedoDirty(tp.ID)
	s.addLaserEffect(combat.LaserBeam{
		// AttackerShipID 0 marks a non-ship source (tower), as for ship targets.
		Target:      domain.EntityRef{Kind: domain.EntityKindTorpedo, ID: int64(tp.ID)},
		From:        t.Pos,
		To:          tp.Pos,
		DamageDealt: res.HPAbsorbed,
		Killed:      res.Killed,
	})
}
