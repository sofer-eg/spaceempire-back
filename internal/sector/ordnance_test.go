package sector_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// Goods ids the launch commands carry in these tests. They mirror the api
// constants (missile 50, drone 51, gt23 "Огненная Буря") but the sector package
// knows nothing about the catalog — the id travels on the command.
const (
	testMissileGoods = domain.GoodsTypeID(50)
	testDroneGoods   = domain.GoodsTypeID(51)
	testTorpedoGoods = domain.GoodsTypeID(23)
)

// errOrdnanceTxFailed stands in for any failure that rolls the launch
// transaction back (constraint violation, lost connection, commit error).
var errOrdnanceTxFailed = errors.New("ordnance tx failed")

// errOrdnanceNoDeadline is reported by the fake when the worker calls it with a
// context that carries no live deadline. Config.RepoTimeout must bound every
// ammunition charge: without a deadline a hung Postgres parks the tick goroutine
// forever (before TASK-147 the torpedo/drone INSERTs ran on a bare
// context.Background()). Asserting it inside the fake is what makes losing that
// deadline fail the suite instead of silently hanging production.
var errOrdnanceNoDeadline = errors.New("ordnance ctx carries no live deadline")

// fakeOrdnance is an in-memory sector.Ordnance (TASK-147): it models the
// app-side adapter's single transaction — the ammunition debit and the
// projectile INSERT(s) either all happen or none do. stock is the launching
// ship's magazine per goods type; unlimited skips that accounting for the many
// tests that only care that a launch works at all. failWith and blockUntilCancel
// drive the two failure modes (rolled-back tx, hung DB).
type fakeOrdnance struct {
	stock     map[domain.GoodsTypeID]int64
	unlimited bool

	// calls counts every reached launch (including the ones that fail), debits
	// the successful charges, charged the units per goods type, and
	// missiles/torpedos/drones the objects actually created. Under the atomicity
	// invariant charged units and created objects stay equal.
	calls      int
	debits     int
	charged    map[domain.GoodsTypeID]int64
	missiles   int
	torpedos   int
	drones     int
	owners     []domain.EntityRef
	goodsTypes []domain.GoodsTypeID

	// torpedoRepo, when set, creates every torpedo through the same fake repo
	// the persistence tests count Create calls on — standing in for the real
	// adapter's torpedosRepo.WithExecutor(tx).Create inside the transaction.
	torpedoRepo *fakeTorpedoRepo

	failWith         error
	blockUntilCancel bool
	nextID           int64
}

func newFakeOrdnance(stock map[domain.GoodsTypeID]int64) *fakeOrdnance {
	if stock == nil {
		stock = map[domain.GoodsTypeID]int64{}
	}
	return &fakeOrdnance{stock: stock, charged: map[domain.GoodsTypeID]int64{}}
}

// unlimitedOrdnance is the default for tests that exercise launch mechanics
// rather than ammunition accounting: every charge succeeds.
func unlimitedOrdnance() *fakeOrdnance {
	f := newFakeOrdnance(nil)
	f.unlimited = true
	return f
}

// enter records the call and checks the contract the worker owes the ordnance:
// the context must carry a deadline (Config.RepoTimeout) that has not expired.
func (f *fakeOrdnance) enter(ctx context.Context) error {
	f.calls++
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("%w: none set", errOrdnanceNoDeadline)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: already expired (%w)", errOrdnanceNoDeadline, err)
	}
	return nil
}

func (f *fakeOrdnance) wait(ctx context.Context) error {
	if !f.blockUntilCancel {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// charge is the transaction's debit half: all-or-nothing, so a short magazine
// fails before any object is created.
func (f *fakeOrdnance) charge(owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	f.owners = append(f.owners, owner)
	f.goodsTypes = append(f.goodsTypes, gtype)
	if f.failWith != nil {
		return f.failWith
	}
	if !f.unlimited {
		if f.stock[gtype] < qty {
			return cargo.ErrInsufficientQuantity
		}
		f.stock[gtype] -= qty
	}
	f.debits++
	f.charged[gtype] += qty
	return nil
}

// begin runs the two preconditions every method shares.
func (f *fakeOrdnance) begin(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	if err := f.enter(ctx); err != nil {
		return err
	}
	if err := f.wait(ctx); err != nil {
		return err
	}
	return f.charge(owner, gtype, qty)
}

func (f *fakeOrdnance) SpendMissile(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) error {
	if err := f.begin(ctx, owner, gtype, 1); err != nil {
		return err
	}
	f.missiles++
	return nil
}

func (f *fakeOrdnance) LaunchTorpedo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, t domain.Torpedo) (domain.TorpedoID, error) {
	if err := f.begin(ctx, owner, gtype, 1); err != nil {
		return 0, err
	}
	f.torpedos++
	if f.torpedoRepo != nil {
		return f.torpedoRepo.Create(ctx, t)
	}
	f.nextID++
	return domain.TorpedoID(f.nextID), nil
}

func (f *fakeOrdnance) LaunchDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ds []domain.Drone) ([]domain.DroneID, error) {
	if err := f.begin(ctx, owner, gtype, int64(len(ds))); err != nil {
		return nil, err
	}
	ids := make([]domain.DroneID, 0, len(ds))
	for range ds {
		f.nextID++
		f.drones++
		ids = append(ids, domain.DroneID(f.nextID))
	}
	return ids, nil
}

