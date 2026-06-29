package sector

import (
	"context"
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// TorpedoImpact is a per-tick torpedo event accumulated for the current tick.
// Exactly one of the three outcome flags is set:
//   - Hit=true: a detonation; carries the splash centre (Pos) and SplashRadius so
//     the area-damage pass (TASK-100.3.5.5) and the renderer (TASK-100.3.5.7) know
//     where the blast lands.
//   - Killed=true: the torpedo was shot down (its own HP reached 0 from enemy
//     weapons, TASK-100.3.5.6); it dies where it is with NO splash and NO
//     detonation — distinct from a Hit (no SplashRadius) and from an Expired.
//   - Expired=true: a TTL expiry or owner-loss; deals no damage.
//
// Modelled on DroneImpact / MissileImpact and, like them before the DTO layer,
// kept internal this sub-task — the Snapshot/AOI surfacing is TASK-100.3.5.7.
type TorpedoImpact struct {
	TorpedoID    domain.TorpedoID
	OwnerShipID  domain.ShipID
	Target       domain.EntityRef
	Pos          domain.Vec2
	SplashRadius float64
	Hit          bool
	Killed       bool
	Expired      bool
}

// tickTorpedos advances every torpedo in the sector for dt seconds. For each
// torpedo it resolves the owner and the (ship or destructible-static) target,
// homes via combat.TickTorpedo, and ends the torpedo's life on:
//   - shoot-down (its own HP reached 0, TASK-100.3.5.6) → removal + Killed impact,
//     no splash, no detonation — checked first so a torpedo killed in flight never
//     gets a last detonation off;
//   - owner loss (owner ship gone or dead) → removal + Expired impact;
//   - detonation (within HitRadius of an alive target) → indiscriminate splash
//     damage to everything inside SplashRadius (combat.ApplyDamageInRadius,
//     TASK-100.3.5.5) + removal + Hit impact carrying the splash centre/radius;
//   - TTL expiry → removal + Expired impact, no damage.
//
// fireLasers/tickTowers/tickDrones run before this step, so a torpedo whose HP a
// weapon drove to 0 this tick is reaped here exactly once, the same one-place
// reap sweepKilledShips gives a laser-killed ship.
//
// Every removal writes the DELETE to the DB immediately (torpedoes are
// persistent, like drones). All mutations happen here — one writer per sector.
func (w *Worker) tickTorpedos(ctx context.Context, s *sectorState, dt float64, now time.Time) {
	for id, t := range s.torpedos {
		if t.HP <= 0 {
			// Shot down (ЧТЗ §5.3 "сбита"): an enemy weapon (lasers/drones/towers
			// run earlier this tick) drove the torpedo's hull to 0 via TakeDamage.
			// It dies in place — no detonation, no splash (FR-008) — with a Killed
			// impact distinct from a Hit (no splash centre/radius) and an Expired.
			w.removeTorpedo(ctx, s, id, TorpedoImpact{
				TorpedoID:   id,
				OwnerShipID: t.OwnerShipID,
				Target:      t.Target,
				Pos:         t.Pos,
				Killed:      true,
			})
			continue
		}

		owner, ownerOK := s.ships[t.OwnerShipID]
		if !ownerOK || owner.HP <= 0 {
			// Owner gone — the torpedo dies with it (ЧТЗ FR-009).
			w.removeTorpedo(ctx, s, id, TorpedoImpact{
				TorpedoID:   id,
				OwnerShipID: t.OwnerShipID,
				Target:      t.Target,
				Pos:         t.Pos,
				Expired:     true,
			})
			continue
		}

		targetPos, targetAlive := s.torpedoTargetPos(t.Target)

		switch combat.TickTorpedo(t, targetPos, targetAlive, dt, now) {
		case combat.TorpedoKeep:
			s.markTorpedoDirty(id)
		case combat.TorpedoHit:
			// Detonate: deal indiscriminate area damage to every ship and
			// destructible static inside SplashRadius of the blast (TASK-100.3.5.5,
			// ЧТЗ FR-007) — friendly-fire included, the owner's own ships among
			// them. Damage only lowers HP; a target that drops to HP<=0 is reaped
			// by sweepKilledShips next (it runs right after this step), exactly as
			// a laser hit. Mark every hit target dirty so the WS combat delta and
			// the persistence batch carry the new HP.
			for _, ref := range combat.ApplyDamageInRadius(s.ships, s.destructibles, t.Pos, t.SplashRadius, t.Damage, t.PlayerID) {
				if ref.Kind == domain.EntityKindShip {
					s.markDirty(domain.ShipID(ref.ID))
					continue
				}
				// Splash is the second damage source to statics (lasers are the
				// first). A static the blast drops to HP<=0 is reaped inline here,
				// exactly as fireLaserAtStatic does on a killing beam — there is no
				// static sweep (sweepKilledShips only sweeps ships), so without this
				// the dead static lingers as a zombie: charging shields, staying a
				// dock/trade target, and (towers) still firing. killStatic emits
				// entity_killed, persist-deletes, and drops it from the live
				// set/layout (TASK-100.3.5.5).
				if d := s.destructibles[ref]; d.HP <= 0 {
					w.killStatic(ctx, s, d)
				} else {
					s.markDestructibleDirty(ref)
				}
			}
			// Emit the splash centre + radius for the render pass (TASK-100.3.5.7)
			// and remove the torpedo.
			w.removeTorpedo(ctx, s, id, TorpedoImpact{
				TorpedoID:    id,
				OwnerShipID:  t.OwnerShipID,
				Target:       t.Target,
				Pos:          t.Pos,
				SplashRadius: t.SplashRadius,
				Hit:          true,
			})
		case combat.TorpedoExpired:
			w.removeTorpedo(ctx, s, id, TorpedoImpact{
				TorpedoID:   id,
				OwnerShipID: t.OwnerShipID,
				Target:      t.Target,
				Pos:         t.Pos,
				Expired:     true,
			})
		}
	}
}

// torpedosInRadius returns the subset of live torpedoes whose Pos lies within
// radius of center (ЧТЗ doc-1 §3 FR-010, NFR-003). radius<=0 disables the filter.
// Output is a value-type map; Torpedo has no slice/map/pointer fields (the
// time.Time TTL is value-copied), so a plain copy is deep-safe and satisfies the
// worker→subscriber isolation contract. Mirrors dronesInRadius / missilesInRadius.
func torpedosInRadius(src map[domain.TorpedoID]*domain.Torpedo, center domain.Vec2, radius float64) map[domain.TorpedoID]domain.Torpedo {
	if len(src) == 0 {
		return nil
	}
	out := make(map[domain.TorpedoID]domain.Torpedo, len(src))
	if radius <= 0 {
		for id, t := range src {
			out[id] = *t
		}
		return out
	}
	r2 := radius * radius
	for id, t := range src {
		dx := t.Pos.X - center.X
		dy := t.Pos.Y - center.Y
		if dx*dx+dy*dy <= r2 {
			out[id] = *t
		}
	}
	return out
}

// diffTorpedos produces the per-tick torpedo delta vs the subscriber's
// previously-seen set. domain.Torpedo is a comparable value (ExpiresAt is its
// only non-scalar field, a value-type time.Time fixed at launch), so "changed"
// is a plain `!=` — exactly as diffMissiles compares Missile. Mirrors diffDrones.
func diffTorpedos(prev, curr map[domain.TorpedoID]domain.Torpedo) (added, updated []domain.Torpedo, removed []domain.TorpedoID) {
	for id, t := range curr {
		pv, existed := prev[id]
		switch {
		case !existed:
			added = append(added, t)
		case pv != t:
			updated = append(updated, t)
		}
	}
	for id := range prev {
		if _, still := curr[id]; !still {
			removed = append(removed, id)
		}
	}
	return added, updated, removed
}

// filterTorpedoImpactsForAOI keeps only impacts whose Pos is inside the
// subscriber's AOI window (ЧТЗ doc-1 §3 FR-010). radius<=0 disables the filter.
// Mirrors filterDroneImpactsForAOI / filterMissileImpactsForAOI.
func filterTorpedoImpactsForAOI(imps []TorpedoImpact, center domain.Vec2, radius float64) []TorpedoImpact {
	if len(imps) == 0 {
		return nil
	}
	if radius <= 0 {
		out := make([]TorpedoImpact, len(imps))
		copy(out, imps)
		return out
	}
	r2 := radius * radius
	var out []TorpedoImpact
	for _, imp := range imps {
		if pointInRadius2(imp.Pos, center, r2) {
			out = append(out, imp)
		}
	}
	return out
}

// removeTorpedo deletes a torpedo from RAM and the DB (immediate), records the
// supplied impact for the tick, and clears any dirty flag. Used by every
// end-of-life path (detonation / expire / owner-loss).
func (w *Worker) removeTorpedo(ctx context.Context, s *sectorState, id domain.TorpedoID, imp TorpedoImpact) {
	s.addTorpedoImpact(imp)
	delete(s.torpedos, id)
	delete(s.torpedosDirty, id)
	if w.torpedoRepo != nil {
		if err := w.torpedoRepo.Delete(ctx, id); err != nil {
			w.logger.ErrorContext(ctx, "torpedo delete failed",
				"err", err, "torpedo", int64(id), "sector", int64(s.sectorID))
		}
	}
}
