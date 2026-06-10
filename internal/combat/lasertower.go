package combat

import "spaceempire/back/internal/domain"

// TowerSpec holds the per-shot knobs of a laser tower, ported from the
// SP `TO_LaserTower` procedure constants (the originals are not per-row
// data). See back/docs/specs/lasertowers.md §2.
type TowerSpec struct {
	// Range is the fire reach in world units: a tower only acquires
	// targets within this distance.
	Range float64
	// Damage is applied to the selected target every tick it stays in
	// range (shield first, then HP, via ApplyDamage).
	Damage int
}

// DefaultTowerSpec returns the single tower class of phase 4.5, calibrated
// to the tight seed scale (statics at ±100..180 units) and the ~100-HP
// starter ship — SP `ltw_laser=25000` one-shots the legacy 6-figure HP and
// does not translate, so Damage is recut to a strong-but-not-instant 20.
func DefaultTowerSpec() TowerSpec {
	return TowerSpec{
		Range:  150,
		Damage: 20,
	}
}

// HostilePredicate reports whether ship is a valid hostile target for a
// tower owned by towerOwner (nil for NPC/race towers). Phase 4.5 production
// wiring uses NoHostility until relations (6.2) define real standings; the
// fire path is exercised by tests with an owner-based predicate.
type HostilePredicate func(towerOwner *domain.PlayerID, ship *domain.Ship) bool

// NoHostility is the phase-4.5 production stub: a tower considers nobody
// hostile until relations (6.2) land. Towers still load, render, and tick;
// they simply never acquire a target.
func NoHostility(*domain.PlayerID, *domain.Ship) bool { return false }

// SelectTowerTarget returns the nearest hostile, alive ship within
// spec.Range of the tower, or nil when none qualifies. This collapses the
// SP's four-tier shot-distribution selection to nearest-hostile: the
// distribution only matters when many towers share targets (deferred until
// that density exists); for a single tower it picks the same closest valid
// target. hostile must be non-nil — the caller defaults it to NoHostility.
func SelectTowerTarget(
	t domain.LaserTower,
	ships map[domain.ShipID]*domain.Ship,
	spec TowerSpec,
	hostile HostilePredicate,
) *domain.Ship {
	var best *domain.Ship
	var bestDist float64
	for _, ship := range ships {
		if ship.HP <= 0 {
			continue
		}
		dist := ship.Pos.Sub(t.Pos).Length()
		if dist > spec.Range {
			continue
		}
		if !hostile(t.OwnerID, ship) {
			continue
		}
		if best == nil || dist < bestDist {
			best = ship
			bestDist = dist
		}
	}
	return best
}
