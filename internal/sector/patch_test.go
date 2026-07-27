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

// TestUnit_BuildPatch_DetectsMiningTargetChange guards the «Бурить/Стоп» toggle
// (phase 10.3.21): a parked ship that only arms or clears its MiningTarget —
// no Pos/Vel/Energy change — must still be broadcast, or the SPA never learns
// the mining state flipped.
func TestUnit_BuildPatch_DetectsMiningTargetChange(t *testing.T) {
	t.Parallel()

	a7 := domain.AsteroidID(7)

	// Arm: nil -> set.
	armed := buildPatch(
		map[domain.ShipID]domain.Ship{1: {ID: 1, Pos: domain.Vec2{X: 1, Y: 1}}},
		map[domain.ShipID]domain.Ship{1: {ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, MiningTarget: &a7}},
		1,
	)
	require.Len(t, armed.Updated, 1, "arming mining must surface the ship")

	// Stop: set -> nil.
	stopped := buildPatch(
		map[domain.ShipID]domain.Ship{1: {ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, MiningTarget: &a7}},
		map[domain.ShipID]domain.Ship{1: {ID: 1, Pos: domain.Vec2{X: 1, Y: 1}}},
		1,
	)
	require.Len(t, stopped.Updated, 1, "clearing mining must surface the ship")
}

// TestUnit_BuildPatch_DetectsAccessToggle guards the «Вход открыт/закрыт» button
// (TASK-126): a docked ship whose IsOpen flips — no Pos/Vel/HP change — must
// still be broadcast, or the SPA keeps the stale isOpen (button text and its
// data-active highlight) until a reload and the next click re-sends open=true.
func TestUnit_BuildPatch_DetectsAccessToggle(t *testing.T) {
	t.Parallel()

	closed := domain.Ship{ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, HP: 100}
	open := closed
	open.IsOpen = true

	assert.False(t, shipEqual(closed, open), "IsOpen must be an observable field")

	opened := buildPatch(
		map[domain.ShipID]domain.Ship{1: closed},
		map[domain.ShipID]domain.Ship{1: open},
		1,
	)
	require.Len(t, opened.Updated, 1, "opening access must surface the ship")
	assert.True(t, opened.Updated[0].IsOpen, "the patch must carry the NEW isOpen, not the stale one")

	reclosed := buildPatch(
		map[domain.ShipID]domain.Ship{1: open},
		map[domain.ShipID]domain.Ship{1: closed},
		1,
	)
	require.Len(t, reclosed.Updated, 1, "closing access must surface the ship")
	assert.False(t, reclosed.Updated[0].IsOpen, "the patch must carry the NEW isOpen, not the stale one")
}

// TestUnit_BuildPatch_DetectsStealthToggle guards the «СТЕЛС» chip (TASK-126):
// the client reads IsHidden off its own ship, so a cloak state change has to
// travel in the delta instead of waiting for the next welcome snapshot.
func TestUnit_BuildPatch_DetectsStealthToggle(t *testing.T) {
	t.Parallel()

	visible := domain.Ship{ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, HP: 100}
	cloaked := visible
	cloaked.IsHidden = true

	assert.False(t, shipEqual(visible, cloaked), "IsHidden must be an observable field")

	hidden := buildPatch(
		map[domain.ShipID]domain.Ship{1: visible},
		map[domain.ShipID]domain.Ship{1: cloaked},
		1,
	)
	require.Len(t, hidden.Updated, 1, "cloaking must surface the ship")
	assert.True(t, hidden.Updated[0].IsHidden, "the patch must carry the NEW isHidden, not the stale one")

	surfaced := buildPatch(
		map[domain.ShipID]domain.Ship{1: cloaked},
		map[domain.ShipID]domain.Ship{1: visible},
		1,
	)
	require.Len(t, surfaced.Updated, 1, "decloaking must surface the ship")
	assert.False(t, surfaced.Updated[0].IsHidden, "the patch must carry the NEW isHidden, not the stale one")
}