func (f *fakeOrdnance) left(gtype domain.GoodsTypeID) int64 { return f.stock[gtype] }

// ordnanceWorker builds a single-sector worker whose launches run through ord.
func ordnanceWorker(t *testing.T, ord sector.Ordnance, cfg sector.Config, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append([]sector.Option{sector.WithOrdnance(ord)}, opts...)
	return sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships}, opts...)
}

// torpedoOrdnanceWorker is ordnanceWorker plus torpedo persistence, wired so the
// ordnance creates through the same fake repo the worker deletes/batches through.
// Snapshot carries no torpedo set, so repo.liveCount mirrors the live RAM set —
// the same proxy torpedos_test.go uses.
func torpedoOrdnanceWorker(t *testing.T, ord *fakeOrdnance, cfg sector.Config, ships []domain.Ship) (*sector.Worker, *fakeTorpedoRepo) {
	t.Helper()
	repo := newFakeTorpedoRepo()
	ord.torpedoRepo = repo
	return ordnanceWorker(t, ord, cfg, ships, sector.WithTorpedos(repo, nil)), repo
}

func ordnanceCfg() sector.Config {
	return sector.Config{TickInterval: time.Second, AOIRadius: 100000}
}

// launcherShip carries every launch module the three commands gate on, so one
// fixture serves the missile, torpedo and drone tests. Drone control level 2
// keeps the cap small enough to exercise the toSpawn clamp.
func launcherShip(id, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID: domain.ShipID(id), PlayerID: domain.PlayerID(playerID), SectorID: testSector,
		Pos: pos, Direction: domain.Vec2{X: 1, Y: 0},
		HP: 5000, MaxHP: 5000, Shield: 100, MaxShield: 100,
		Equipment: []domain.InstalledEquipment{
			{Type: "up_launcher", Level: 1},
			{Type: "up_torpedo_launcher", Level: 1},
			{Type: "up_drone_control", Level: 2},
		},
	}
}

// launchPair returns a launcher and a far-away target, far enough that nothing
// detonates or dies during the launch tick.
func launchPair() []domain.Ship {
	return []domain.Ship{
		launcherShip(1, 100, domain.Vec2{X: 0, Y: 0}),
		launcherShip(2, 200, domain.Vec2{X: 20000, Y: 0}),
	}
}

func shipTarget(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindShip, ID: id}
}

// TestUnit_LaunchMissile_LostAckChargesOnce reproduces the TASK-147 hole for
// missiles: the HTTP handler already timed out and went away (Reply == nil), yet
// the queued command still applies. With the debit inside apply the player pays
// exactly once for exactly one missile — before the fix the handler refunded on
// its 504 and the missile flew for free.
func TestUnit_LaunchMissile_LostAckChargesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testMissileGoods: 1})
	w := ordnanceWorker(t, ord, ordnanceCfg(), launchPair())

	// Reply nil == the handler gave up on the ack (504) and is gone.
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2),
		GoodsType: testMissileGoods, Reply: nil,
	}))
	w.Tick(ctx)

	require.Equal(t, 1, ord.debits, "ammunition charged exactly once")
	require.Equal(t, 1, ord.missiles)
	require.Len(t, w.Snapshot(testSector).Missiles, 1)
	assert.EqualValues(t, 0, ord.left(testMissileGoods))
	assert.Equal(t, []domain.GoodsTypeID{testMissileGoods}, ord.goodsTypes,
		"the command carries the goods id")
	assert.Equal(t, []domain.EntityRef{{Kind: domain.EntityKindShip, ID: 1}}, ord.owners,
		"the debit hits the launching ship's hold")

	// The player retries after the 504: the magazine is empty, so the second
	// command is refused and no free missile appears.
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2),
		GoodsType: testMissileGoods, Reply: reply,
	}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, cargo.ErrInsufficientQuantity)
	assert.Equal(t, 1, ord.debits, "no extra charge")
	assert.Equal(t, 1, ord.missiles, "no free duplicate missile")
}

