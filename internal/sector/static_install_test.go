package sector_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
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

// errNoDeadline is reported by the fake when the worker calls it with a context
// that carries no live deadline. Config.RepoTimeout must bound every install:
// without a deadline a hung Postgres parks the tick goroutine forever, and with
// the withDefaults branch deleted RepoTimeout is 0 — the deadline is then already
// blown before the call even starts. Asserting it inside the fake is what makes
// removing that default fail the suite instead of silently 500-ing in production.
var errNoDeadline = errors.New("install ctx carries no live deadline")

// fakeStaticInstaller is an in-memory sector.StaticInstaller (TASK-144): it
// models the app-side adapter's single transaction — the cargo debit and the
// object INSERT either both happen or neither does. stock is the installing
// ship's hold; blockUntilCancel and failWith let tests drive the two failure
// modes (hung DB, rolled-back tx).
type fakeStaticInstaller struct {
	stock int64
	// calls counts every reached install (including the ones that end up
	// failing), debits the successful cargo debits, jammers/satellites the
	// objects actually created. Under the atomicity invariant debits and objects
	// stay equal.
	calls      int
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
	// credits counts the successful dismantle credits and dismantled names the
	// objects whose rows were deleted (TASK-146). noRoom makes the credit fail
	// with cargo.ErrNoSpace, modelling a hold with no space for the object.
	credits    int
	dismantled []domain.EntityRef
	noRoom     bool
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

// enter records the call and checks the contract the worker owes the installer:
// the context must carry a deadline (Config.RepoTimeout) and it must not have
// expired yet.
func (f *fakeStaticInstaller) enter(ctx context.Context) error {
	f.calls++
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("%w: none set", errNoDeadline)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: already expired (%w)", errNoDeadline, err)
	}
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
	if err := f.enter(ctx); err != nil {
		return 0, err
	}
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
	if err := f.enter(ctx); err != nil {
		return 0, err
	}
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

// credit is consume read backwards (TASK-146): the dismantle pays one unit back
// into the hold, refusing when it does not fit. noRoom models a full hold.
func (f *fakeStaticInstaller) credit(owner domain.EntityRef, gtype domain.GoodsTypeID) error {
	f.owners = append(f.owners, owner)
	f.goodsTypes = append(f.goodsTypes, gtype)
	if f.failWith != nil {
		return f.failWith
	}
	if f.noRoom {
		return cargo.ErrNoSpace
	}
	f.stock++
	f.credits++
	return nil
}

func (f *fakeStaticInstaller) DismantleJammer(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, id domain.JammerID) error {
	if err := f.enter(ctx); err != nil {
		return err
	}
	if err := f.wait(ctx); err != nil {
		return err
	}
	if err := f.credit(owner, gtype); err != nil {
		return err
	}
	f.jammers = dropByID(f.jammers, func(j domain.Jammer) bool { return j.ID == id })
	f.dismantled = append(f.dismantled, domain.EntityRef{Kind: domain.EntityKindJammer, ID: int64(id)})
	return nil
}

func (f *fakeStaticInstaller) DismantleSatellite(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, id domain.SatelliteID) error {
	if err := f.enter(ctx); err != nil {
		return err
	}
	if err := f.wait(ctx); err != nil {
		return err
	}
	if err := f.credit(owner, gtype); err != nil {
		return err
	}
	f.satellites = dropByID(f.satellites, func(s domain.Satellite) bool { return s.ID == id })
	f.dismantled = append(f.dismantled, domain.EntityRef{Kind: domain.EntityKindSatellite, ID: int64(id)})
	return nil
}

// dropByID removes the first element matching pred, modelling the DELETE.
func dropByID[T any](xs []T, pred func(T) bool) []T {
	for i := range xs {
		if pred(xs[i]) {
			return append(xs[:i:i], xs[i+1:]...)
		}
	}
	return xs
}

func installerWorker(t *testing.T, inst *fakeStaticInstaller, cfg sector.Config, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	return sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		append([]sector.Option{sector.WithStaticInstaller(inst)}, opts...)...,
	)
}

// modelledDBCost is the duration source the drain-budget tests hand the worker
// in place of time.Since: a call the fake stalls until its deadline cost a full
// RepoTimeout of real time, a call it answers cost nothing worth charging. hung
// reports which of the two the fake — installer or ordnance — is doing right now.
// That is the same arithmetic the wall clock produced, minus the race with the
// scheduler that made it flaky (TASK-154).
func modelledDBCost(hung func() bool, repoTimeout time.Duration) func(time.Time) time.Duration {
	return func(time.Time) time.Duration {
		if hung() {
			return repoTimeout
		}
		return 0
	}
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

// noInstallerWorker builds a worker with NO StaticInstaller wired — the
// misconfiguration the two tests below pin down.
func noInstallerWorker(t *testing.T) *sector.Worker {
	t.Helper()
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {installerTestShip()}},
	)
}

