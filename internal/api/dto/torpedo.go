package dto

import "spaceempire/back/internal/domain"

// LaunchTorpedoRequest is the body of POST /api/cmd/launch-torpedo. PlayerID
// comes from the session cookie; the body carries the launching ship, the
// target, and the ammunition Class (2 = gt23 "Огненная Буря", 3 = gt24
// "Святая Торпеда").
type LaunchTorpedoRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
	Class     int       `json:"class"`
}

// LaunchTorpedoResponse echoes the torpedo id allocated by the worker so the
// client can correlate WS frames with its own UI state. The torpedo spawn is a
// later sub-task (TASK-100.3.5.4); until then a successful launch only enforces
// the gates and the energy cost, so TorpedoID is zero.
type LaunchTorpedoResponse struct {
	OK        bool  `json:"ok"`
	TorpedoID int64 `json:"torpedoID"`
}

// Torpedo mirrors domain.Torpedo on the wire (ЧТЗ doc-1 §3 FR-010). Pos / Vel /
// Direction are split into scalar pairs for parity with Ship / Drone / Missile so
// the SPA can render the icon and the homing trail without re-deriving Vec2.
// Class selects the ammunition profile/icon (2 = "Огненная Буря", 3 = "Святая
// Торпеда") and HP lets the renderer show that a torpedo can be shot down. A
// separate DTO list keeps Ship untouched (ЧТЗ NFR-006 / R-05). The TTL fence is
// the server's concern, so ExpiresAt is intentionally not on the wire.
type Torpedo struct {
	ID     int64     `json:"id"`
	Owner  int64     `json:"owner"`
	Target EntityRef `json:"target"`
	X      float64   `json:"x"`
	Y      float64   `json:"y"`
	VX     float64   `json:"vx"`
	VY     float64   `json:"vy"`
	DirX   float64   `json:"dirX"`
	DirY   float64   `json:"dirY"`
	Class  int       `json:"class"`
	HP     int       `json:"hp"`
}

// TorpedoImpact is a one-frame torpedo event broadcast in the same Snapshot that
// removes the torpedo (mirrors MissileImpact / DroneImpact). Exactly one outcome
// flag is set: Hit (a detonation — carries the splash centre X/Y and SplashRadius
// so the SPA can animate the area blast, ЧТЗ §5.3), Killed (shot down — dies in
// place, no splash), or Expired (TTL / owner-loss — no damage). The renderer
// draws a brief flash at (X, Y); a Hit additionally draws the SplashRadius ring.
type TorpedoImpact struct {
	TorpedoID    int64     `json:"torpedoID"`
	Owner        int64     `json:"owner"`
	Target       EntityRef `json:"target"`
	X            float64   `json:"x"`
	Y            float64   `json:"y"`
	SplashRadius float64   `json:"splashRadius,omitempty"`
	Hit          bool      `json:"hit,omitempty"`
	Killed       bool      `json:"killed,omitempty"`
	Expired      bool      `json:"expired,omitempty"`
}

// TorpedoFromDomain converts a domain.Torpedo to its wire form.
func TorpedoFromDomain(t domain.Torpedo) Torpedo {
	return Torpedo{
		ID:    int64(t.ID),
		Owner: int64(t.OwnerShipID),
		Target: EntityRef{
			Kind: int(t.Target.Kind),
			ID:   t.Target.ID,
		},
		X:     t.Pos.X,
		Y:     t.Pos.Y,
		VX:    t.Vel.X,
		VY:    t.Vel.Y,
		DirX:  t.Direction.X,
		DirY:  t.Direction.Y,
		Class: t.Class,
		HP:    t.HP,
	}
}

// TorpedosFromDomain bulk-converts a slice of domain torpedoes.
func TorpedosFromDomain(in []domain.Torpedo) []Torpedo {
	out := make([]Torpedo, len(in))
	for i, t := range in {
		out[i] = TorpedoFromDomain(t)
	}
	return out
}
