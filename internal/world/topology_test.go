package world_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/world"
)

func fixture() (*world.Topology, []domain.Sector, []domain.Gate) {
	sectors := []domain.Sector{
		{ID: 1, Name: "A", Bounds: domain.Rect{Min: domain.Vec2{X: 0, Y: 0}, Max: domain.Vec2{X: 1, Y: 1}}},
		{ID: 2, Name: "B"},
		{ID: 3, Name: "C"},
	}
	gates := []domain.Gate{
		{ID: 10, SectorA: 1, PosA: domain.Vec2{X: 1, Y: 0}, SectorB: 2, PosB: domain.Vec2{X: -1, Y: 0}},
		{ID: 11, SectorA: 2, PosA: domain.Vec2{X: 0, Y: 1}, SectorB: 3, PosB: domain.Vec2{X: 0, Y: -1}},
	}
	return world.New(sectors, gates), sectors, gates
}

func TestUnit_Topology_Sectors_PreservesOrder(t *testing.T) {
	t.Parallel()

	topo, sectors, _ := fixture()

	got := topo.Sectors()
	assert.Equal(t, sectors, got)
}

func TestUnit_Topology_Gates_PreservesOrder(t *testing.T) {
	t.Parallel()

	topo, _, gates := fixture()

	got := topo.Gates()
	assert.Equal(t, gates, got)
}

func TestUnit_Topology_GateBetween_FindsGateBothDirections(t *testing.T) {
	t.Parallel()

	topo, _, gates := fixture()

	assert.Equal(t, &gates[0], topo.GateBetween(1, 2))
	assert.Equal(t, &gates[0], topo.GateBetween(2, 1))
	assert.Equal(t, &gates[1], topo.GateBetween(2, 3))
	assert.Equal(t, &gates[1], topo.GateBetween(3, 2))
}

func TestUnit_Topology_GateBetween_ReturnsNilWhenNoGate(t *testing.T) {
	t.Parallel()

	topo, _, _ := fixture()

	assert.Nil(t, topo.GateBetween(1, 3))
	assert.Nil(t, topo.GateBetween(1, 999))
	assert.Nil(t, topo.GateBetween(999, 1))
}

func TestUnit_Topology_New_EmptyInputs(t *testing.T) {
	t.Parallel()

	topo := world.New(nil, nil)

	assert.Empty(t, topo.Sectors())
	assert.Empty(t, topo.Gates())
	assert.Nil(t, topo.GateBetween(1, 2))
}

// TASK-110: destroying a gate takes its link out of the graph. Everything else
// reads the topology, so this one mutation is what makes routes, jumps and both
// linked sector workers agree the pair is disconnected.
func TestUnit_Topology_DestroyGate_RemovesTheLink(t *testing.T) {
	t.Parallel()

	topo, _, _ := fixture()
	before := topo.Version()

	assert.True(t, topo.DestroyGate(10), "the gate was there to destroy")
	assert.Nil(t, topo.Gate(10), "gone from lookups")
	assert.Nil(t, topo.GateBetween(1, 2), "gone from adjacency, both directions")
	assert.Nil(t, topo.GateBetween(2, 1))
	assert.Len(t, topo.Gates(), 1, "only the surviving gate is listed")
	assert.Greater(t, topo.Version(), before, "the graph revision moved")

	// The untouched link is intact — rebuilding adjacency must not lose it.
	assert.NotNil(t, topo.GateBetween(2, 3))

	// A repeat is a no-op and reports it: both endpoints of a gate can be shot to
	// death in the same tick pair, and the second kill must not double-count.
	version := topo.Version()
	assert.False(t, topo.DestroyGate(10))
	assert.Equal(t, version, topo.Version(), "no change, no revision bump")
	assert.False(t, topo.DestroyGate(999), "an unknown id changes nothing")
}

// GatesInSector is what a worker seeds its shootable endpoints from: only gates
// touching that sector, and only live ones.
func TestUnit_Topology_GatesInSector(t *testing.T) {
	t.Parallel()

	topo, _, _ := fixture()

	one := topo.GatesInSector(1)
	assert.Len(t, one, 1)
	assert.EqualValues(t, 10, one[0].ID)

	two := topo.GatesInSector(2)
	assert.Len(t, two, 2, "sector 2 sits between both gates")

	assert.Empty(t, topo.GatesInSector(42), "a sector with no gates")

	topo.DestroyGate(10)
	assert.Empty(t, topo.GatesInSector(1), "a destroyed gate is not an endpoint any more")
	assert.Len(t, topo.GatesInSector(2), 1)
}

// Gates returns a copy: DestroyGate rebuilds the backing slice, so a caller
// holding the previous result must not see it mutate under them.
func TestUnit_Topology_Gates_ReturnsCopy(t *testing.T) {
	t.Parallel()

	topo, _, _ := fixture()
	held := topo.Gates()
	require.Len(t, held, 2)

	topo.DestroyGate(10)
	assert.Len(t, held, 2, "the earlier snapshot is untouched")
	assert.Len(t, topo.Gates(), 1)
}