// TestUnit_LaunchTorpedo_LostAckChargesOnce is the same lost-ack scenario for
// launch-torpedo (same hole, same fix).
func TestUnit_LaunchTorpedo_LostAckChargesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testTorpedoGoods: 1})
	w, repo := torpedoOrdnanceWorker(t, ord, ordnanceCfg(), launchPair())

	require.NoError(t, w.Send(testSector, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2, Target: shipTarget(2),
		GoodsType: testTorpedoGoods, Reply: nil,
	}))
	w.Tick(ctx)

	require.Equal(t, 1, ord.debits, "ammunition charged exactly once")
	require.Equal(t, 1, ord.torpedos)
	require.Equal(t, 1, repo.creates, "the row is created in the charging transaction")
	require.Equal(t, 1, repo.liveCount(), "the torpedo is in flight")
	assert.Equal(t, []domain.GoodsTypeID{testTorpedoGoods}, ord.goodsTypes)

	reply := make(chan sector.LaunchTorpedoResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2, Target: shipTarget(2),
		GoodsType: testTorpedoGoods, Reply: reply,
	}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, cargo.ErrInsufficientQuantity)
	assert.Equal(t, 1, ord.debits, "no extra charge")
	assert.Equal(t, 1, ord.torpedos, "no free duplicate torpedo")
}

// TestUnit_LaunchDrone_LostAckChargesOnce is the same lost-ack scenario for
// launch-drone: the whole salvo is charged and spawned in one transaction, so a
// dropped ack cannot leave the player with free drones.
func TestUnit_LaunchDrone_LostAckChargesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testDroneGoods: 2})
	w := ordnanceWorker(t, ord, ordnanceCfg(), launchPair())

	require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 2,
		GoodsType: testDroneGoods, Reply: nil,
	}))
	w.Tick(ctx)

	require.Equal(t, 1, ord.debits, "one all-or-nothing charge for the salvo")
	assert.EqualValues(t, 2, ord.charged[testDroneGoods])
	require.Equal(t, 2, ord.drones)
	require.Len(t, w.Snapshot(testSector).Drones, 2)
	assert.EqualValues(t, 0, ord.left(testDroneGoods))

	reply := make(chan sector.LaunchDroneResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 1,
		GoodsType: testDroneGoods, Reply: reply,
	}))
	w.Tick(ctx)

	// The cap (up_drone_control level 2) is reached as well, so this retry is
	// refused before the empty magazine even matters — either way no free drone.
	require.Error(t, (<-reply).Err)
	assert.Equal(t, 2, ord.drones, "no free duplicate drones")
}

// TestUnit_LaunchMissile_TxFailureSpendsNothing: the launch transaction rolls
// back — no missile in RAM, no ammunition gone, and (crucially) no energy spent,
// because the energy debit happens only after the launch has committed.
func TestUnit_LaunchMissile_TxFailureSpendsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testMissileGoods: 1})
	ord.failWith = errOrdnanceTxFailed
	ships := launchPair()
	ships[0].Energy, ships[0].MaxEnergy = 50, 1000
	w := ordnanceWorker(t, ord, ordnanceCfg(), ships)

	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2), EnergyCost: 30,
		GoodsType: testMissileGoods, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, errOrdnanceTxFailed)
	assert.Zero(t, res.MissileID)
	assert.Equal(t, 0, ord.debits, "the debit rolled back")
	assert.EqualValues(t, 1, ord.left(testMissileGoods), "ammunition still in the hold")
	assert.Empty(t, w.Snapshot(testSector).Missiles, "no missile in flight")
	assert.Equal(t, 50, shipEnergyByID(t, w, 1), "a failed launch spends no energy")
}

