package sector_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeTorpedoRepo is an in-memory TorpedoRepo for the sector tick tests. It
// assigns DB-style ids on Create and mirrors the live set so a test can assert
// both persistence calls and what survives in flight. All access is from the
// single worker tick goroutine the test drives synchronously (Send → Tick), so
// no locking is needed.
type fakeTorpedoRepo struct {
	next                      int64
	live                      map[domain.TorpedoID]domain.Torpedo
	creates, deletes, batches int
}

func newFakeTorpedoRepo() *fakeTorpedoRepo {
	return &fakeTorpedoRepo{live: map[domain.TorpedoID]domain.Torpedo{}}
}

func (r *fakeTorpedoRepo) Create(_ context.Context, t domain.Torpedo) (domain.TorpedoID, error) {
	r.next++
	id := domain.TorpedoID(r.next)
	t.ID = id
	r.live[id] = t
	r.creates++
	return id, nil
}

func (r *fakeTorpedoRepo) BatchUpdate(_ context.Context, ts []domain.Torpedo) error {
	r.batches++
	for _, t := range ts {
		if _, ok := r.live[t.ID]; ok {
			r.live[t.ID] = t
		}
	}
	return nil
}

func (r *fakeTorpedoRepo) Delete(_ context.Context, id domain.TorpedoID) error {
	r.deletes++
	delete(r.live, id)
	return nil
}

func (r *fakeTorpedoRepo) liveCount() int { return len(r.live) }

// newTorpedoWorker builds a single-sector worker with torpedo persistence wired
// to repo, plus any extra options (e.g. a destructible station). The launch
// transaction runs through an unlimited fakeOrdnance that creates through the
// same repo (TASK-147: the row and the ammunition debit are one transaction), so
// repo.creates still counts exactly the launches.
func newTorpedoWorker(t *testing.T, cfg sector.Config, clk clock.Clock, repo *fakeTorpedoRepo, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	ord := unlimitedOrdnance()
	ord.torpedoRepo = repo
	opts = append([]sector.Option{sector.WithTorpedos(repo, nil), sector.WithOrdnance(ord)}, opts...)
	return sector.NewWorker(0, cfg, clk, nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships}, opts...)
}

// TestUnit_LaunchTorpedo_SpawnsAndPersists: a successful launch builds the
// torpedo, persists it immediately (Create), and echoes its DB-assigned id.
func TestUnit_LaunchTorpedo_SpawnsAndPersists(t *testing.T) {
	t.Parallel()
	repo := newFakeTorpedoRepo()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := torpedoShip(2, 200, domain.Vec2{X: 5000, Y: 0}) // far: no detonation on the launch tick
	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{a, b})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, res.Err)
	require.NotZero(t, res.TorpedoID, "launch must echo the DB-assigned torpedo id")
	require.Equal(t, 1, repo.creates, "launch persists the torpedo immediately")
	require.Equal(t, 1, repo.liveCount(), "the torpedo is in flight")
}

// TestUnit_Torpedo_DetonatesOnTarget: a torpedo launched at a ship homes over a
// series of ticks and, at dist<=HitRadius, detonates — removed from the live set
// and the DB (ЧТЗ AC-4) — and now deals splash damage to the target caught in
// the blast (ЧТЗ AC-6). This is the .4→.5 guard: the assertion was previously
// "target unharmed"; it is inverted here to "target took splash damage".
func TestUnit_Torpedo_DetonatesOnTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeTorpedoRepo()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := torpedoShip(2, 200, domain.Vec2{X: 200, Y: 0})
	b.HP = 5000 // tough enough to survive the blast so the splash damage is readable
	b.MaxHP = 5000
	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{a, b})

	// Launch tick: the torpedo is spawned and takes its first homing step but
	// is nowhere near the 200-unit-away target yet.
	require.NoError(t, sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}).Err)
	require.Equal(t, 1, repo.liveCount(), "torpedo is in flight after launch, not instantly detonated")

	for i := 0; i < 15 && repo.liveCount() > 0; i++ {
		w.Tick(ctx)
	}
	require.Zero(t, repo.liveCount(), "torpedo detonates on the target and is removed")
	require.GreaterOrEqual(t, repo.deletes, 1, "detonation persists the removal (Delete)")

	// The target sat inside SplashRadius at detonation, so it lost shield then HP.
	var target domain.Ship
	for _, s := range w.Snapshot(testSector).Ships {
		if s.ID == 2 {
			target = s
		}
	}
	require.Equal(t, domain.ShipID(2), target.ID, "target survives the blast (5000 HP) and stays in the sector")
	require.Less(t, target.HP, 5000, "detonation deals splash damage to the target in radius")
	require.Zero(t, target.Shield, "splash drains the target's shield first")
}

