package sector_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// fakeGateRepo records the gates whose destruction was persisted.
type fakeGateRepo struct {
	mu       sync.Mutex
	marked   []domain.GateID
	failWith error
}

func (f *fakeGateRepo) MarkDestroyed(_ context.Context, id domain.GateID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.marked = append(f.marked, id)
	return nil
}

func (f *fakeGateRepo) destroyed() []domain.GateID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.GateID(nil), f.marked...)
}

func gateRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindGate, ID: id}
}

// gateTopology is a three-sector chain 1 — 2 — 3 whose gates carry combat state.
// Sector 1's side of gate 10 sits at (100,0), which is where the attacker parks.
// The chain matters for the graph assertions: severing gate 10 must leave sector 3
// unreachable from sector 1, not merely change the hop count.
func gateTopology(hp, shield int) *world.Topology {
	sectors := []domain.Sector{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"}}
	gates := []domain.Gate{
		{
			ID:      10,
			SectorA: 1, PosA: domain.Vec2{X: 100, Y: 0},
			SectorB: 2, PosB: domain.Vec2{X: -100, Y: 0},
			HP: hp, Shield: shield, MaxShield: shield, ShieldRecharge: 5,
		},
		{
			ID:      11,
			SectorA: 2, PosA: domain.Vec2{X: 200, Y: 0},
			SectorB: 3, PosB: domain.Vec2{X: -200, Y: 0},
			HP: hp, Shield: shield, MaxShield: shield, ShieldRecharge: 5,
		},
	}
	return world.New(sectors, gates)
}

// gateCombatWorker owns sectors 1 and 2 (so one worker can be observed on both
// sides of gate 10) with the given ships in sector 1.
func gateCombatWorker(t *testing.T, topo *world.Topology, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append(opts, sector.WithHandoff(topo, &fakeBus{}))
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000, GateRange: 50},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{1: ships, 2: nil},
		opts...,
	)
}

// TestUnit_GateCombat_EndpointIsShootableInEverySector is AC#1/AC#2: a gate now
// carries combat state, and each linked sector holds its own endpoint — the two
// sides are separate DestructibleStatics because one HP pool cannot have two
// writers (one writer per sector).
func TestUnit_GateCombat_EndpointIsShootableInEverySector(t *testing.T) {
	t.Parallel()
	w := gateCombatWorker(t, gateTopology(5000, 1000), nil)
	// Snapshots are published by a tick; nothing is shooting, so one idle tick
	// just makes the seeded state observable.
	w.Tick(context.Background())

	a, ok := findDestructible(w.Snapshot(1), gateRef(10))
	require.True(t, ok, "sector 1 holds its side of gate 10")
	assert.Equal(t, domain.Vec2{X: 100, Y: 0}, a.Pos, "at this sector's endpoint")
	assert.Equal(t, 5000, a.HP)
	assert.Equal(t, 1000, a.MaxShield)

	b, ok := findDestructible(w.Snapshot(2), gateRef(10))
	require.True(t, ok, "sector 2 holds the other side of the same gate")
	assert.Equal(t, domain.Vec2{X: -100, Y: 0}, b.Pos)

	// Gate 11 only touches sectors 2 and 3, so sector 1 must not carry it.
	_, ok = findDestructible(w.Snapshot(1), gateRef(11))
	assert.False(t, ok, "a gate that does not touch this sector is not registered here")
}

// AC#1: the gate's shield recharges per tick, like every other destructible
// static (port of TO_ObjectShieldCharge).
func TestUnit_GateCombat_ShieldRecharges(t *testing.T) {
	t.Parallel()
	topo := gateTopology(5000, 1000)
	// Start the endpoint below its cap by shooting it once, then let it charge.
	attacker := staticAttacker(1, 7, domain.Vec2{X: 100, Y: 0}, 300, gateRef(10))
	w := gateCombatWorker(t, topo, []domain.Ship{attacker})

	w.Tick(context.Background())
	hit, ok := findDestructible(w.Snapshot(1), gateRef(10))
	require.True(t, ok)
	require.Less(t, hit.Shield, 1000, "the shot landed on the shield")

	shieldAfterHit := hit.Shield
	// Stop shooting so only the recharge moves the number.
	require.NoError(t, w.Send(1, sector.CeaseFireCommand{PlayerID: 7, ShipID: 1}))
	w.Tick(context.Background())

	charged, _ := findDestructible(w.Snapshot(1), gateRef(10))
	assert.Greater(t, charged.Shield, shieldAfterHit, "the gate's shield recharges like any static")
}

// AC#2: anyone can shoot a gate. Owned statics go through the hostility oracle
// (friendly/neutral objects are invulnerable), but a gate has no owner, so that
// oracle would answer "not hostile" and leave gates invulnerable — the very state
// TASK-110 removes. The worker here has NO hostility wired (the default refuses
// everything), so a landed shot proves the gate exception.
func TestUnit_GateCombat_AttackableWithoutHostility(t *testing.T) {
	t.Parallel()
	attacker := staticAttacker(1, 7, domain.Vec2{X: 100, Y: 0}, 250, gateRef(10))
	w := gateCombatWorker(t, gateTopology(5000, 1000), []domain.Ship{attacker})

	w.Tick(context.Background())

	d, ok := findDestructible(w.Snapshot(1), gateRef(10))
	require.True(t, ok)
	assert.Equal(t, 750, d.Shield, "1000 - 250: the shot landed with no hostility oracle at all")
	ships := snapByID(w)
	assert.NotNil(t, ships[1].AttackTarget, "the engagement is not dropped as invulnerable")
}

