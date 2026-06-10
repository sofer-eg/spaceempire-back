package sector

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

func TestUnit_BuildPatch_AddedUpdatedRemoved(t *testing.T) {
	t.Parallel()

	prev := map[domain.ShipID]domain.Ship{
		1: {ID: 1, PlayerID: 1, Pos: domain.Vec2{X: 0, Y: 0}, HP: 100},
		2: {ID: 2, PlayerID: 1, Pos: domain.Vec2{X: 5, Y: 5}, HP: 100},
		3: {ID: 3, PlayerID: 2, Pos: domain.Vec2{X: 7, Y: 7}, HP: 100},
	}
	curr := map[domain.ShipID]domain.Ship{
		1: {ID: 1, PlayerID: 1, Pos: domain.Vec2{X: 1, Y: 0}, HP: 100}, // moved
		2: {ID: 2, PlayerID: 1, Pos: domain.Vec2{X: 5, Y: 5}, HP: 100}, // unchanged
		4: {ID: 4, PlayerID: 2, Pos: domain.Vec2{X: 9, Y: 9}, HP: 100}, // added
		// 3 removed
	}

	p := buildPatch(prev, curr, 42)

	require.Equal(t, uint64(42), p.Tick)
	require.Len(t, p.Added, 1)
	assert.Equal(t, domain.ShipID(4), p.Added[0].ID)
	require.Len(t, p.Updated, 1)
	assert.Equal(t, domain.ShipID(1), p.Updated[0].ID)
	require.Len(t, p.Removed, 1)
	assert.Equal(t, domain.ShipID(3), p.Removed[0])
}

func TestUnit_BuildPatch_EmptyWhenNoChange(t *testing.T) {
	t.Parallel()

	curr := map[domain.ShipID]domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, HP: 100},
	}
	p := buildPatch(curr, curr, 1)
	assert.True(t, p.IsEmpty())
}

func TestUnit_BuildPatch_FirstDeliveryIsAllAdded(t *testing.T) {
	t.Parallel()

	curr := map[domain.ShipID]domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 0, Y: 0}},
		2: {ID: 2, Pos: domain.Vec2{X: 1, Y: 1}},
	}
	p := buildPatch(nil, curr, 1)
	require.Len(t, p.Added, 2)
	ids := []domain.ShipID{p.Added[0].ID, p.Added[1].ID}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	assert.Equal(t, []domain.ShipID{1, 2}, ids)
	assert.Empty(t, p.Updated)
	assert.Empty(t, p.Removed)
}

func TestUnit_BuildPatch_DetectsTargetChange(t *testing.T) {
	t.Parallel()

	t1 := domain.Vec2{X: 10, Y: 10}
	t2 := domain.Vec2{X: 20, Y: 20}
	prev := map[domain.ShipID]domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 0, Y: 0}, Target: &t1},
	}
	curr := map[domain.ShipID]domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 0, Y: 0}, Target: &t2},
	}
	p := buildPatch(prev, curr, 1)
	require.Len(t, p.Updated, 1)
}

func TestUnit_BuildPatch_DetectsTargetClear(t *testing.T) {
	t.Parallel()

	t1 := domain.Vec2{X: 10, Y: 10}
	prev := map[domain.ShipID]domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 0, Y: 0}, Target: &t1},
	}
	curr := map[domain.ShipID]domain.Ship{
		1: {ID: 1, Pos: domain.Vec2{X: 0, Y: 0}, Target: nil},
	}
	p := buildPatch(prev, curr, 1)
	require.Len(t, p.Updated, 1)
}
