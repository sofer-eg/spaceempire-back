package domain

// DestructibleStatic is the mutable combat state of one static object —
// station, shipyard, trade station, pirbase, or laser tower (phase 6.2b).
// Statics ship to the client once (immutable layout) while their HP/Shield
// live here, keyed by EntityRef, so the sector worker can damage, recharge,
// and destroy them uniformly without 5× per-kind branches. It implements
// combat.Damageable.
type DestructibleStatic struct {
	Ref EntityRef
	Pos Vec2
	// OwnerID is the player that owns the static (nil for NPC/pirate
	// objects). Drives the hostility gate: a static is attackable only when
	// its owner is hostile to the attacker (phase 6.2a oracle).
	OwnerID *PlayerID

	HP             int
	Shield         int
	MaxShield      int
	ShieldRecharge int
}

// TakeDamage soaks dmg into the static's shield then HP (combat.Damageable).
func (d *DestructibleStatic) TakeDamage(dmg int) DamageResult {
	if d == nil {
		return DamageResult{}
	}
	return applyDamage(&d.HP, &d.Shield, dmg)
}

// ChargeShield adds ShieldRecharge to Shield, clamped at MaxShield. Returns
// true when the value changed (so the worker can dirty-track it). No-op for a
// static with no shield module (MaxShield <= 0) or one already at full.
func (d *DestructibleStatic) ChargeShield() bool {
	if d.MaxShield <= 0 || d.Shield >= d.MaxShield {
		if d.Shield > d.MaxShield {
			d.Shield = d.MaxShield
			return true
		}
		return false
	}
	d.Shield += d.ShieldRecharge
	if d.Shield > d.MaxShield {
		d.Shield = d.MaxShield
	}
	return true
}

// DestructiblesFromStatics flattens every static object in s into its
// combat-state form, preserving owner/HP/shield. Called once at worker
// cold-start to seed the per-sector destructible map.
func DestructiblesFromStatics(s SectorStatics) []DestructibleStatic {
	out := make([]DestructibleStatic, 0,
		len(s.Stations)+len(s.Shipyards)+len(s.TradeStations)+len(s.Pirbases)+len(s.LaserTowers)+len(s.Satellites))
	for _, o := range s.Stations {
		out = append(out, DestructibleStatic{Ref: o.ObjectID(), Pos: o.Pos, OwnerID: o.OwnerID, HP: o.HP, Shield: o.Shield, MaxShield: o.MaxShield, ShieldRecharge: o.ShieldRecharge})
	}
	for _, o := range s.Shipyards {
		out = append(out, DestructibleStatic{Ref: o.ObjectID(), Pos: o.Pos, OwnerID: o.OwnerID, HP: o.HP, Shield: o.Shield, MaxShield: o.MaxShield, ShieldRecharge: o.ShieldRecharge})
	}
	for _, o := range s.TradeStations {
		out = append(out, DestructibleStatic{Ref: o.ObjectID(), Pos: o.Pos, OwnerID: o.OwnerID, HP: o.HP, Shield: o.Shield, MaxShield: o.MaxShield, ShieldRecharge: o.ShieldRecharge})
	}
	for _, o := range s.Pirbases {
		// Pirbases have no owner column — OwnerID stays nil (race-based
		// hostility for pirates is deferred to race-standing).
		out = append(out, DestructibleStatic{Ref: o.ObjectID(), Pos: o.Pos, HP: o.HP, Shield: o.Shield, MaxShield: o.MaxShield, ShieldRecharge: o.ShieldRecharge})
	}
	for _, o := range s.LaserTowers {
		ref := EntityRef{Kind: EntityKindLaserTower, ID: int64(o.ID)}
		out = append(out, DestructibleStatic{Ref: ref, Pos: o.Pos, OwnerID: o.OwnerID, HP: o.HP, Shield: o.Shield, MaxShield: o.MaxShield, ShieldRecharge: o.ShieldRecharge})
	}
	for _, o := range s.Satellites {
		out = append(out, DestructibleStatic{Ref: o.ObjectID(), Pos: o.Pos, OwnerID: o.OwnerID, HP: o.HP, Shield: o.Shield, MaxShield: o.MaxShield, ShieldRecharge: o.ShieldRecharge})
	}
	return out
}
