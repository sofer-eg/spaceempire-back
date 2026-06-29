package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
// to repo, plus any extra options (e.g. a destructible station).
func newTorpedoWorker(t *testing.T, cfg sector.Config, clk clock.Clock, repo *fakeTorpedoRepo, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append([]sector.Option{sector.WithTorpedos(repo, nil)}, opts...)
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
