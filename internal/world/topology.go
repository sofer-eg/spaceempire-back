// Package world holds the in-memory representation of the world topology
// (sectors and gates). The data is loaded from Postgres once at startup and is
// read-only except for one event: a gate being destroyed (TASK-110), which
// removes its link. That single mutation is guarded by a RWMutex and bumps a
// version counter so cached path searches can tell their graph moved under them.
package world

import (
	"sync"
	"sync/atomic"

	"spaceempire/back/internal/domain"
)

// Topology is the sector/gate map served to every consumer (HTTP /api/world,
// path router, sector workers). It is the single authority on which links exist:
// destroying a gate here is what makes every reader — routes, jumps, both linked
// workers — agree that the sector pair is no longer connected.
type Topology struct {
	sectors []domain.Sector

	mu    sync.RWMutex
	gates []domain.Gate

	// adjacency[a][b] returns the gate linking sectors a and b in either
	// direction. Symmetric: a key exists for both endpoints of every gate.
	adjacency map[domain.SectorID]map[domain.SectorID]*domain.Gate

	// version increments on every graph change (a destroyed gate). Readers that
	// cache derived data (PathRouter's BFS results) compare it to spot a stale
	// cache without holding the topology lock. Atomic so a router can read it on
	// its hot path.
	version atomic.Uint64
}

// New builds a Topology from the loaded slices. The slices are kept as
// the canonical order — callers must not mutate them after passing in.
func New(sectors []domain.Sector, gates []domain.Gate) *Topology {
	t := &Topology{sectors: sectors, gates: gates}
	t.rebuildAdjacency()
	return t
}

// rebuildAdjacency recomputes the neighbour index from the current gate slice.
// Callers must hold the write lock (or be inside New, before publication).
func (t *Topology) rebuildAdjacency() {
	adj := make(map[domain.SectorID]map[domain.SectorID]*domain.Gate, len(t.sectors))
	for i := range t.gates {
		g := &t.gates[i]
		if adj[g.SectorA] == nil {
			adj[g.SectorA] = make(map[domain.SectorID]*domain.Gate)
		}
		if adj[g.SectorB] == nil {
			adj[g.SectorB] = make(map[domain.SectorID]*domain.Gate)
		}
		adj[g.SectorA][g.SectorB] = g
		adj[g.SectorB][g.SectorA] = g
	}
	t.adjacency = adj
}

// Sectors returns every sector in load order. The returned slice shares
// memory with Topology; callers must treat it as read-only.
func (t *Topology) Sectors() []domain.Sector {
	return t.sectors
}

// Gates returns every live gate. The result is a copy: the backing slice can be
// rebuilt by DestroyGate, so handing out a shared view would let a caller iterate
// a slice that is being replaced.
func (t *Topology) Gates() []domain.Gate {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]domain.Gate, len(t.gates))
	copy(out, t.gates)
	return out
}

// Gate returns the live gate with the given id, or nil when it does not exist or
// has been destroyed. Returns a copy for the same reason Gates does.
func (t *Topology) Gate(id domain.GateID) *domain.Gate {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i := range t.gates {
		if t.gates[i].ID == id {
			g := t.gates[i]
			return &g
		}
	}
	return nil
}

// GatesInSector returns every live gate with an endpoint in the given sector —
// what a sector worker needs to register its shootable gate endpoints.
func (t *Topology) GatesInSector(sectorID domain.SectorID) []domain.Gate {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []domain.Gate
	for i := range t.gates {
		if t.gates[i].SectorA == sectorID || t.gates[i].SectorB == sectorID {
			out = append(out, t.gates[i])
		}
	}
	return out
}

// GateBetween returns the gate connecting sectors a and b, or nil if no
// such gate exists (or it was destroyed). The lookup is symmetric:
// GateBetween(a, b) and GateBetween(b, a) return the same gate.
//
// The returned pointer aims into the topology's own slice, which DestroyGate may
// replace — callers use it within the tick that fetched it and never write to it.
func (t *Topology) GateBetween(a, b domain.SectorID) *domain.Gate {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if neighbours, ok := t.adjacency[a]; ok {
		return neighbours[b]
	}
	return nil
}

// DestroyGate removes a gate from the graph: its link disappears from adjacency
// and it stops being returned by Gates/Gate/GatesInSector, so routes, jumps and
// both linked workers immediately agree the sectors are no longer connected
// (TASK-110). Reports whether the gate was there to remove, so the caller can tell
// the first destruction from a repeat (both endpoints can be shot to death in the
// same tick pair).
//
// Persistence is the caller's job (world persistence MarkDestroyed): this only
// changes the live graph.
func (t *Topology) DestroyGate(id domain.GateID) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.gates {
		if t.gates[i].ID != id {
			continue
		}
		t.gates = append(t.gates[:i:i], t.gates[i+1:]...)
		t.rebuildAdjacency()
		t.version.Add(1)
		return true
	}
	return false
}

// Version is the graph's revision. It changes only when a link is removed;
// consumers caching derived data compare it to detect that their cache is stale.
func (t *Topology) Version() uint64 {
	return t.version.Load()
}

// neighbours returns the sectors reachable from sectorID in one jump. Used by the
// router's BFS, which must not touch the adjacency map directly: DestroyGate
// replaces it under the write lock.
func (t *Topology) neighbours(sectorID domain.SectorID) []domain.SectorID {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]domain.SectorID, 0, len(t.adjacency[sectorID]))
	for n := range t.adjacency[sectorID] {
		out = append(out, n)
	}
	return out
}
