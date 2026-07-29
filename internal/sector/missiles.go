package sector

import (
	"context"
	"sort"
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// sortMissiles sorts a slice of value-type Missile by ID in place so
// every Snapshot the worker hands out is deterministic for diff
// computation and tests.
func sortMissiles(ms []domain.Missile) {
	sort.Slice(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
}

// missileSpec is the spec the sector tick passes into combat.TickMissile.
// Phase 4.3 has exactly one class of missile (see spec §1), so the spec
// is a package-level constant. When the balance catalog arrives this will
// move into per-missile state.
var missileSpec = combat.DefaultMissileSpec()

// MissileImpact is a per-tick missile event broadcast in the same
// Snapshot as the missile's removal: it tells the SPA whether the
// missile detonated on a target or just expired. Pos is the missile's
// last position; Damage and Killed are zero when Expired=true.
type MissileImpact struct {
	MissileID      domain.MissileID
	AttackerShipID domain.ShipID
	Target         domain.EntityRef
	Pos            domain.Vec2
	Damage         int
	Killed         bool
	Expired        bool
}

// tickMissiles integrates every missile in the sector for dt seconds.
// For each missile it resolves the live target (same-sector ship or
// destructible static, alive), invokes combat.TickMissile, and on a
// hit/expire records a MissileImpact + removes the missile.
//
// A missile homes to either a ship (s.ships) or a destructible static
// (s.destructibles) per TASK-113 FR-08; the resolve is shared with
// torpedoes via resolveTargetPos. On a hit the point damage routes
// through the same domain.Damageable path the laser/torpedo use
// (combat.ApplyDamage / TakeDamage) — a ship is left for sweepKilledShips
// to reap while a static dropped to HP<=0 is reaped inline via killStatic
// (there is no static sweep). Unlike a torpedo a missile deals POINT
// damage only — no splash (ЧТЗ C-02). A missile whose target left the
// sector / died falls back to its LastTargetPos and can only expire via TTL.
func (w *Worker) tickMissiles(ctx context.Context, s *sectorState, dt float64, now time.Time) {
	for id, m := range s.missiles {
		targetPos, targetAlive := s.resolveTargetPos(m.Target)

		outcome := combat.TickMissile(m, targetPos, targetAlive, missileSpec, dt, now)
		switch outcome {
		case combat.MissileKeep:
			continue
		case combat.MissileExpired:
			s.addMissileImpact(MissileImpact{
				MissileID:      id,
				AttackerShipID: m.OwnerShipID,
				Target:         m.Target,
				Pos:            m.Pos,
				Expired:        true,
			})
			delete(s.missiles, id)
		case combat.MissileHit:
			// targetAlive==true is the precondition for MissileHit, so the
			// resolved target (ship or static) is present and alive here.
			w.applyMissileHit(ctx, s, id, m)
			delete(s.missiles, id)
		}
	}
}

// applyMissileHit deals one missile's point damage to its (live) target and
// records the impact. A ship target is damaged and left for sweepKilledShips;
// a destructible static is damaged through the same Damageable path the laser
// uses and, when the blow kills it, reaped inline via killStatic (statics have
// no sweep) — TASK-113 FR-08. Point damage only, no splash (ЧТЗ C-02).
func (w *Worker) applyMissileHit(ctx context.Context, s *sectorState, id domain.MissileID, m *domain.Missile) {
	imp := MissileImpact{
		MissileID:      id,
		AttackerShipID: m.OwnerShipID,
		Target:         m.Target,
		Pos:            m.Pos,
	}
	if m.Target.Kind == domain.EntityKindShip {
		ship := s.ships[domain.ShipID(m.Target.ID)]
		res := combat.ApplyDamage(ship, m.Damage)
		// Attribute the kill for bounty payout (6.3) to the missile owner.
		ship.LastAttacker = m.PlayerID
		imp.Damage = res.ShieldAbsorbed + res.HPAbsorbed
		imp.Killed = res.Killed
		s.addMissileImpact(imp)
		s.markDirty(ship.ID)
		if !res.Killed {
			// TASK-100.3.9.1: knockoff on a surviving shield-down target.
			w.knockModules(ctx, s, ship)
		}
		return
	}
	if m.Target.Kind == domain.EntityKindContainer {
		// A loot container is soft and carries no shield: the crate either survives
		// the hit or is destroyed with its cargo (TASK-111). Denying an enemy their
		// loot is the point, so nothing is salvaged from the wreck.
		w.applyMissileHitContainer(ctx, s, imp, domain.ContainerID(m.Target.ID), m.Damage)
		return
	}
	// Destructible static: route point damage through Damageable (mirror of
	// fireLaserAtStatic). Static kills carry no player attribution.
	d := s.destructibles[m.Target]
	res := combat.ApplyDamage(d, m.Damage)
	imp.Damage = res.ShieldAbsorbed + res.HPAbsorbed
	imp.Killed = res.Killed
	s.addMissileImpact(imp)
	if res.Killed {
		w.killStatic(ctx, s, d)
	} else {
		s.markDestructibleDirty(m.Target)
	}
}

// applyMissileHitContainer damages a loot container and, on a kill, removes it
// from the sector and deletes its row — the same reap the TTL sweep runs, which is
// also what disposes of the cargo (ON DELETE CASCADE on the container's cargo
// rows). Nothing drops: the goods are destroyed, not spilled, which is what makes
// shooting a crate a denial move rather than a slower pickup.
//
// The container's HP is RAM-only, so a survivor's damage is simply not persisted:
// a restart heals it. That is the same treatment every other destructible's HP
// gets (TASK-67), and a container's whole lifetime is a TTL anyway.
func (w *Worker) applyMissileHitContainer(ctx context.Context, s *sectorState, imp MissileImpact, id domain.ContainerID, dmg int) {
	c, ok := s.containers[id]
	if !ok {
		// Picked up or expired between the homing check and here: the missile is
		// spent, nothing to damage.
		imp.Expired = true
		s.addMissileImpact(imp)
		return
	}
	res := c.TakeDamage(dmg)
	imp.Damage = res.HPAbsorbed
	imp.Killed = res.Killed
	s.addMissileImpact(imp)
	if !res.Killed {
		return
	}
	s.removeContainer(id)
	if w.containerRepo == nil {
		return
	}
	err := w.dbCall(ctx, func(ctx context.Context) error {
		return w.containerRepo.Delete(ctx, id)
	})
	if err != nil {
		// Same best-effort contract as the TTL sweep: the crate is gone from the
		// sector either way, and the row is swept on the next restart's TTL pass.
		w.logger.ErrorContext(ctx, "container destroyed but row delete failed",
			"err", err, "container", int64(id), "sector", int64(s.sectorID))
	}
}