// TestUnit_GateCombat_KillSeversTheLink is AC#3, the whole point of the task:
// destroying a gate removes it from this sector, from the world graph (so the
// router stops routing through it and the jump is refused), and — via the
// per-tick sweep — from the twin sector too, which a different writer owns.
func TestUnit_GateCombat_KillSeversTheLink(t *testing.T) {
	t.Parallel()
	topo := gateTopology(200, 0) // one shot kills
	repo := &fakeGateRepo{}
	attacker := staticAttacker(1, 7, domain.Vec2{X: 100, Y: 0}, 500, gateRef(10))
	w := gateCombatWorker(t, topo, []domain.Ship{attacker}, sector.WithGates(repo))

	router := world.NewPathRouter(topo, nil)
	// Warm the router's cache on the intact graph: sector 3 is two hops away
	// through gate 10. A cache that survived the sever would keep answering this.
	hops, ok := router.Hops(1, 3)
	require.True(t, ok)
	require.Equal(t, 2, hops)

	w.Tick(context.Background())

	// Gone from the killing sector...
	_, ok = findDestructible(w.Snapshot(1), gateRef(10))
	assert.False(t, ok, "the destroyed endpoint leaves the sector")
	// ...and from the world graph, so no route and no jump can use it.
	assert.Nil(t, topo.Gate(10), "a destroyed gate is out of the topology")
	assert.Nil(t, topo.GateBetween(1, 2), "the link is severed")
	_, ok = router.NextSector(1, 3)
	assert.False(t, ok, "sector 3 is unreachable: the router re-ran BFS on the severed graph")
	_, ok = router.Hops(1, 2)
	assert.False(t, ok)
	assert.Equal(t, []domain.GateID{10}, repo.destroyed(), "the wreck is persisted")

	// The twin sector's endpoint belongs to its own writer, so it goes on that
	// sector's next tick rather than inside this kill.
	w.Tick(context.Background())
	_, ok = findDestructible(w.Snapshot(2), gateRef(10))
	assert.False(t, ok, "the other side reconciles against the shared topology")

	// Gate 11 is untouched: severing one link must not disturb the rest.
	assert.NotNil(t, topo.Gate(11))
	require.True(t, func() bool { _, ok := router.Hops(2, 3); return ok }(), "2 — 3 still connected")
}

// A jump through a destroyed gate is refused. The gate is simply not in the
// topology any more, so JumpCommand's own lookup answers ErrInvalidGate — there is
// no separate "is this wreckage" branch to forget.
func TestUnit_GateCombat_JumpThroughDestroyedGateRefused(t *testing.T) {
	t.Parallel()
	topo := gateTopology(200, 0)
	attacker := staticAttacker(1, 7, domain.Vec2{X: 100, Y: 0}, 500, gateRef(10))
	// A second ship parked at the gate, ready to jump.
	jumper := domain.Ship{ID: 2, PlayerID: 8, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}}
	w := gateCombatWorker(t, topo, []domain.Ship{attacker, jumper}, sector.WithGates(&fakeGateRepo{}))

	w.Tick(context.Background())
	require.Nil(t, topo.Gate(10), "the gate died this tick")

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(1, sector.JumpCommand{PlayerID: 8, ShipID: 2, GateID: 10, Reply: reply}))
	w.Tick(context.Background())
	require.ErrorIs(t, (<-reply).Err, sector.ErrInvalidGate)
	assert.Len(t, w.Snapshot(1).Ships, 2, "nobody jumped anywhere")
}

// Without a gate repo the sever is RAM-only: the link is gone for this run and
// comes back on the next restart. Same contract as the tower/satellite/jammer
// persists — a missing repo never blocks the kill.
func TestUnit_GateCombat_NoRepoStillSevers(t *testing.T) {
	t.Parallel()
	topo := gateTopology(200, 0)
	attacker := staticAttacker(1, 7, domain.Vec2{X: 100, Y: 0}, 500, gateRef(10))
	w := gateCombatWorker(t, topo, []domain.Ship{attacker})

	w.Tick(context.Background())

	assert.Nil(t, topo.Gate(10), "the live graph is severed")
	_, ok := findDestructible(w.Snapshot(1), gateRef(10))
	assert.False(t, ok)
}

// A failing persist is logged, not fatal: the link stays severed in RAM. The kill
// must not be rolled back — the object is already gone from the sector.
func TestUnit_GateCombat_PersistFailureKeepsTheSever(t *testing.T) {
	t.Parallel()
	topo := gateTopology(200, 0)
	repo := &fakeGateRepo{failWith: assert.AnError}
	attacker := staticAttacker(1, 7, domain.Vec2{X: 100, Y: 0}, 500, gateRef(10))
	w := gateCombatWorker(t, topo, []domain.Ship{attacker}, sector.WithGates(repo))

	w.Tick(context.Background())

	assert.Nil(t, topo.Gate(10))
	assert.Empty(t, repo.destroyed(), "nothing was recorded")
	_, ok := findDestructible(w.Snapshot(1), gateRef(10))
	assert.False(t, ok, "the sector still lost the endpoint")
}

// Guard on the public target set: a gate is a weapon target now, which is what
// lets the launch handlers accept one (TASK-111).
func TestUnit_GateCombat_IsStaticTargetKind(t *testing.T) {
	t.Parallel()
	assert.True(t, sector.IsStaticTargetKind(domain.EntityKindGate))
}

// A worker with no topology (most unit tests) registers no gate endpoints and the
// sweep is a no-op — gates simply do not exist for it.
func TestUnit_GateCombat_NoTopologyNoEndpoints(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{1: nil})
	w.Tick(context.Background())
	assert.Empty(t, w.Snapshot(1).Destructibles)
}