// TestUnit_Torpedo_SplashReapsKilledStatic: a static dropped to HP<=0 by torpedo
// splash is reaped inline (TASK-100.3.5.5) — there is no static sweep, so without
// the inline reap it would zombie on. A fragile tower in the blast is removed
// from the combat set + rendered layout, persist-deleted, and publishes
// entity_killed; a tough station caught in the same blast survives and is only
// damaged (dirtied, not reaped).
func TestUnit_Torpedo_SplashReapsKilledStatic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := bus.NewInMemory(8)
	t.Cleanup(b.Close)
	got := make(chan sector.EntityKilledEvent, 1)
	require.NoError(t, b.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			got <- ev
		}
	}))

	repo := newFakeTorpedoRepo()
	towerRepo := &fakeTowerRepo{}

	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	target := torpedoShip(2, 200, domain.Vec2{X: 200, Y: 0})
	target.HP = 100000 // survives the blast so the torpedo always has a live target to home onto
	target.MaxHP = 100000

	// A fragile tower in the blast: 50 HP < class-3 splash (600 dmg) → reaped.
	tower := domain.LaserTower{ID: 5, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 200, Y: 20}, HP: 50, Built: true}
	// A tough station in the same blast: survives, must only be dirtied.
	station := stationStatic(9, ownerPtr(7), domain.Vec2{X: 200, Y: 40}, 100000, 0, 0, 0)
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {
		LaserTowers: []domain.LaserTower{tower},
		Stations:    []domain.Station{station},
	}}

	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{a, target},
		sector.WithStatics(statics),
		sector.WithTowerPersistence(towerRepo),
		sector.WithHandoff(nil, b),
	)

	require.NoError(t, sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}).Err)
	for i := 0; i < 15 && repo.liveCount() > 0; i++ {
		w.Tick(ctx)
	}
	require.Zero(t, repo.liveCount(), "torpedo detonated")

	snap := w.Snapshot(testSector)
	_, towerAlive := findDestructible(snap, domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 5})
	require.False(t, towerAlive, "splash-killed tower reaped from the combat set")
	require.Empty(t, snap.Statics.LaserTowers, "splash-killed tower gone from the rendered layout")
	require.Equal(t, []domain.LaserTowerID{5}, towerRepo.deleted, "tower destruction persisted (delete)")

	st, ok := findDestructible(snap, stationRef(9))
	require.True(t, ok, "tough station survives the same blast, not reaped")
	require.Less(t, st.HP, 100000, "surviving static still took splash damage (dirtied, not reaped)")

	select {
	case ev := <-got:
		require.Equal(t, domain.EntityKindLaserTower, ev.Victim.Kind, "entity_killed carries the splash-killed tower")
		require.Equal(t, int64(5), ev.Victim.ID)
	case <-time.After(time.Second):
		t.Fatal("entity_killed for the splash-killed tower was not published")
	}
}

