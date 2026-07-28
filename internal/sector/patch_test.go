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

// TestUnit_BuildPatch_DetectsFinalTargetClear reproduces the NPC-miner path
// (TASK-143): applyMovement drops Target and zeroes Vel on arrival, that state
// reaches the subscriber, and only on the NEXT tick applyMine clears
// FinalTarget on its own — no other observable field moves. Without FinalTarget
// in shipEqual the client keeps a stale «МАРШРУТ» line until the next welcome
// snapshot.
func TestUnit_BuildPatch_DetectsFinalTargetClear(t *testing.T) {
	t.Parallel()

	course := domain.Course{Sector: 7, Pos: domain.Vec2{X: 100, Y: 200}}
	arrived := domain.Ship{ID: 1, Pos: domain.Vec2{X: 100, Y: 200}, HP: 100, FinalTarget: &course}
	mining := arrived
	mining.FinalTarget = nil

	assert.False(t, shipEqual(&arrived, &mining), "FinalTarget must be an observable field")

	p := buildPatch(
		map[domain.ShipID]domain.Ship{1: arrived},
		map[domain.ShipID]domain.Ship{1: mining},
		1,
	)
	require.Len(t, p.Updated, 1, "clearing the autopilot course alone must surface the ship")
	assert.Nil(t, p.Updated[0].FinalTarget, "the patch must carry the NEW (cleared) course, not the stale one")
}

// TestUnit_BuildPatch_DetectsFinalTargetChange covers the remaining course
// transitions the SPA renders as «МАРШРУТ»: arming a course, retargeting it to
// another sector/position, and swapping the approach static. Each block asserts
// the VALUE that travels in the patch, not just that something was sent —
// asserting on len(Updated) alone still passes when the stale copy is shipped.
func TestUnit_BuildPatch_DetectsFinalTargetChange(t *testing.T) {
	t.Parallel()

	parked := domain.Ship{ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, HP: 100}
	toSector7 := domain.Course{Sector: 7, Pos: domain.Vec2{X: 100, Y: 200}}
	toSector9 := domain.Course{Sector: 9, Pos: domain.Vec2{X: 100, Y: 200}}

	// Arm: nil -> set.
	armedShip := parked
	armedShip.FinalTarget = &toSector7
	armed := buildPatch(
		map[domain.ShipID]domain.Ship{1: parked},
		map[domain.ShipID]domain.Ship{1: armedShip},
		1,
	)
	require.Len(t, armed.Updated, 1, "setting a course must surface the ship")
	require.NotNil(t, armed.Updated[0].FinalTarget)
	assert.Equal(t, toSector7, *armed.Updated[0].FinalTarget, "the patch must carry the NEW course")

	// Retarget: another destination sector.
	retargetedShip := parked
	retargetedShip.FinalTarget = &toSector9
	retargeted := buildPatch(
		map[domain.ShipID]domain.Ship{1: armedShip},
		map[domain.ShipID]domain.Ship{1: retargetedShip},
		1,
	)
	require.Len(t, retargeted.Updated, 1, "changing the destination sector must surface the ship")
	require.NotNil(t, retargeted.Updated[0].FinalTarget)
	assert.Equal(t, domain.SectorID(9), retargeted.Updated[0].FinalTarget.Sector,
		"the patch must carry the NEW destination sector")

	// Swap the approach static: same sector + position, different Approach value.
	station5 := domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	station6 := domain.EntityRef{Kind: domain.EntityKindStation, ID: 6}
	approach5 := parked
	approach5.FinalTarget = &domain.Course{Sector: 9, Pos: toSector9.Pos, Approach: &station5}
	approach6 := parked
	approach6.FinalTarget = &domain.Course{Sector: 9, Pos: toSector9.Pos, Approach: &station6}
	swapped := buildPatch(
		map[domain.ShipID]domain.Ship{1: approach5},
		map[domain.ShipID]domain.Ship{1: approach6},
		1,
	)
	require.Len(t, swapped.Updated, 1, "changing the approach static must surface the ship")
	require.NotNil(t, swapped.Updated[0].FinalTarget)
	require.NotNil(t, swapped.Updated[0].FinalTarget.Approach)
	assert.Equal(t, station6, *swapped.Updated[0].FinalTarget.Approach,
		"the patch must carry the NEW approach ref")
}

// TestUnit_BuildPatch_FinalTargetApproachRecreatedIsNoChurn is the anti-churn
// guard for TASK-143: shipsMapSubset deep-copies FinalTarget through
// cloneCourse, which allocates a fresh Course AND a fresh Approach every tick.
// A naive `*a.FinalTarget == *b.FinalTarget` compares Approach by pointer
// identity and would therefore report "changed" every tick for every ship on an
// approach course — a delta per such ship per subscriber per tick.
func TestUnit_BuildPatch_FinalTargetApproachRecreatedIsNoChurn(t *testing.T) {
	t.Parallel()

	approach := domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	live := domain.Ship{
		ID: 1, Pos: domain.Vec2{X: 1, Y: 1}, HP: 100,
		FinalTarget: &domain.Course{Sector: 3, Pos: domain.Vec2{X: 10, Y: 10}, Approach: &approach},
	}
	src := map[domain.ShipID]*domain.Ship{1: &live}
	ids := map[domain.ShipID]struct{}{1: {}}

	// Two consecutive AOI snapshots of a ship nothing happened to.
	prev := shipsMapSubset(src, ids)
	curr := shipsMapSubset(src, ids)

	require.NotSame(t, prev[1].FinalTarget, curr[1].FinalTarget,
		"fixture must exercise distinct Course allocations")
	require.NotSame(t, prev[1].FinalTarget.Approach, curr[1].FinalTarget.Approach,
		"fixture must exercise distinct Approach allocations")

	p := buildPatch(prev, curr, 1)
	assert.True(t, p.IsEmpty(), "an unchanged approach course must not emit a delta every tick")
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

	assert.False(t, shipEqual(&closed, &open), "IsOpen must be an observable field")

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

	assert.False(t, shipEqual(&visible, &cloaked), "IsHidden must be an observable field")

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
