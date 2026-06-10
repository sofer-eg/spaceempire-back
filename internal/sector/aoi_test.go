package sector

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

func TestUnit_SpatialGrid_QueryReturnsOnlyShipsInRadius(t *testing.T) {
	t.Parallel()

	g := newSpatialGrid(100)
	in := &domain.Ship{ID: 1, Pos: domain.Vec2{X: 10, Y: 10}}
	edge := &domain.Ship{ID: 2, Pos: domain.Vec2{X: 50, Y: 0}}
	out := &domain.Ship{ID: 3, Pos: domain.Vec2{X: 1000, Y: 1000}}
	for _, s := range []*domain.Ship{in, edge, out} {
		g.insert(s)
	}

	got := g.queryIDs(domain.Vec2{X: 0, Y: 0}, 60)

	require.Contains(t, got, in.ID)
	require.Contains(t, got, edge.ID)
	require.NotContains(t, got, out.ID)
}

func TestUnit_SpatialGrid_BoundaryShipsAreVisible(t *testing.T) {
	t.Parallel()

	g := newSpatialGrid(50)
	exact := &domain.Ship{ID: 1, Pos: domain.Vec2{X: 100, Y: 0}}
	justOutside := &domain.Ship{ID: 2, Pos: domain.Vec2{X: 100.0001, Y: 0}}
	g.insert(exact)
	g.insert(justOutside)

	got := g.queryIDs(domain.Vec2{X: 0, Y: 0}, 100)

	assert.Contains(t, got, exact.ID, "ship at exactly radius distance must be visible")
	assert.NotContains(t, got, justOutside.ID)
}

func TestUnit_SpatialGrid_QueryAcrossManyCells(t *testing.T) {
	t.Parallel()

	g := newSpatialGrid(10)
	for i := 0; i < 100; i++ {
		g.insert(&domain.Ship{ID: domain.ShipID(i + 1), Pos: domain.Vec2{X: float64(i), Y: 0}})
	}

	got := g.queryIDs(domain.Vec2{X: 50, Y: 0}, 15)

	ids := make([]int, 0, len(got))
	for id := range got {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	// ship i has Pos.X = i (0-indexed slot i becomes ID i+1).
	// distance(i, 50) <= 15 ⇒ i ∈ [35, 65], so IDs 36..66 inclusive.
	require.Equal(t, 31, len(ids))
	assert.Equal(t, 36, ids[0])
	assert.Equal(t, 66, ids[len(ids)-1])
}

func TestUnit_SpatialGrid_NegativeCoordinatesQuery(t *testing.T) {
	t.Parallel()

	g := newSpatialGrid(10)
	left := &domain.Ship{ID: 1, Pos: domain.Vec2{X: -5, Y: -5}}
	right := &domain.Ship{ID: 2, Pos: domain.Vec2{X: 5, Y: 5}}
	g.insert(left)
	g.insert(right)

	got := g.queryIDs(domain.Vec2{X: -3, Y: -3}, 5)

	assert.Contains(t, got, left.ID)
	assert.NotContains(t, got, right.ID)
}

func TestUnit_SpatialGrid_ZeroRadiusReturnsEmpty(t *testing.T) {
	t.Parallel()

	g := newSpatialGrid(10)
	g.insert(&domain.Ship{ID: 1, Pos: domain.Vec2{X: 0, Y: 0}})

	got := g.queryIDs(domain.Vec2{X: 0, Y: 0}, 0)

	assert.Empty(t, got, "zero radius is a degenerate window — no ships should match")
}

func TestUnit_BuildGrid_PopulatesAllShips(t *testing.T) {
	t.Parallel()

	ships := map[domain.ShipID]*domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 0, Y: 0}},
		2: {ID: 2, Pos: domain.Vec2{X: 100, Y: 100}},
		3: {ID: 3, Pos: domain.Vec2{X: 200, Y: 200}},
	}

	g := buildGrid(ships, 50)

	got := g.queryIDs(domain.Vec2{X: 100, Y: 100}, 1000)
	require.Len(t, got, 3)
}

func TestUnit_PlayerShip_ReturnsOwnShip(t *testing.T) {
	t.Parallel()

	ships := map[domain.ShipID]*domain.Ship{
		1: {ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 100, Y: 200}, RadarRange: 3500},
		2: {ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 999, Y: 999}},
	}

	got := playerShip(ships, 7)
	require.NotNil(t, got)
	assert.Equal(t, domain.Vec2{X: 100, Y: 200}, got.Pos)
	assert.Equal(t, 3500.0, got.RadarRange)
}

func TestUnit_PlayerShip_NilWhenNoOwnShip(t *testing.T) {
	t.Parallel()

	ships := map[domain.ShipID]*domain.Ship{
		1: {ID: 1, PlayerID: 8, Pos: domain.Vec2{X: 100, Y: 200}},
	}

	assert.Nil(t, playerShip(ships, 7))
}

func TestUnit_RadarOrFallback(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 3500.0, radarOrFallback(3500, 5000))
	assert.Equal(t, 5000.0, radarOrFallback(0, 5000), "no class radar → fallback")
	assert.Equal(t, 5000.0, radarOrFallback(-1, 5000))
}