// TestUnit_Torpedo_ExpiresOnTTL: a torpedo that cannot reach its target dies
// when its TTL elapses — removed without ever damaging the target (ЧТЗ AC-8).
func TestUnit_Torpedo_ExpiresOnTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	repo := newFakeTorpedoRepo()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := torpedoShip(2, 200, domain.Vec2{X: 1_000_000, Y: 0}) // unreachable
	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 5_000_000},
		clk, repo, []domain.Ship{a, b})

	require.NoError(t, sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2, // class-2 TTL is 30s
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}).Err)
	require.Equal(t, 1, repo.liveCount())

	for i := 0; i < 10 && repo.liveCount() > 0; i++ {
		clk.Advance(5 * time.Second) // 10×5s = 50s > 30s TTL
		w.Tick(ctx)
	}
	require.Zero(t, repo.liveCount(), "torpedo expires at TTL and is removed")
	require.GreaterOrEqual(t, repo.deletes, 1, "expiry persists the removal (Delete)")

	// No damage on a TTL expiry: the unreachable target keeps full HP/shield.
	for _, s := range w.Snapshot(testSector).Ships {
		if s.ID == 2 {
			require.Equal(t, s.MaxHP, s.HP, "TTL expiry deals no damage")
			require.Equal(t, s.MaxShield, s.Shield, "TTL expiry deals no shield damage")
		}
	}
}

// TestUnit_Torpedo_DiesOnOwnerLoss: once the launching ship dies, its in-flight
// torpedo self-destructs — removed from the live set and the DB (ЧТЗ AC-3,
// FR-009). The owner is killed by a hostile laser between ticks.
func TestUnit_Torpedo_DiesOnOwnerLoss(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeTorpedoRepo()

	owner := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	owner.HP = 150
	owner.Shield = 0
	owner.MaxShield = 0

	killer := torpedoShip(2, 200, domain.Vec2{X: 40, Y: 0})
	killer.Equipment = nil // the killer needs no launcher
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 100
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 1}

	// Far-away target so the torpedo cannot detonate before the owner dies.
	target := torpedoShip(3, 300, domain.Vec2{X: 5000, Y: 0})

	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{owner, killer, target})

	// Launch tick: torpedo spawned; first laser hit takes the owner 150→50.
	require.NoError(t, sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 3},
	}).Err)
	require.Equal(t, 1, repo.liveCount(), "torpedo alive while the owner survives")

	// Second tick: owner 50→dead, torpedo self-destructs.
	w.Tick(ctx)
	require.Zero(t, repo.liveCount(), "torpedo dies with its owner")
	require.GreaterOrEqual(t, repo.deletes, 1, "owner-loss persists the removal (Delete)")
}

