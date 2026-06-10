package sector

import (
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// towerSpec is the single laser-tower class of phase 4.5 (see
// lasertowers.md §2). Package-level value until a balance catalog arrives,
// mirroring droneSpec.
var towerSpec = combat.DefaultTowerSpec()

// tickTowers runs the laser-tower combat step: each tower in the sector's
// read-only statics acquires the nearest hostile ship in range (per the
// worker's hostility predicate) and applies damage to it. Towers are not
// mutated — only the target ship is — so they stay in SectorStatics rather
// than a mutable per-tick map.
//
// app wires a relations-backed predicate in 6.2a, so player-owned towers now
// fire at hostiles; NPC towers (no owner) stay passive until race-standing.
// On each shot the tower emits a LaserBeam into s.laserEffects — the same
// one-frame channel ship lasers use — so the SPA renders the tower beam with
// no client change.
func (w *Worker) tickTowers(s *sectorState) {
	for _, t := range s.statics.LaserTowers {
		// Player-owned towers use the relations predicate (6.2a); race-owned
		// towers (owner==nil) use the race predicate (8.3) — a per-tower
		// closure capturing the tower's race. A race tower with no race
		// predicate wired stays passive.
		pred := w.hostile
		if t.OwnerID == nil {
			if w.raceHostile == nil {
				continue
			}
			race := t.Race
			pred = func(_ *domain.PlayerID, ship *domain.Ship) bool {
				return w.raceHostile(race, ship)
			}
		}
		target := combat.SelectTowerTarget(t, s.ships, towerSpec, pred)
		if target == nil {
			continue
		}
		res := combat.ApplyDamage(target, towerSpec.Damage)
		// Attribute the kill for bounty payout (6.3) to the tower owner. NPC
		// towers (no owner) leave LastAttacker untouched.
		if t.OwnerID != nil {
			target.LastAttacker = *t.OwnerID
		}
		s.markDirty(target.ID)
		s.addLaserEffect(combat.LaserBeam{
			// AttackerShipID 0 marks a non-ship source (tower); the SPA draws
			// the beam from From→To regardless.
			Target:      domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target.ID)},
			From:        t.Pos,
			To:          target.Pos,
			DamageDealt: res.ShieldAbsorbed + res.HPAbsorbed,
			Killed:      res.Killed,
		})
	}
}
