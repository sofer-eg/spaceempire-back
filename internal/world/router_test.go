package world_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/world"
)

// routerFixture builds a small 5-sector map shaped like:
//
//	1 ── 2 ── 3 ── 4
//	     │
//	     5
//
// Sector 6 exists with no gates (unreachable from anywhere).
func routerFixture() *world.Topology {
	sectors := []domain.Sector{
		{ID: 1, Name: "S1"},
		{ID: 2, Name: "S2"},
		{ID: 3, Name: "S3"},
		{ID: 4, Name: "S4"},
		{ID: 5, Name: "S5"},
		{ID: 6, Name: "Isolated"},
	}
	gates := []domain.Gate{
		{ID: 10, SectorA: 1, PosA: domain.Vec2{X: 100, Y: 0}, SectorB: 2, PosB: domain.Vec2{X: -100, Y: 0}},
		{ID: 11, SectorA: 2, PosA: domain.Vec2{X: 100, Y: 0}, SectorB: 3, PosB: domain.Vec2{X: -100, Y: 0}},
		{ID: 12, SectorA: 3, PosA: domain.Vec2{X: 100, Y: 0}, SectorB: 4, PosB: domain.Vec2{X: -100, Y: 0}},
		{ID: 13, SectorA: 2, PosA: domain.Vec2{X: 0, Y: 100}, SectorB: 5, PosB: domain.Vec2{X: 0, Y: -100}},
	}
	return world.New(sectors, gates)
}

func TestUnit_PathRouter_NextSector_SameSource(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	next, ok := r.NextSector(2, 2)
	assert.True(t, ok)
	assert.Equal(t, domain.SectorID(2), next)
}

func TestUnit_PathRouter_NextSector_DirectNeighbour(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	next, ok := r.NextSector(1, 2)
	require.True(t, ok)
	assert.Equal(t, domain.SectorID(2), next)
}

func TestUnit_PathRouter_NextSector_MultiHop(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	next, ok := r.NextSector(1, 4)
	require.True(t, ok)
	assert.Equal(t, domain.SectorID(2), next, "first hop from 1 toward 4 must be 2")

	next, ok = r.NextSector(4, 1)
	require.True(t, ok)
	assert.Equal(t, domain.SectorID(3), next, "first hop from 4 toward 1 must be 3")

	next, ok = r.NextSector(5, 4)
	require.True(t, ok)
	assert.Equal(t, domain.SectorID(2), next, "first hop from 5 toward 4 must be 2")
}

func TestUnit_PathRouter_NextSector_Unreachable(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	_, ok := r.NextSector(1, 6)
	assert.False(t, ok)

	_, ok = r.NextSector(6, 1)
	assert.False(t, ok)
}

func TestUnit_PathRouter_NextSector_ExcludedBlocksTraversal(t *testing.T) {
	t.Parallel()

	// Exclude sector 3, breaking the 1-2-3-4 chain. Sector 4 becomes
	// unreachable from anywhere.
	r := world.NewPathRouter(routerFixture(), []domain.SectorID{3})

	_, ok := r.NextSector(1, 4)
	assert.False(t, ok)

	_, ok = r.NextSector(1, 3)
	assert.False(t, ok, "excluded sector cannot be a destination either")

	// 1 → 2 → 5 still works.
	next, ok := r.NextSector(1, 5)
	require.True(t, ok)
	assert.Equal(t, domain.SectorID(2), next)
}

func TestUnit_PathRouter_Hops_Distances(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	cases := []struct {
		from, to domain.SectorID
		want     int
	}{
		{1, 1, 0},
		{1, 2, 1},
		{1, 3, 2},
		{1, 4, 3},
		{1, 5, 2},
		{5, 4, 3},
	}
	for _, c := range cases {
		d, ok := r.Hops(c.from, c.to)
		require.True(t, ok, "Hops(%d, %d) must be reachable", c.from, c.to)
		assert.Equal(t, c.want, d, "Hops(%d, %d)", c.from, c.to)
	}
}

func TestUnit_PathRouter_Hops_Unreachable(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	_, ok := r.Hops(1, 6)
	assert.False(t, ok)
}

func TestUnit_PathRouter_GateBetween_DelegatesToTopology(t *testing.T) {
	t.Parallel()

	topo := routerFixture()
	r := world.NewPathRouter(topo, []domain.SectorID{3})

	assert.Equal(t, topo.GateBetween(2, 3), r.GateBetween(2, 3),
		"GateBetween is physical and must ignore exclusions")
	assert.NotNil(t, r.GateBetween(1, 2))
	assert.Nil(t, r.GateBetween(1, 3), "no direct gate between 1 and 3")
}

func TestUnit_PathRouter_GateSidePos_BothSides(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	pos, ok := r.GateSidePos(1, 2)
	require.True(t, ok)
	assert.Equal(t, domain.Vec2{X: 100, Y: 0}, pos, "gate 10: PosA on side of sector 1")

	pos, ok = r.GateSidePos(2, 1)
	require.True(t, ok)
	assert.Equal(t, domain.Vec2{X: -100, Y: 0}, pos, "gate 10: PosB on side of sector 2")

	pos, ok = r.GateSidePos(5, 2)
	require.True(t, ok)
	assert.Equal(t, domain.Vec2{X: 0, Y: -100}, pos, "gate 13: PosB on side of sector 5")
}

func TestUnit_PathRouter_GateSidePos_NotNeighbours(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	_, ok := r.GateSidePos(1, 4)
	assert.False(t, ok, "sectors 1 and 4 are not direct neighbours")

	_, ok = r.GateSidePos(1, 6)
	assert.False(t, ok)
}

func TestUnit_PathRouter_NextSector_CachedResultMatchesFresh(t *testing.T) {
	t.Parallel()

	r := world.NewPathRouter(routerFixture(), nil)

	// First call populates the cache; second must return the same answer.
	first, ok1 := r.NextSector(1, 4)
	second, ok2 := r.NextSector(1, 4)
	assert.True(t, ok1)
	assert.True(t, ok2)
	assert.Equal(t, first, second)
}

func BenchmarkPathRouter_NextSector(b *testing.B) {
	r := world.NewPathRouter(routerFixture(), nil)
	// Warm the cache for source sector 1.
	_, _ = r.NextSector(1, 4)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.NextSector(1, 4)
	}
}