// TestUnit_Torpedo_EndToEnd_LaunchHomingSplashFriendlyFire is the consolidated
// acceptance scenario for TASK-100.3.5.9: it stitches the torpedo main thread —
// launch → ammunition/energy debit → homing to a MOVING target → detonation with
// indiscriminate splash → removal — into one readable narrative, the way the
// system runs it (real LaunchTorpedoCommand + real ticks), rather than the
// isolated unit slices around it.
//
// It closes the one gap those unit slices leave at the sector level: splash
// friendly-fire that hits the FIRING PLAYER'S OWN ship. The combat-package unit
// (ApplyDamageInRadius_FriendlyFireHitsOwnShips) proves the primitive is
// indiscriminate; here a same-player ship caught in the blast really loses HP
// through the full launch→tick→detonate pipeline (ЧТЗ AC-6, R-02).
//
// Coverage threaded through this single test:
//   - AC-1: the ships carry up_torpedo_launcher, so the launch passes the gate.
//   - AC-3: the launch debits the launcher's action energy from the firing ship.
//   - AC-4: the torpedo homes over a series of ticks onto a moving target and
//     detonates within HitRadius (the target really moved while being chased).
//   - AC-6: the detonation deals splash to >1 target INCLUDING the firing
//     player's own ship — friendly-fire, no owner/ally exclusion.
//   - FR-009/AC-4: the spent torpedo is removed from the live set and persist-
//     Deleted (the detonation's recorded end-of-life). The Hit-impact delta and
//     its AOI delivery are covered by TestUnit_Worker_Subscribe_DeliversTorpedos.
//
// Ammunition (gt23/gt24) is debited in the HTTP handler, not the worker, so it
// is exercised by the api launch_torpedo tests (AC-2); the energy debit is the
// in-worker consumable this end-to-end asserts.
func TestUnit_Torpedo_EndToEnd_LaunchHomingSplashFriendlyFire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeTorpedoRepo()

	// Firing ship: far from the blast zone so it survives to detonation, with
	// enough energy for exactly the launch.
	owner := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	owner.Energy = 200
	owner.MaxEnergy = 1000

	// A genuinely moving target: Target==nil + a non-zero Vel makes moveShip
	// coast it each tick, so the torpedo must keep steering onto a drifting mark.
	const targetStartY = 0
	target := torpedoShip(2, 999, domain.Vec2{X: 250, Y: targetStartY})
	target.HP, target.MaxHP = 5000, 5000 // survives the blast so the splash is readable
	target.Vel = domain.Vec2{X: 0, Y: 2}

	// A friendly ship of the FIRING player parked in the blast zone — the
	// friendly-fire victim (AC-6).
	friendly := torpedoShip(4, 100, domain.Vec2{X: 250, Y: 15})
	friendly.HP, friendly.MaxHP = 5000, 5000

	// A third-party bystander also in the blast zone — proves the blast catches
	// more than the homing target (≥2 splash victims).
	bystander := torpedoShip(5, 555, domain.Vec2{X: 255, Y: 30})
	bystander.HP, bystander.MaxHP = 5000, 5000

	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{owner, target, friendly, bystander})

	// Launch (real LaunchTorpedoCommand path): class 3, with the action-energy
	// cost the launcher catalog charges.
	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3, EnergyCost: 100,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, res.Err)
	require.NotZero(t, res.TorpedoID, "launch echoes the DB-assigned id")
	require.Equal(t, 1, repo.creates, "launch persists the torpedo immediately (Create)")
	require.Equal(t, 1, repo.liveCount(), "in flight after launch, not instantly detonated")
	require.Equal(t, 100, shipEnergyByID(t, w, 1), "launch debits the action energy (200-100) — AC-3")

	// Homing: tick until the torpedo reaches the drifting target and detonates.
	for i := 0; i < 30 && repo.liveCount() > 0; i++ {
		w.Tick(ctx)
	}
	require.Zero(t, repo.liveCount(), "torpedo detonates on the moving target and is removed — AC-4")
	require.GreaterOrEqual(t, repo.deletes, 1, "detonation persists the removal (Delete) — FR-009")

	shipByID := func(id domain.ShipID) (domain.Ship, bool) {
		for _, s := range w.Snapshot(testSector).Ships {
			if s.ID == id {
				return s, true
			}
		}
		return domain.Ship{}, false
	}

	// The target really moved while it was being chased (AC-4 "движущаяся цель").
	tgt, ok := shipByID(2)
	require.True(t, ok, "the tough target survives the blast and stays in the sector")
	require.Greater(t, tgt.Pos.Y, float64(targetStartY), "the target drifted while the torpedo homed onto it")
	require.Less(t, tgt.HP, 5000, "the homing target took splash damage")
	require.Zero(t, tgt.Shield, "splash drains the shield first")

	// Friendly-fire: the firing player's OWN ship in the blast took damage (AC-6).
	fr, ok := shipByID(4)
	require.True(t, ok, "the friendly ship survives the blast")
	require.Less(t, fr.HP, 5000, "the firing player's own ship took splash damage — friendly-fire, AC-6")
	require.Zero(t, fr.Shield)

	// A second, unrelated victim in the same blast — splash is area, not single-target.
	by, ok := shipByID(5)
	require.True(t, ok, "the bystander survives the blast")
	require.Less(t, by.HP, 5000, "a bystander in the blast radius also took splash damage")
	require.Zero(t, by.Shield)

	// The firing ship sat outside the blast: it is unharmed and still alive (so
	// the torpedo died by detonation, not owner-loss).
	own, ok := shipByID(1)
	require.True(t, ok, "the firing ship is alive")
	require.Equal(t, 200, own.HP, "the firing ship sat outside the blast radius — unharmed")
}

