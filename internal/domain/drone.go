package domain

import "time"

// DroneID identifies one persistent combat drone. Unlike MissileID (a
// per-worker counter for reconstructable state), DroneID is the
// database-assigned primary key of the `drones` table: drones survive a
// server restart (see back/docs/specs/drones.md §3), so the id must be
// stable across restarts.
type DroneID int64

// Drone is an autonomous combat unit launched from an owner ship. It
// chases the ship it was launched at, brakes at StandoffRange to hold
// station, and fires every tick it is in range. Persistent state:
// immediate INSERT at launch, immediate DELETE at death/recall, periodic
// BatchUpdate of the mutable fields.
//
// Movement ports the same physics family as Missile (see
// back/docs/specs/drones.md §1); Pos / Vel / Direction are integrated by
// combat.TickDrone every tick. Direction is kept a unit vector.
type Drone struct {
	ID          DroneID
	SectorID    SectorID
	OwnerShipID ShipID
	PlayerID    PlayerID

	Pos       Vec2
	Vel       Vec2
	Direction Vec2

	// Target is the entity the drone was launched at (phase 4.4: always
	// an EntityKindShip). Never re-targeted in this phase — auto-acquire
	// arrives with relations in 6.2 (spec §4).
	Target EntityRef

	HP        int
	Damage    int
	ExpiresAt time.Time
}
