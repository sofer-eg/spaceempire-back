package sector

import (
	"context"
	"time"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// TorpedoImpact is a per-tick torpedo event accumulated for the current tick.
// A detonation sets Hit=true and carries the splash centre (Pos) and
// SplashRadius so the area-damage pass (TASK-100.3.5.5) and the renderer
// (TASK-100.3.5.7) know where the blast lands; a TTL expiry or owner-loss sets
// Expired=true and deals no damage. Modelled on DroneImpact / MissileImpact and,
// like them before the DTO layer, kept internal this sub-task — the Snapshot/AOI
// surfacing is TASK-100.3.5.7.
type TorpedoImpact struct {
	TorpedoID    domain.TorpedoID
	OwnerShipID  domain.ShipID
	Target       domain.EntityRef
	Pos          domain.Vec2
	SplashRadius float64
	Hit          bool
	Expired      bool
}

// tickTorpedos advances every torpedo in the sector for dt seconds. For each
// torpedo it resolves the owner and the (ship or destructible-static) target,
// homes via combat.TickTorpedo, and ends the torpedo's life on:
//   - owner loss (owner ship gone or dead) → removal + Expired impact;
//   - detonation (within HitRadius of an alive target) → indiscriminate splash
//     damage to everything inside SplashRadius (combat.ApplyDamageInRadius,
//     TASK-100.3.5.5) + removal + Hit impact carrying the splash centre/radius;
//   - TTL expiry → removal + Expired impact, no damage.
//
// Every removal writes the DELETE to the DB immediately (torpedoes are
// persistent, like drones). All mutations happen here — one writer per sector.
func (w *Worker) tickTorpedos(ctx context.Context, s *sectorState, dt float64, now time.Time) {
	for id, t := range s.torpedos {
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
