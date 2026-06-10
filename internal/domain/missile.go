package domain

import "time"

// MissileID identifies one live missile inside a sector worker. The id
// space is per-worker — counters reset on worker restart because missiles
// are reconstructable state (not persisted).
type MissileID int64

// Missile is a homing projectile owned by an attacker ship. Reconstructable
// state (phase 4.3): no DB persistence, no cold-start recovery — every
// server restart drops every live missile.
//
// Movement model ports SP `TO_Missiles` (`starwind/sql/db.sql:28942`)
// with the simplifications captured in `back/docs/specs/missiles.md`:
// intra-sector flight only, no random hit roll, no cross-sector hop.
type Missile struct {
	ID          MissileID
	SectorID    SectorID
	OwnerShipID ShipID
	PlayerID    PlayerID

	// Pos / Vel / Direction are integrated in TickMissile every tick.
	// Direction is a unit vector — `combat.TickMissile` keeps it
	// normalised after each rotation. Vel inherits the attacker's
	// velocity at launch so a fast strafing pilot does not eject a
	// missile that drifts backwards relative to its target.
	Pos       Vec2
	Vel       Vec2
	Direction Vec2

	// Target names the entity the missile chases. Set at launch and
	// never re-targeted. LastTargetPos is the most recent position the
	// sector could observe (= target's live Pos while the target is
	// alive and in this sector); once the target dies or leaves the
	// sector, the missile keeps flying toward this snapshot until the
	// TTL runs out — by design (see spec §1).
	Target        EntityRef
	LastTargetPos Vec2

	Damage    int
	Speed     float64
	Accel     float64
	TurnRate  float64
	HitRadius float64

	ExpiresAt time.Time
}
