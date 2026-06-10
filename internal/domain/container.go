package domain

import "time"

// Container is a loot drop floating in space: the cargo of a destroyed
// ship, packed into a pickup-able object (port of SP object_type 8). Its
// cargo lives in the cargo table under owner_kind = EntityKindContainer.
// Persistent (immediate writes) but immutable once created — the cargo
// inside only changes via pickup, which deletes the whole container.
type Container struct {
	ID        ContainerID
	SectorID  SectorID
	Pos       Vec2
	ExpiresAt time.Time
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
