package combat

import "spaceempire/back/internal/domain"

// Damageable is anything weapons can hit. On phase 4.1 the only
// implementation is *domain.Ship; later phases add stations, drones, and
// laser towers — each owns its own TakeDamage so the combat package
// stays decoupled from concrete entity layouts.
//
// The receiver method must be a pointer method: TakeDamage mutates the
// target's HP/Shield in place.
type Damageable interface {
	TakeDamage(dmg int) domain.DamageResult
}

// ApplyDamage routes a damage event to its target and returns the
// outcome. Shield is consumed first, then HP — see Ship.TakeDamage.
// Non-positive dmg is a no-op (zero DamageResult).
//
// This thin wrapper exists so phase 4.2+ can hook metrics, WS event
// emission, and AI hate-list updates around a single call site instead
// of every weapon dispatcher.
func ApplyDamage(target Damageable, dmg int) domain.DamageResult {
	if target == nil || dmg <= 0 {
		return domain.DamageResult{}
	}
	return target.TakeDamage(dmg)
}