// TestUnit_LaunchTorpedo_TxFailureSpendsNothing mirrors the missile case: the
// debit and the torpedo INSERT are one transaction, so a rollback leaves neither.
func TestUnit_LaunchTorpedo_TxFailureSpendsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testTorpedoGoods: 1})
	ord.failWith = errOrdnanceTxFailed
	ships := launchPair()
	ships[0].Energy, ships[0].MaxEnergy = 50, 1000
	w, repo := torpedoOrdnanceWorker(t, ord, ordnanceCfg(), ships)

	reply := make(chan sector.LaunchTorpedoResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2, Target: shipTarget(2), EnergyCost: 30,
		GoodsType: testTorpedoGoods, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, errOrdnanceTxFailed)
	assert.Zero(t, res.TorpedoID)
	assert.Equal(t, 0, ord.debits)
	assert.EqualValues(t, 1, ord.left(testTorpedoGoods))
	assert.Zero(t, repo.creates, "no torpedo row")
	assert.Zero(t, repo.liveCount(), "no torpedo in flight")
	assert.Equal(t, 50, shipEnergyByID(t, w, 1), "a failed launch spends no energy")
}

// TestUnit_LaunchDrone_TxFailureSpawnsNothing: one rolled-back transaction means
// the whole salvo is off — no drones in RAM, no units charged. The pre-TASK-147
// loop broke out mid-salvo and left the handler to refund the remainder.
func TestUnit_LaunchDrone_TxFailureSpawnsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testDroneGoods: 5})
	ord.failWith = errOrdnanceTxFailed
	w := ordnanceWorker(t, ord, ordnanceCfg(), launchPair())

	reply := make(chan sector.LaunchDroneResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 2,
		GoodsType: testDroneGoods, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, errOrdnanceTxFailed)
	assert.Zero(t, res.Spawned)
	assert.Equal(t, 0, ord.debits)
	assert.EqualValues(t, 5, ord.left(testDroneGoods))
	assert.Empty(t, w.Snapshot(testSector).Drones)
}

// TestUnit_LaunchDrone_ChargesToSpawnNotRequested pins the deliberate behaviour
// change of TASK-147: the salvo is charged for what the up_drone_control cap
// actually lets fly, not for what was requested. A level-2 ship asking for 5
// with 3 in the hold used to be refused outright (Consume(5) failed in the
// handler); now 2 launch and exactly 2 are charged.
func TestUnit_LaunchDrone_ChargesToSpawnNotRequested(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testDroneGoods: 3})
	w := ordnanceWorker(t, ord, ordnanceCfg(), launchPair())

	reply := make(chan sector.LaunchDroneResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 5,
		GoodsType: testDroneGoods, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.NoError(t, res.Err)
	require.Equal(t, 2, res.Spawned, "the level-2 cap clamps the salvo")
	assert.EqualValues(t, 2, ord.charged[testDroneGoods], "charged for the clamped salvo")
	assert.EqualValues(t, 1, ord.left(testDroneGoods), "the third unit stays in the hold")
	assert.Len(t, w.Snapshot(testSector).Drones, 2)
}

// TestUnit_LaunchDrone_ShortHoldSpawnsNothing: the clamped salvo is
// all-or-nothing. Two drones fit under the cap but only one is in the hold, so
// the whole launch is refused — no partial spawn, nothing charged.
func TestUnit_LaunchDrone_ShortHoldSpawnsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testDroneGoods: 1})
	w := ordnanceWorker(t, ord, ordnanceCfg(), launchPair())

	reply := make(chan sector.LaunchDroneResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 2,
		GoodsType: testDroneGoods, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, cargo.ErrInsufficientQuantity)
	assert.Zero(t, res.Spawned)
	assert.EqualValues(t, 1, ord.left(testDroneGoods))
	assert.Empty(t, w.Snapshot(testSector).Drones)
}

// TestUnit_Launch_HungDBBoundedByRepoTimeout covers the deadline half of AC#4
// for all three commands: a Postgres that never answers must not park the tick
// goroutine. RepoTimeout bounds the charge, so the command fails and the tick
// completes — the pre-TASK-147 torpedo/drone INSERTs ran on an uninterruptible
// context.Background() and would have hung forever.
func TestUnit_Launch_HungDBBoundedByRepoTimeout(t *testing.T) {
	t.Parallel()
	cfg := ordnanceCfg()
	cfg.RepoTimeout = 20 * time.Millisecond

	tickBounded := func(t *testing.T, w *sector.Worker) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			w.Tick(context.Background())
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("tick blocked on a hung ammunition charge")
		}
	}

	t.Run("missile", func(t *testing.T) {
		t.Parallel()
		ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testMissileGoods: 1})
		ord.blockUntilCancel = true
		w := ordnanceWorker(t, ord, cfg, launchPair())
		reply := make(chan sector.LaunchMissileResult, 1)
		require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
			PlayerID: 100, ShipID: 1, Target: shipTarget(2),
			GoodsType: testMissileGoods, Reply: reply,
		}))
		tickBounded(t, w)
		require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
		assert.Empty(t, w.Snapshot(testSector).Missiles)
	})

	t.Run("torpedo", func(t *testing.T) {
		t.Parallel()
		ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testTorpedoGoods: 1})
		ord.blockUntilCancel = true
		w, repo := torpedoOrdnanceWorker(t, ord, cfg, launchPair())
		reply := make(chan sector.LaunchTorpedoResult, 1)
		require.NoError(t, w.Send(testSector, sector.LaunchTorpedoCommand{
			PlayerID: 100, ShipID: 1, Class: 2, Target: shipTarget(2),
			GoodsType: testTorpedoGoods, Reply: reply,
		}))
		tickBounded(t, w)
		require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
		assert.Zero(t, repo.liveCount())
	})

	t.Run("drone", func(t *testing.T) {
		t.Parallel()
		ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testDroneGoods: 2})
		ord.blockUntilCancel = true
		w := ordnanceWorker(t, ord, cfg, launchPair())
		reply := make(chan sector.LaunchDroneResult, 1)
		require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
			PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 2,
			GoodsType: testDroneGoods, Reply: reply,
		}))
		tickBounded(t, w)
		require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
		assert.Empty(t, w.Snapshot(testSector).Drones)
	})
}

