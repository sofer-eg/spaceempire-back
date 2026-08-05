package dto

import "spaceempire/back/internal/domain"

// LaunchDroneRequest is the body of POST /api/cmd/launch-drone. PlayerID
// comes from the session cookie.
//
// There is no count: the SERVER decides the salvo size (TASK-176) —
// min(up_drone_control level − live drones, drones in the hold). The clients used
// to send a fixed 3 of their own, and the one that did not also clamp it to the
// hold (the canvas menu, which has no cargo) turned every short magazine into a
// 400. Only the worker knows the cap and only its transaction can size the hold, so
// neither number can be decided here or in the SPA.
type LaunchDroneRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
}

// LaunchDroneResponse echoes how many drones actually launched — the cap, or fewer
// when the hold had fewer aboard. Nothing is left over to refund: the salvo is
// sized, charged and INSERTed in one transaction inside the worker.
type LaunchDroneResponse struct {
	OK      bool `json:"ok"`
	Spawned int  `json:"spawned"`
}

// RecallDronesRequest is the body of POST /api/cmd/recall-drones.
type RecallDronesRequest struct {
	ShipID int64 `json:"shipID"`
}

// RecallDronesResponse reports how many drones returned to cargo and how many
// stayed out because the hold could not take them (TASK-156). left > 0 is not an
// error: the player frees space and recalls again.
type RecallDronesResponse struct {
	OK       bool `json:"ok"`
	Recalled int  `json:"recalled"`
	Left     int  `json:"left"`
}

// Drone mirrors domain.Drone on the wire. Pos / Vel / Direction are split
// into scalar pairs for parity with Ship/Missile.
type Drone struct {
	ID     int64     `json:"id"`
	Owner  int64     `json:"owner"`
	Target EntityRef `json:"target"`
	X      float64   `json:"x"`
	Y      float64   `json:"y"`
	VX     float64   `json:"vx"`
	VY     float64   `json:"vy"`
	DirX   float64   `json:"dirX"`
	DirY   float64   `json:"dirY"`
	HP     int       `json:"hp"`
}

// DroneImpact is a one-frame drone event: a shot fired (Damage, Killed)
// or a death/expire (Expired). The SPA renders a brief flash at (X, Y).
type DroneImpact struct {
	DroneID int64     `json:"droneID"`
	Owner   int64     `json:"owner"`
	Target  EntityRef `json:"target"`
	X       float64   `json:"x"`
	Y       float64   `json:"y"`
	Damage  int       `json:"damage,omitempty"`
	Killed  bool      `json:"killed,omitempty"`
	Expired bool      `json:"expired,omitempty"`
}

// DroneFromDomain converts a domain.Drone to its wire form.
func DroneFromDomain(d domain.Drone) Drone {
	return Drone{
		ID:    int64(d.ID),
		Owner: int64(d.OwnerShipID),
		Target: EntityRef{
			Kind: int(d.Target.Kind),
			ID:   d.Target.ID,
		},
		X:    d.Pos.X,
		Y:    d.Pos.Y,
		VX:   d.Vel.X,
		VY:   d.Vel.Y,
		DirX: d.Direction.X,
		DirY: d.Direction.Y,
		HP:   d.HP,
	}
}

// DronesFromDomain bulk-converts a slice of domain drones.
func DronesFromDomain(in []domain.Drone) []Drone {
	out := make([]Drone, len(in))
	for i, d := range in {
		out[i] = DroneFromDomain(d)
	}
	return out
}
