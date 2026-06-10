package world

import (
	"sync"

	"spaceempire/back/internal/domain"
)

// PathRouter resolves sector-to-sector routes over the static gate graph.
// BFS is lazy and cached per source sector — the first lookup for a given
// source walks the whole reachable subgraph; subsequent lookups from the
// same source are O(1) hash hits plus one parent step for NextSector.
//
// The world is immutable after startup, so cache entries never expire.
// Concurrent readers walk the cache under RLock; writers take Lock and
// re-check the cache after acquiring it (another goroutine may have
// finished the BFS while we were waiting).
type PathRouter struct {
	topo     *Topology
	excluded map[domain.SectorID]struct{}

	mu    sync.RWMutex
	cache map[domain.SectorID]*bfsResult
}

// bfsResult holds per-destination distance and parent pointer relative to
// the source sector that owns this entry. parent[s] is the sector visited
// immediately before s on the shortest path from the source; the source
// itself maps to itself.
type bfsResult struct {
	dist   map[domain.SectorID]int
	parent map[domain.SectorID]domain.SectorID
}

// NewPathRouter builds a router on top of an immutable topology. Sectors
// listed in `excluded` are treated as if they did not exist for the
// purposes of routing: BFS will not traverse them, and any source or
// destination among them is reported as unreachable. The underlying
// topology — and therefore GateBetween — is unaffected.
func NewPathRouter(topo *Topology, excluded []domain.SectorID) *PathRouter {
	ex := make(map[domain.SectorID]struct{}, len(excluded))
	for _, s := range excluded {
		ex[s] = struct{}{}
	}
	return &PathRouter{
		topo:     topo,
		excluded: ex,
		cache:    make(map[domain.SectorID]*bfsResult),
	}
}

// NextSector returns the first hop from `from` toward `to`. Special case:
// when from == to the router reports (from, true) — caller can detect
// "already at destination" by comparing the result to the source.
// Returns (0, false) when the destination is unreachable or either
// endpoint is excluded.
func (r *PathRouter) NextSector(from, to domain.SectorID) (domain.SectorID, bool) {
	if from == to {
		return from, true
	}
	if r.isExcluded(from) || r.isExcluded(to) {
		return 0, false
	}

	res := r.bfsFrom(from)
	if _, ok := res.dist[to]; !ok {
		return 0, false
	}

	// Walk parent pointers from `to` back until the previous step is `from`.
	cur := to
	for {
		prev := res.parent[cur]
		if prev == from {
			return cur, true
		}
		cur = prev
	}
}

// Hops returns the shortest-path distance in jumps from `from` to `to`.
// Hops(A, A) is (0, true). Returns (0, false) when unreachable or either
// endpoint is excluded.
func (r *PathRouter) Hops(from, to domain.SectorID) (int, bool) {
	if from == to {
		return 0, true
	}
	if r.isExcluded(from) || r.isExcluded(to) {
		return 0, false
	}

	res := r.bfsFrom(from)
	d, ok := res.dist[to]
	if !ok {
		return 0, false
	}
	return d, true
}

func (r *PathRouter) isExcluded(s domain.SectorID) bool {
	_, blocked := r.excluded[s]
	return blocked
}

// GateBetween delegates to Topology — excluded sectors are not filtered
// here, since the gate physically exists. Callers that need to honor
// exclusions should go through NextSector/Hops first.
func (r *PathRouter) GateBetween(a, b domain.SectorID) *domain.Gate {
	return r.topo.GateBetween(a, b)
}

// GateSidePos returns the exit coordinates on the `from` side of the
// gate connecting `from` to `to`. Only direct neighbours are valid input
// — for multi-hop routes, callers chain NextSector + GateSidePos. Returns
// (Vec2{}, false) when no gate links the two sectors.
func (r *PathRouter) GateSidePos(from, to domain.SectorID) (domain.Vec2, bool) {
	g := r.topo.GateBetween(from, to)
	if g == nil {
		return domain.Vec2{}, false
	}
	if g.SectorA == from {
		return g.PosA, true
	}
	return g.PosB, true
}

func (r *PathRouter) bfsFrom(source domain.SectorID) *bfsResult {
	r.mu.RLock()
	if res, ok := r.cache[source]; ok {
		r.mu.RUnlock()
		return res
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	if res, ok := r.cache[source]; ok {
		return res
	}

	res := r.runBFS(source)
	r.cache[source] = res
	return res
}

func (r *PathRouter) runBFS(source domain.SectorID) *bfsResult {
	res := &bfsResult{
		dist:   map[domain.SectorID]int{source: 0},
		parent: map[domain.SectorID]domain.SectorID{source: source},
	}

	queue := []domain.SectorID{source}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for neighbour := range r.topo.adjacency[cur] {
			if _, blocked := r.excluded[neighbour]; blocked {
				continue
			}
			if _, seen := res.dist[neighbour]; seen {
				continue
			}
			res.dist[neighbour] = res.dist[cur] + 1
			res.parent[neighbour] = cur
			queue = append(queue, neighbour)
		}
	}
	return res
}
