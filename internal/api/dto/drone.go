package dto

import "spaceempire/back/internal/domain"

// LaunchDroneRequest is the body of POST /api/cmd/launch-drone. PlayerID
// comes from the session cookie. Count drones are launched at TargetRef.
type LaunchDroneRequest struct {
	ShipID    int64     `json:"shipID"`
	TargetRef EntityRef `json:"targetRef"`
	Count     int       `json:"count"`
}

// LaunchDroneResponse echoes how many drones were actually spawned (the
// handler refunds the rest of the requested count if a launch fell short).
type LaunchDroneResponse struct {
	OK      bool `json:"ok"`
	Spawned int  `json:"spawned"`
}

// RecallDronesRequest is the body of POST /api/cmd/recall-drones.
type RecallDronesRequest struct {
	ShipID int64 `json:"shipID"`
}

// RecallDronesResponse reports how many drones returned to cargo.
type RecallDronesResponse struct {
	OK       bool `json:"ok"`
	Recalled int  `json:"recalled"`
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