// TestUnit_Torpedo_SplashHitsTheLaunchingShip: the blast is indiscriminate about
// the FIRING SHIP itself — a launcher that detonates its torpedo inside its own
// SplashRadius eats its own splash (ЧТЗ AC-6, R-02). The sector-level cousin
// (TestUnit_Torpedo_EndToEnd_LaunchHomingSplashFriendlyFire) covers "another ship
// of the same player"; the launcher itself was covered only at the primitive level
// (combat.ApplyDamageInRadius_FriendlyFireHitsOwnShips). This closes it through
// the full launch→tick→detonate pipeline (TASK-114 AC-2).
func TestUnit_Torpedo_SplashHitsTheLaunchingShip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeTorpedoRepo()

	// The launcher: tough enough to survive its own class-3 blast (600 dmg) so
	// the self-inflicted damage is readable rather than a kill.
	owner := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	owner.HP, owner.MaxHP = 5000, 5000

	// Point-blank target: 40 units out, so the detonation (within HitRadius 16 of
	// the target, i.e. at most 56 from the launcher) lands well inside the class-3
	// SplashRadius of 70 measured from the launcher itself.
	target := torpedoShip(2, 200, domain.Vec2{X: 40, Y: 0})
	target.HP, target.MaxHP = 5000, 5000

	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{owner, target})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, res.Err)

	for i := 0; i < 15 && repo.liveCount() > 0; i++ {
		w.Tick(ctx)
	}
	require.Zero(t, repo.liveCount(), "the point-blank torpedo detonated")

	snap := w.Snapshot(testSector)
	shipByID := func(id domain.ShipID) (domain.Ship, bool) {
		for _, s := range snap.Ships {
			if s.ID == id {
				return s, true
			}
		}
		return domain.Ship{}, false
	}

	own, ok := shipByID(1)
	require.True(t, ok, "the launcher survives its own blast (5000 HP)")
	require.Less(t, own.HP, 5000, "the LAUNCHING ship took its own splash — no self-exclusion, AC-6")
	require.Zero(t, own.Shield, "the launcher's own shield absorbed the blast first")

	// The blast still did its job on the mark it was aimed at.
	tgt, ok := shipByID(2)
	require.True(t, ok, "the target survives the blast")
	require.Less(t, tgt.HP, 5000, "the aimed-at target took splash damage too")

	// The detonation is a Hit (not a shoot-down / expiry) fired by ship 1.
	require.Len(t, snap.TorpedoImpacts, 1)
	require.True(t, snap.TorpedoImpacts[0].Hit, "the torpedo detonated rather than expiring")
	require.Equal(t, domain.ShipID(1), snap.TorpedoImpacts[0].OwnerShipID)
}

