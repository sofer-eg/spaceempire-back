package dto

import (
	"time"

	"spaceempire/back/internal/domain"
)

// LaunchMissileRequest is the body of POST /api/cmd/launch-missile.
// PlayerID is sourced from the session cookie; the body only carries
// the launching ship and the target.
type LaunchMissileRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
}

// LaunchMissileResponse echoes the worker-allocated missile id so the
// client can correlate WS frames with its own UI optimistic state.
type LaunchMissileResponse struct {
	OK        bool  `json:"ok"`
	MissileID int64 `json:"missileID"`
}

// Missile mirrors domain.Missile on the wire. Pos / Vel / Direction are
// split into scalar pairs for parity with Ship and so the SPA can read
// them without re-deriving Vec2 helpers. ExpiresAt is the wall-clock
// instant the missile auto-detonates (TTL fence).
type Missile struct {
	ID        int64     `json:"id"`
	Attacker  int64     `json:"attacker"`
	Target    EntityRef `json:"target"`
	X         float64   `json:"x"`
	Y         float64   `json:"y"`
	VX        float64   `json:"vx"`
	VY        float64   `json:"vy"`
	DirX      float64   `json:"dirX"`
	DirY      float64   `json:"dirY"`
	ExpiresAt string    `json:"expiresAt"`
}

// MissileImpact is a one-frame event broadcast in the same Snapshot
// that removes the missile. Expired==true means TTL ran out (no
// damage); otherwise Damage/Killed describe the hit outcome.
type MissileImpact struct {
	MissileID int64     `json:"missileID"`
	Attacker  int64     `json:"attacker"`
	Target    EntityRef `json:"target"`
	X         float64   `json:"x"`
	Y         float64   `json:"y"`
	Damage    int       `json:"damage,omitempty"`
	Killed    bool      `json:"killed,omitempty"`
	Expired   bool      `json:"expired,omitempty"`
}

// MissileFromDomain converts a domain.Missile to its wire form.
func MissileFromDomain(m domain.Missile) Missile {
	return Missile{
		ID:       int64(m.ID),
		Attacker: int64(m.OwnerShipID),
		Target: EntityRef{
			Kind: int(m.Target.Kind),
			ID:   m.Target.ID,
		},
		X:         m.Pos.X,
		Y:         m.Pos.Y,
		VX:        m.Vel.X,
		VY:        m.Vel.Y,
		DirX:      m.Direction.X,
		DirY:      m.Direction.Y,
		ExpiresAt: m.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

// MissilesFromDomain bulk-converts a slice of domain missiles.
func MissilesFromDomain(in []domain.Missile) []Missile {
	out := make([]Missile, len(in))
	for i, m := range in {
		out[i] = MissileFromDomain(m)
	}
	return out
}
