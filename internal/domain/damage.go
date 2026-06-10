package domain

// DamageResult is the outcome of one damage application: how much was
// soaked by the shield, how much by HP, leftover overkill, and whether
// the target died. Lives in domain (not combat) so any Damageable
// implementer (ship, station, drone, …) can return it without the
// combat package importing domain.
type DamageResult struct {
	ShieldAbsorbed int
	HPAbsorbed     int
	Overkill       int
	Killed         bool
}

// TakeDamage applies dmg to the ship: shield first, then HP. Negative or
// zero dmg is a no-op and returns a zero DamageResult — combat callers
// rely on the early-return so they do not have to gate on dmg>0 either.
// A target already at HP=0 is not re-killed: Killed reports the
// transition this call caused (false when the ship was already dead).
func (s *Ship) TakeDamage(dmg int) DamageResult {
	if s == nil {
		return DamageResult{}
	}
	return applyDamage(&s.HP, &s.Shield, dmg)
}

// applyDamage soaks dmg into *shield first, then *hp (mutating both in
// place), and reports the outcome. Shared by every Damageable
// implementation (ship, destructible static) so the shield-then-HP rule
// lives in one place. Non-positive dmg is a no-op. Killed reports the
// alive→dead transition this call caused (false if *hp was already 0).
func applyDamage(hp, shield *int, dmg int) DamageResult {
	if dmg <= 0 {
		return DamageResult{}
	}
	wasAlive := *hp > 0
	var res DamageResult

	if *shield > 0 {
		if dmg <= *shield {
			*shield -= dmg
			res.ShieldAbsorbed = dmg
			return res
		}
		res.ShieldAbsorbed = *shield
		dmg -= *shield
		*shield = 0
	}

	if dmg <= *hp {
		*hp -= dmg
		res.HPAbsorbed = dmg
	} else {
		res.HPAbsorbed = *hp
		res.Overkill = dmg - *hp
		*hp = 0
	}
	res.Killed = wasAlive && *hp == 0
	return res
}
