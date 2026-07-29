package domain

import "time"

// Container is a loot drop floating in space: the cargo of a destroyed
// ship, packed into a pickup-able object (port of SP object_type 8). Its
// cargo lives in the cargo table under owner_kind = EntityKindContainer.
// Persistent (immediate writes) but immutable once created — the cargo
// inside only changes via pickup, which deletes the whole container.
//
// A container is also shootable (TASK-111): denying an enemy their loot is a
// legitimate move, and one hit's worth of HP is all a floating crate gets. HP is
// RAM-only — it has no column, is seeded from Config.ContainerHP at load/spawn,
// and resets on restart. That matches how every other destructible carries its
// combat state (RAM, TASK-67) while only the destruction itself is persisted: for
// a container the destruction IS the row going away.
type Container struct {
	ID        ContainerID
	SectorID  SectorID
	Pos       Vec2
	ExpiresAt time.Time

	HP int
}

// TakeDamage soaks dmg into the container's hull; a container has no shield
// (combat.Damageable).
func (c *Container) TakeDamage(dmg int) DamageResult {
	if c == nil {
		return DamageResult{}
	}
	shield := 0
	return applyDamage(&c.HP, &shield, dmg)
}

// ContainerDrop is one planned container the kill handler hands to the
// persistence layer: a single cargo stack at a chosen position with a
// TTL. RecordKill turns each into a container row plus its cargo row.
type ContainerDrop struct {
	Pos       Vec2
	ExpiresAt time.Time
	GoodsType GoodsTypeID
	Quantity  int64
}
