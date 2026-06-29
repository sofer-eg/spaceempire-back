package domain

import "time"

// TorpedoID identifies one persistent torpedo. Like DroneID (and unlike the
// per-worker MissileID), TorpedoID is the database-assigned primary key of
// the `torpedos` table: torpedoes are persistent combat objects with HP and
// a TTL — they survive a server restart, so the id must be stable across
// restarts (see ЧТЗ doc-1 §3 FR-001, NFR-002).
type TorpedoID int64

// Torpedo is a heavy, slow, high-damage homing projectile launched from an
// owner ship. Unlike a Missile (reconstructable, RAM-only) a Torpedo is a
// persistent combat object: it has HP (so it can be shot down), a finite
// TTL, and its own DB row — immediate INSERT at launch, immediate DELETE at
// death/expire/detonation, periodic BatchUpdate of the mutable fields. It
// is modelled on domain.Drone for persistence and on domain.Missile for the
// homing physics family (see ЧТЗ doc-1 §3 FR-001, §5.1).
//
// Pos / Vel / Direction are integrated by combat.TickTorpedo every tick
// (implemented in a later sub-task). Direction is kept a unit vector.
type Torpedo struct {
	ID          TorpedoID
	SectorID    SectorID
	OwnerShipID ShipID
	PlayerID    PlayerID

	Pos       Vec2
	Vel       Vec2
	Direction Vec2

	// Target names the entity the torpedo homes toward (a ship or a
	// destructible static object). LastTargetPos is the most recent
	// position the sector could observe; once the target dies or leaves
	// the sector, the torpedo keeps flying toward this snapshot until the
	// TTL runs out (same fallback as Missile, see ЧТЗ doc-1 §3 FR-005).
	Target        EntityRef
	LastTargetPos Vec2

	// Class is the ammunition class: 2 (gt23 "Огненная Буря") or 3
	// (gt24 "Святая Торпеда"). It selects the balance profile (ЧТЗ §5.1).
	Class int

	Damage       int
	Speed        float64
	Accel        float64
	TurnRate     float64
	HitRadius    float64
	SplashRadius float64

	HP        int
	ExpiresAt time.Time
}
