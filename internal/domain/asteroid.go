package domain

// AsteroidID is the database-assigned primary key of the `asteroids` table.
// Asteroids are persistent: their mass is mined down over time and the row is
// deleted when depleted, so the id must be stable across restarts.
type AsteroidID int64

// Asteroid is a minable ore body sitting at a fixed position in a sector.
// NPC miners (phase 5.4) drill it: each Mine action lowers Mass and yields
// that much OreType into the miner's hold. When Mass reaches zero the
// asteroid is removed from the world.
//
// Persistent state: loaded at cold-start, Mass written by the periodic
// snapshot (like a drone's mutable fields), the row deleted immediately when
// depleted. Pos and OreType never change after creation.
type Asteroid struct {
	ID       AsteroidID
	SectorID SectorID
	Pos      Vec2
	// Mass is the remaining ore, in units of OreType. Drilling subtracts
	// from it; at <= 0 the asteroid is depleted and removed.
	Mass int64
	// OreType is the goods type the asteroid yields — stored directly as a
	// GoodsTypeID (no separate "asteroid type" mapping, unlike the old
	// schema's gt = type + 7).
	OreType GoodsTypeID
}