// TestUnit_InstallJammer_WithoutInstallerRefused: a worker built without
// WithStaticInstaller must refuse the install instead of creating the object for
// free. The installer is the only thing that charges the player, so a refactor
// that drops the wiring has to break loudly — before TASK-144's review this path
// silently ignored GoodsType and deployed a ≈1.13M cr generator for nothing.
func TestUnit_InstallJammer_WithoutInstallerRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := noInstallerWorker(t)

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrInstallerUnavailable)
	assert.Zero(t, res.JammerID)
	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Jammers, "no free generator")
	assert.Empty(t, snap.Destructibles, "nothing in the combat set either")
}

// TestUnit_InstallSatellite_WithoutInstallerRefused mirrors the jammer case.
func TestUnit_InstallSatellite_WithoutInstallerRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := noInstallerWorker(t)

	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 26, Reply: reply,
	}))
	w.Tick(ctx)

	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrInstallerUnavailable)
	assert.Zero(t, res.SatelliteID)
	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Satellites, "no free satellite")
	assert.Empty(t, snap.Destructibles, "nothing in the combat set either")
}

// budgetStopHandler counts the warning drainQueued logs when it abandons a drain
// with commands still queued. That warning is the whole observable difference the
// missing applyAndDrain reset makes: the budget gates nothing but whether the
// drain keeps going, so an early return still applies every command — one Run
// wake-up later.
type budgetStopHandler struct {
	n *atomic.Int64
}

func (h budgetStopHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h budgetStopHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "inbox drain stopped on db budget" {
		h.n.Add(1)
	}
	return nil
}
func (h budgetStopHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h budgetStopHandler) WithGroup(string) slog.Handler      { return h }

// TestUnit_Install_RunResetsDrainBudgetPerWakeUp covers the reset in
// applyAndDrain, which TestUnit_Install_DrainBudgetBoundsOneDrain cannot: that
// test calls w.Tick directly, and the reset it exercises is drainInbox's.
// Production drains through Run's `case env := <-w.inbox` instead.
//
// dbBudget is only ever assigned by those two resets, so with
// applyAndDrain's deleted the Run path drains on whatever the last Tick left —
// zero on a worker that has not ticked yet, negative after any install. The drain
// then gives up after two commands and reports a backlog it has no reason to
// have, on a Postgres that is answering perfectly.
func TestUnit_Install_RunResetsDrainBudgetPerWakeUp(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const queued = 8
	var stops atomic.Int64
	inst := &fakeStaticInstaller{stock: queued}
	// TickInterval outlives the test on purpose: no Tick runs, so applyAndDrain is
	// the only thing that can hand this drain a budget.
	w := sector.NewWorker(0,
		sector.Config{
			TickInterval: time.Hour, AOIRadius: 2000,
			InboxCapacity: 64, RepoTimeout: 2 * time.Second,
		},
		clock.NewRealClock(), nil,
		slog.New(budgetStopHandler{n: &stops}),
		map[domain.SectorID][]domain.Ship{testSector: {installerTestShip()}},
		sector.WithStaticInstaller(inst),
		// A DB that answers costs the budget nothing, stated rather than measured:
		// the reset is what this test is about, and "eight fake installs fit in
		// 2 s of wall clock" is an assumption about the scheduler, not about the
		// reset (TASK-154).
		sector.WithDBDurationSource(func(time.Time) time.Duration { return 0 }),
	)

	// Queued before Run starts, so the first wake-up finds the whole backlog in
	// the inbox rather than racing the sender for it.
	replies := make([]chan sector.InstallJammerResult, queued)
	for i := range replies {
		replies[i] = make(chan sector.InstallJammerResult, 1)
		require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
			PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: replies[i],
		}))
	}

	go func() { _ = w.Run(ctx) }()

	for i, reply := range replies {
		select {
		case res := <-reply:
			require.NoError(t, res.Err, "install %d", i)
		case <-time.After(5 * time.Second):
			t.Fatalf("no ack for install %d", i)
		}
	}

	// Every ack is in, so every append the worker made is visible here.
	// A healthy installer costs nothing, so a full RepoTimeout budget carries the
	// whole backlog in a single drain.
	assert.Zero(t, stops.Load(),
		"a drain against a healthy DB must not report giving up on the DB budget")
	assert.Equal(t, queued, inst.calls)
	assert.Len(t, inst.jammers, queued)
}

