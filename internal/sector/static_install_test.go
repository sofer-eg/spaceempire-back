package sector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// errInstallTxFailed stands in for any failure that rolls the install
// transaction back (constraint violation, lost connection, commit error).
var errInstallTxFailed = errors.New("install tx failed")

// fakeStaticInstaller is an in-memory sector.StaticInstaller (TASK-144): it
// models the app-side adapter's single transaction — the cargo debit and the
// object INSERT either both happen or neither does. stock is the installing
// ship's hold; blockUntilCancel and failWith let tests drive the two failure
// modes (hung DB, rolled-back tx).
type fakeStaticInstaller struct {
	stock int64
	// debits counts successful cargo debits, jammers/satellites the objects
	// actually created. Under the atomicity invariant they must stay equal.
	debits     int
	jammers    []domain.Jammer
	satellites []domain.Satellite
	nextID     int64
	goodsTypes []domain.GoodsTypeID
	owners     []domain.EntityRef
	// failWith, when set, is returned instead of doing anything — the whole
	// transaction rolled back, so neither the debit nor the object happened.
	failWith error
	// blockUntilCancel makes the call wait for ctx cancellation, standing in
	// for a hung Postgres (AC#3).
	blockUntilCancel bool
}

func (f *fakeStaticInstaller) consume(owner domain.EntityRef, gtype domain.GoodsTypeID) error {
	f.owners = append(f.owners, owner)
	f.goodsTypes = append(f.goodsTypes, gtype)
	if f.failWith != nil {
		return f.failWith
	}
	if f.stock < 1 {
		return cargo.ErrInsufficientQuantity
	}
	f.stock--
	f.debits++
	return nil
}

func (f *fakeStaticInstaller) wait(ctx context.Context) error {
	if !f.blockUntilCancel {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeStaticInstaller) InstallJammer(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, j domain.Jammer) (domain.JammerID, error) {
	if err := f.wait(ctx); err != nil {
		return 0, err
	}
	if err := f.consume(owner, gtype); err != nil {
		return 0, err
	}
	f.nextID++
	j.ID = domain.JammerID(f.nextID)
	f.jammers = append(f.jammers, j)
	return j.ID, nil
}

func (f *fakeStaticInstaller) InstallSatellite(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, s domain.Satellite) (domain.SatelliteID, error) {
	if err := f.wait(ctx); err != nil {
		return 0, err
	}
	if err := f.consume(owner, gtype); err != nil {
		return 0, err
	}
	f.nextID++
	s.ID = domain.SatelliteID(f.nextID)
	f.satellites = append(f.satellites, s)
	return s.ID, nil
}

func installerWorker(t *testing.T, inst *fakeStaticInstaller, cfg sector.Config, ships []domain.Ship) *sector.Worker {
	t.Helper()
	return sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		sector.WithStaticInstaller(inst),
	)
}

func installerTestShip() domain.Ship {
	return domain.Ship{ID: 1, PlayerID: 7, SectorID: testSector, Pos: domain.Vec2{X: 30, Y: -40}, Race: 2}
}

// TestUnit_InstallJammer_LostAckDebitsOnce reproduces the TASK-144 hole: the
// HTTP handler already timed out and went away (Reply == nil), yet the queued
// command still applies. The debit now happens inside apply, in the same
// transaction as the INSERT, so the player pays exactly once for exactly one
// generator — no free duplicate. A retry on the (now empty) hold is refused.
func TestUnit_InstallJammer_LostAckDebitsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, sector.Config{TickInterval: time.Second, AOIRadius: 2000}, []domain.Ship{installerTestShip()})

	// Reply nil == the handler gave up on the ack (504) and is gone.
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: nil,
	}))
	w.Tick(ctx)

	require.Equal(t, 1, inst.debits, "cargo debited exactly once")
	require.Len(t, inst.jammers, 1, "exactly one generator created")
	require.Len(t, w.Snapshot(testSector).Statics.Jammers, 1)
	assert.EqualValues(t, 0, inst.stock)
	require.Equal(t, []domain.GoodsTypeID{27}, inst.goodsTypes, "the command carries the goods id")
	require.Equal(t, []domain.EntityRef{{Kind: domain.EntityKindShip, ID: 1}}, inst.owners,
		"the debit hits the installing ship's hold")

	// The player retries after the 504: the hold is empty now, so the second
	// command is refused and no second generator appears.
	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply,
	}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, cargo.ErrInsufficientQuantity)
	assert.Equal(t, 1, inst.debits, "no extra debit")
	assert.Len(t, inst.jammers, 1, "no free duplicate generator")
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, 1)
}

