// Package world holds the in-memory representation of the static world
// topology (sectors and gates). The data is loaded from Postgres once at
// startup; nothing mutates Topology at runtime, so lookups need no locks.
package world

import "spaceempire/back/internal/domain"

// Topology is the read-only sector/gate map served to every consumer
// (HTTP /api/world, path router, sector workers).
type Topology struct {
	sectors []domain.Sector
	gates   []domain.Gate

	// adjacency[a][b] returns the gate linking sectors a and b in either
	// direction. Symmetric: a key exists for both endpoints of every gate.
	adjacency map[domain.SectorID]map[domain.SectorID]*domain.Gate
}

// New builds a Topology from the loaded slices. The slices are kept as
// the canonical order — callers must not mutate them after passing in.
func New(sectors []domain.Sector, gates []domain.Gate) *Topology {
	adj := make(map[domain.SectorID]map[domain.SectorID]*domain.Gate, len(sectors))
	for i := range gates {
		g := &gates[i]
		if adj[g.SectorA] == nil {
			adj[g.SectorA] = make(map[domain.SectorID]*domain.Gate)
		}
		if adj[g.SectorB] == nil {
			adj[g.SectorB] = make(map[domain.SectorID]*domain.Gate)
		}
		adj[g.SectorA][g.SectorB] = g
		adj[g.SectorB][g.SectorA] = g
	}
	return &Topology{sectors: sectors, gates: gates, adjacency: adj}
}

// Sectors returns every sector in load order. The returned slice shares
// memory with Topology; callers must treat it as read-only.
func (t *Topology) Sectors() []domain.Sector {
	return t.sectors
}

// Gates returns every gate in load order. Read-only, same caveat as Sectors.
func (t *Topology) Gates() []domain.Gate {
	return t.gates
}

// GateBetween returns the gate connecting sectors a and b, or nil if no
// such gate exists. The lookup is symmetric: GateBetween(a, b) and
// GateBetween(b, a) return the same gate.
func (t *Topology) GateBetween(a, b domain.SectorID) *domain.Gate {
	if neighbours, ok := t.adjacency[a]; ok {
		return neighbours[b]
	}
	return nil
}
