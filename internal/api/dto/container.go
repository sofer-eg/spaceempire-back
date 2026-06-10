package dto

import "spaceempire/back/internal/domain"

// PickupContainerRequest is the body of POST /api/cmd/pickup-container.
// PlayerID comes from the session cookie.
type PickupContainerRequest struct {
	ShipID      int64 `json:"shipID"`
	ContainerID int64 `json:"containerID"`
}

// PickupContainerResponse acknowledges a successful pickup.
type PickupContainerResponse struct {
	OK bool `json:"ok"`
}

// Container mirrors domain.Container on the wire. The cargo inside is not
// shipped in the radar delta — the SPA only needs the glyph position;
// the contents transfer on pickup.
type Container struct {
	ID int64   `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

// ContainerFromDomain converts a domain.Container to its wire form.
func ContainerFromDomain(c domain.Container) Container {
	return Container{ID: int64(c.ID), X: c.Pos.X, Y: c.Pos.Y}
}

// ContainersFromDomain bulk-converts a slice of domain containers.
func ContainersFromDomain(in []domain.Container) []Container {
	out := make([]Container, len(in))
	for i, c := range in {
		out[i] = ContainerFromDomain(c)
	}
	return out
}