// TestUnit_InstallSatellite_LostAckDebitsOnce is the same lost-ack scenario for
// install-satellite (same code path, same hole).
func TestUnit_InstallSatellite_LostAckDebitsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, sector.Config{TickInterval: time.Second, AOIRadius: 2000}, []domain.Ship{installerTestShip()})

	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 26, Reply: nil,
	}))
	w.Tick(ctx)

	require.Equal(t, 1, inst.debits, "cargo debited exactly once")
	require.Len(t, inst.satellites, 1, "exactly one satellite created")
	require.Len(t, w.Snapshot(testSector).Statics.Satellites, 1)
	require.Equal(t, []domain.GoodsTypeID{26}, inst.goodsTypes)

	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 26, Reply: reply,
	}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, cargo.ErrInsufficientQuantity)
	assert.Equal(t, 1, inst.debits, "no extra debit")
	assert.Len(t, inst.satellites, 1, "no free duplicate satellite")
}

// TestUnit_InstallJammer_TxFailureLeavesNothing: the install transaction rolls
// back — the object must not exist in RAM, in the rendered layout or in the
// combat set, and the hold is untouched (the debit rolled back with it).
func TestUnit_InstallJammer_TxFailureLeavesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1, failWith: errInstallTxFailed}
	w := installerWorker(t, inst, sector.Config{TickInterval: time.Second, AOIRadius: 2000}, []domain.Ship{installerTestShip()})

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, errInstallTxFailed)
	assert.Zero(t, res.JammerID)
	assert.Equal(t, 0, inst.debits, "the debit rolled back with the insert")
	assert.EqualValues(t, 1, inst.stock, "goods still in the hold")
	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Jammers, "no generator in the layout")
	_, ok := findDestructible(snap, jammerRef(1))
	assert.False(t, ok, "no generator in the combat set")
}

// TestUnit_InstallSatellite_TxFailureLeavesNothing mirrors the jammer case.
func TestUnit_InstallSatellite_TxFailureLeavesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1, failWith: errInstallTxFailed}
	w := installerWorker(t, inst, sector.Config{TickInterval: time.Second, AOIRadius: 2000}, []domain.Ship{installerTestShip()})

	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 26, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, errInstallTxFailed)
	assert.Zero(t, res.SatelliteID)
	assert.Equal(t, 0, inst.debits)
	assert.EqualValues(t, 1, inst.stock)
	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Satellites)
	_, ok := findDestructible(snap, satRef(1))
	assert.False(t, ok)
}

// TestUnit_InstallJammer_HungDBBoundedByRepoTimeout covers AC#3: a Postgres that
// never answers must not park the tick goroutine forever. RepoTimeout bounds the
// install call, so the command fails and the tick completes.
func TestUnit_InstallJammer_HungDBBoundedByRepoTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1, blockUntilCancel: true}
	w := installerWorker(t, inst,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000, RepoTimeout: 20 * time.Millisecond},
		[]domain.Ship{installerTestShip()})

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply,
	}))

	done := make(chan struct{})
	go func() {
		w.Tick(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tick blocked on a hung install")
	}

	require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
	assert.Empty(t, w.Snapshot(testSector).Statics.Jammers)
}

// TestUnit_InstallJammer_GateRejectionSkipsCargo: a rejected gate (no such ship)
// must not reach the installer at all — the goods are never touched, so there is
// nothing to refund. Same for a foreign / docked ship, covered in jammer_test.go.
func TestUnit_InstallJammer_GateRejectionSkipsCargo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, sector.Config{TickInterval: time.Second, AOIRadius: 2000}, nil)

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply,
	}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, sector.ErrShipNotFound)
	assert.Equal(t, 0, inst.debits)
	assert.Empty(t, inst.goodsTypes, "installer never called for a rejected gate")
	assert.EqualValues(t, 1, inst.stock)
}