// TestUnit_Torpedo_SnapshotCarriesFlightAndImpacts: the full per-sector Snapshot
// — what /debug/world dumps, not the per-subscriber AOI delta the player renders
// from — carries the torpedoes in flight and this tick's torpedo impacts, exactly
// as it already carries drones/missiles (TASK-114 AC-1). Without it a torpedo in
// flight is invisible to every non-delta consumer.
func TestUnit_Torpedo_SnapshotCarriesFlightAndImpacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newFakeTorpedoRepo()

	owner := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	target := torpedoShip(2, 200, domain.Vec2{X: 400, Y: 0})
	target.HP, target.MaxHP = 5000, 5000 // survives, so the torpedo dies by detonation
	w := newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), repo, []domain.Ship{owner, target})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, res.Err)

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Torpedos, 1, "the in-flight torpedo is in the full snapshot")
	require.Equal(t, res.TorpedoID, snap.Torpedos[0].ID)
	require.Equal(t, domain.PlayerID(100), snap.Torpedos[0].PlayerID, "the snapshot carries the firing player")
	require.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}, snap.Torpedos[0].Target)
	require.Equal(t, float64(70), snap.Torpedos[0].SplashRadius, "the class-3 profile round-trips into the dump")
	require.Empty(t, snap.TorpedoImpacts, "nothing has detonated yet")

	for i := 0; i < 30 && repo.liveCount() > 0; i++ {
		w.Tick(ctx)
	}
	require.Zero(t, repo.liveCount(), "the torpedo reached its target and detonated")

	snap = w.Snapshot(testSector)
	require.Empty(t, snap.Torpedos, "the spent torpedo left the live set")
	require.Len(t, snap.TorpedoImpacts, 1, "the detonation is in the full snapshot for exactly one tick")
	require.Equal(t, res.TorpedoID, snap.TorpedoImpacts[0].TorpedoID)
	require.True(t, snap.TorpedoImpacts[0].Hit)
	require.Equal(t, float64(70), snap.TorpedoImpacts[0].SplashRadius, "the blast radius the renderer needs")

	// The published impact slice is a COPY, not a window onto the worker's
	// per-tick buffer. clearTorpedoImpacts() truncates with [:0] and the NEXT
	// impact appends straight back into the same backing array, so an aliased
	// snapshot silently mutates under its holder while keeping len()==1 — only
	// asserting the VALUE a held snapshot still carries catches that.
	held := snap
	w.Tick(ctx)
	require.Empty(t, w.Snapshot(testSector).TorpedoImpacts, "impacts are one-frame")

	// Second detonation, several ticks later: it must land in the new snapshot
	// and leave the earlier, still-held one alone.
	second := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, second.Err)
	require.NotEqual(t, res.TorpedoID, second.TorpedoID, "a second, distinguishable torpedo")
	for i := 0; i < 30 && repo.liveCount() > 0; i++ {
		w.Tick(ctx)
	}
	fresh := w.Snapshot(testSector)
	require.Len(t, fresh.TorpedoImpacts, 1)
	require.Equal(t, second.TorpedoID, fresh.TorpedoImpacts[0].TorpedoID,
		"the second detonation is the one in the fresh snapshot")

	require.Len(t, held.TorpedoImpacts, 1)
	require.Equal(t, res.TorpedoID, held.TorpedoImpacts[0].TorpedoID,
		"the earlier held snapshot must NOT be rewritten by a later tick's impact")
}

// TestUnit_LaunchTorpedo_DeadOrMissingTargetGate: a launch at a dead or
// non-existent target is rejected BEFORE any energy or ammunition is spent
// (carry-over from the .3 review). No torpedo is created.
func TestUnit_LaunchTorpedo_DeadOrMissingTargetGate(t *testing.T) {
	t.Parallel()

	newWorker := func(t *testing.T, repo *fakeTorpedoRepo, ships []domain.Ship) *sector.Worker {
		return newTorpedoWorker(t, sector.Config{TickInterval: time.Second, AOIRadius: 100000},
			clock.NewRealClock(), repo, ships)
	}

	t.Run("missing target", func(t *testing.T) {
		t.Parallel()
		repo := newFakeTorpedoRepo()
		a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
		a.Energy = 50
		a.MaxEnergy = 1000
		w := newWorker(t, repo, []domain.Ship{a})

		res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
			PlayerID: 100, ShipID: 1, Class: 2, EnergyCost: 30,
			Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 999}, // no such ship
		})
		require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
		require.Equal(t, 0, repo.creates, "no torpedo created for a missing target")
		require.Equal(t, 50, shipEnergyByID(t, w, 1), "rejected launch spends no energy")
	})

	t.Run("dead target", func(t *testing.T) {
		t.Parallel()
		repo := newFakeTorpedoRepo()
		a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
		a.Energy = 50
		a.MaxEnergy = 1000
		dead := torpedoShip(2, 200, domain.Vec2{X: 100, Y: 0})
		dead.HP = 0 // already dead
		w := newWorker(t, repo, []domain.Ship{a, dead})

		res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
			PlayerID: 100, ShipID: 1, Class: 2, EnergyCost: 30,
			Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		})
		require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
		require.Equal(t, 0, repo.creates, "no torpedo created for a dead target")
		require.Equal(t, 50, shipEnergyByID(t, w, 1), "rejected launch spends no energy")
	})
}
