package sector

import (
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
// For each missile it resolves the live target (same-sector ship,
// alive), invokes combat.TickMissile, and on a hit/expire records a
// MissileImpact + removes the missile.
//
// Damage routing is intentionally narrow: phase 4.3 only supports
// EntityKindShip targets (see spec §4 and §11 — non-ship damage routing
// arrives in 4.6 with the kill handler). A missile chasing a non-ship
// kind, or a ship that left the sector / died, falls back to its
// LastTargetPos and can only expire via TTL.
func tickMissiles(s *sectorState, dt float64, now time.Time) {
	for id, m := range s.missiles {
		var (
			targetPos   domain.Vec2
			targetAlive bool
			targetShip  *domain.Ship
		)
		if m.Target.Kind == domain.EntityKindShip {
			if ship, ok := s.ships[domain.ShipID(m.Target.ID)]; ok && ship.HP > 0 {
				targetShip = ship
				targetPos = ship.Pos
				targetAlive = true
			}
		}

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
			// Apply damage to the (live) ship target. targetAlive==true
			// is the precondition for MissileHit, so targetShip is non-nil
			// at this point.
			res := combat.ApplyDamage(targetShip, m.Damage)
			// Attribute the kill for bounty payout (6.3) to the missile owner.
			targetShip.LastAttacker = m.PlayerID
			s.addMissileImpact(MissileImpact{
				MissileID:      id,
				AttackerShipID: m.OwnerShipID,
				Target:         m.Target,
				Pos:            m.Pos,
				Damage:         res.ShieldAbsorbed + res.HPAbsorbed,
				Killed:         res.Killed,
			})
			s.markDirty(targetShip.ID)
			delete(s.missiles, id)
		}
	}
}
