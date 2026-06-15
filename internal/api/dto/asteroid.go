package dto

import "spaceempire/back/internal/domain"

// Asteroid mirrors domain.Asteroid on the wire. Pos and OreType are fixed at
// creation; Mass shrinks as the body is mined, so a delta may re-send an
// asteroid with a lower Mass (the SPA patches its glyph / yield readout).
type Asteroid struct {
	ID      int64   `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Mass    int64   `json:"mass"`
	OreType int64   `json:"ore_type"`
}

// AsteroidFromDomain converts a domain.Asteroid to its wire form.
func AsteroidFromDomain(a domain.Asteroid) Asteroid {
	return Asteroid{
		ID:      int64(a.ID),
		X:       a.Pos.X,
		Y:       a.Pos.Y,
		Mass:    a.Mass,
		OreType: int64(a.OreType),
	}
}

// AsteroidsFromDomain bulk-converts a slice of domain asteroids.
func AsteroidsFromDomain(in []domain.Asteroid) []Asteroid {
	out := make([]Asteroid, len(in))
	for i, a := range in {
		out[i] = AsteroidFromDomain(a)
	}
	return out
}
