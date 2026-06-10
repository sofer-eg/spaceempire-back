package world_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