// TestUnit_Install_DrainBudgetBoundsOneDrain: RepoTimeout bounds ONE install, so
// without a per-drain budget a queue of installs against a hung Postgres would
// stall the Run goroutine (and every sector this worker owns) for
// InboxCapacity × RepoTimeout. Any player can fill that queue — an install with
// an empty hold now reaches the worker.
//
// With the budget, one drain spends at most ~RepoTimeout of DB time: the first
// hung install exhausts it and the rest stay in the inbox for the next tick. The
// test proves both halves — one stall is all a drain pays for, and the queued
// remainder still applies once the DB answers again.
//
// "One stall per drain" is asserted through inst.calls, not through how long
// w.Tick took: the wall-clock form of this assertion raced the scheduler and
// flaked under parallel load (TASK-154). The install still really runs into its
// RepoTimeout deadline here — that is what makes the stalled one deploy nothing —
// but what the budget is charged is stated by the injected duration source rather
// than measured. TestUnit_Install_DrainBudgetSpendsProportionally pins the
// subtraction itself.
func TestUnit_Install_DrainBudgetBoundsOneDrain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		queued      = 10
		repoTimeout = 50 * time.Millisecond
	)
	inst := &fakeStaticInstaller{stock: queued, blockUntilCancel: true}
	w := installerWorker(t, inst,
		sector.Config{
			TickInterval: time.Second, AOIRadius: 2000,
			InboxCapacity: 64, RepoTimeout: repoTimeout,
		},
		[]domain.Ship{installerTestShip()},
		sector.WithDBDurationSource(modelledDBCost(func() bool { return inst.blockUntilCancel }, repoTimeout)))

	for i := 0; i < queued; i++ {
		require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
			PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: nil,
		}))
	}

	w.Tick(ctx)

	assert.Equal(t, 1, inst.calls,
		"the budget stops the drain after the first stall, instead of chaining a RepoTimeout stall per queued install")

	// The DB answers again: the commands the budget left queued apply on the next
	// tick, so nothing was dropped — only deferred.
	inst.blockUntilCancel = false
	w.Tick(ctx)
	assert.Equal(t, queued, inst.calls, "the remainder was still in the inbox")
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, queued-1,
		"every command but the stalled one deployed")
}

// TestUnit_Install_DrainBudgetSpendsProportionally pins the arithmetic the
// wall-clock assertion in TestUnit_Install_DrainBudgetBoundsOneDrain could only
// approximate: the budget is RepoTimeout minus what each synchronous DB call
// cost, tested after every applied command. A DB answering in a quarter of
// RepoTimeout therefore carries exactly four installs per drain — not ten (a
// budget never charged, i.e. spendDBBudget dropped from the install helpers),
// not one (a budget zeroed by any call rather than debited by its cost), and not
// four-then-nothing (a budget the next drain forgets to reset).
//
// Nothing here waits on real time: the fake answers immediately and the cost is
// stated, so the assertion holds under any scheduler load (TASK-154).
func TestUnit_Install_DrainBudgetSpendsProportionally(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		queued      = 10
		repoTimeout = 400 * time.Millisecond
		callCost    = repoTimeout / 4
		perDrain    = 4
	)
	inst := &fakeStaticInstaller{stock: queued}
	w := installerWorker(t, inst,
		sector.Config{
			TickInterval: time.Second, AOIRadius: 2000,
			InboxCapacity: 64, RepoTimeout: repoTimeout,
		},
		[]domain.Ship{installerTestShip()},
		sector.WithDBDurationSource(func(time.Time) time.Duration { return callCost }))

	for i := 0; i < queued; i++ {
		require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
			PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: nil,
		}))
	}

	w.Tick(ctx)
	assert.Equal(t, perDrain, inst.calls, "one drain spends RepoTimeout at callCost per install")

	w.Tick(ctx)
	assert.Equal(t, 2*perDrain, inst.calls, "the next drain starts on a fresh budget, not the last one's overdraft")

	w.Tick(ctx)
	assert.Equal(t, queued, inst.calls, "the remainder applied — deferred, never dropped")
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, queued)
}

// TestUnit_Worker_DBBudgetChargesRealElapsedTime holds the one thing every other
// budget test replaces: the production measurement (TASK-154 review). Those tests
// all inject WithDBDurationSource, so a measurement that returned zero — a
// dropped default, or the "unify the clocks" refactor this seam exists to
// forestall (w.dbSince = the injected clock) — would leave the whole unit suite
// green while a hung Postgres charged the drain nothing, chaining a RepoTimeout
// per queued install and parking Run with no tick in between. That is the
// TASK-144 regress, restored in silence.
//
// The worker gets a clock frozen in January on purpose: real elapsed time since
// an instant an hour ago is always at least an hour, while anything derived from
// the injected clock measures against that frozen January and misses by months.
// Nothing waits — the start instant is simply in the past.
func TestUnit_Worker_DBBudgetChargesRealElapsedTime(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(0, sector.Config{TickInterval: time.Second},
		clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		nil, nil, nil)

	assert.GreaterOrEqual(t, w.DBCallCost(time.Now().Add(-time.Hour)), time.Hour,
		"the drain budget must be charged a DB call's real elapsed time, not the injected clock's")
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