// TestUnit_Launch_WithoutOrdnanceRefused: a worker built without WithOrdnance
// must refuse every launch instead of firing for free. The ordnance is the only
// thing that charges the player, so a refactor that drops the wiring has to
// break loudly — exactly the doctrine TASK-144 set for StaticInstaller.
func TestUnit_Launch_WithoutOrdnanceRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newWorker := func() *sector.Worker {
		return sector.NewWorker(0, ordnanceCfg(), clock.NewRealClock(), nil, nil,
			map[domain.SectorID][]domain.Ship{testSector: launchPair()})
	}

	t.Run("missile", func(t *testing.T) {
		t.Parallel()
		w := newWorker()
		reply := make(chan sector.LaunchMissileResult, 1)
		require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
			PlayerID: 100, ShipID: 1, Target: shipTarget(2),
			GoodsType: testMissileGoods, Reply: reply,
		}))
		w.Tick(ctx)
		require.ErrorIs(t, (<-reply).Err, sector.ErrOrdnanceUnavailable)
		assert.Empty(t, w.Snapshot(testSector).Missiles, "no free missile")
	})

	t.Run("torpedo", func(t *testing.T) {
		t.Parallel()
		repo := newFakeTorpedoRepo()
		w := sector.NewWorker(0, ordnanceCfg(), clock.NewRealClock(), nil, nil,
			map[domain.SectorID][]domain.Ship{testSector: launchPair()},
			sector.WithTorpedos(repo, nil))
		reply := make(chan sector.LaunchTorpedoResult, 1)
		require.NoError(t, w.Send(testSector, sector.LaunchTorpedoCommand{
			PlayerID: 100, ShipID: 1, Class: 2, Target: shipTarget(2),
			GoodsType: testTorpedoGoods, Reply: reply,
		}))
		w.Tick(ctx)
		require.ErrorIs(t, (<-reply).Err, sector.ErrOrdnanceUnavailable)
		assert.Zero(t, repo.creates, "no free torpedo")
	})

	t.Run("drone", func(t *testing.T) {
		t.Parallel()
		w := newWorker()
		reply := make(chan sector.LaunchDroneResult, 1)
		require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
			PlayerID: 100, ShipID: 1, Target: shipTarget(2), Count: 1,
			GoodsType: testDroneGoods, Reply: reply,
		}))
		w.Tick(ctx)
		res := <-reply
		require.ErrorIs(t, res.Err, sector.ErrOrdnanceUnavailable)
		assert.Zero(t, res.Spawned)
		assert.Empty(t, w.Snapshot(testSector).Drones, "no free drones")
	})
}

// TestUnit_Launch_GateRejectionSkipsCharge: a rejected gate (no such ship) must
// not reach the ordnance at all — the ammunition is never touched, so there is
// nothing to refund. This is what replaces the old Consume-then-Refund dance.
func TestUnit_Launch_GateRejectionSkipsCharge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{testMissileGoods: 1})
	w := ordnanceWorker(t, ord, ordnanceCfg(), nil) // empty sector

	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: shipTarget(2),
		GoodsType: testMissileGoods, Reply: reply,
	}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, sector.ErrShipNotFound)
	assert.Equal(t, 0, ord.calls, "ordnance never called for a rejected gate")
	assert.EqualValues(t, 1, ord.left(testMissileGoods))
}
