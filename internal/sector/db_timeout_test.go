package sector_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TASK-148: every DB call the worker makes from its Run goroutine runs under
// cfg.RepoTimeout and charges the drain's DB budget. Before it, only the install
// (TASK-144), launch (TASK-147) and recall (TASK-152) commands did — everything
// else (pickups, hauls, hacks, captures, saves, deletes, publishes, the periodic
// batches) ran under a bare context.Background() or an unbounded tick context, so
// a hung Postgres parked the Run goroutine, and with it every sector the worker
// owned, through whichever command happened to touch the DB first.
//
// The tests below drive that from both ends: the deadline exists and fires
// (nothing parks the tick), and the deadline does NOT quietly cost the player
// cargo — pickup, cargo teleport and station hack all report the failure and
// leave the game state they were about to change untouched.

// errNoTickDeadline is what the stalling fakes report when the worker reaches
// them without a live deadline. Asserting the contract inside the fake is what
// makes a call site that forgets dbCall (or a Config.RepoTimeout default that is
// deleted, leaving 0 → an already-blown deadline) fail the suite instead of
// silently parking a worker in production. Mirrors errNoDeadline in
// static_install_test.go.
var errNoTickDeadline = errors.New("in-tick db call carries no live deadline")

// dbStall is the shared body of the fakes below: it checks the deadline contract
// on entry and, when hung, models a Postgres that never answers by waiting for
// the deadline to fire. Every fake here is driven from the tick goroutine only,
// so the plain fields need no locking; the tests that run Tick in a goroutine
// read them after the tick has returned.
type dbStall struct {
	hung  bool
	calls int
}

func (s *dbStall) enter(ctx context.Context) error {
	s.calls++
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("%w: none set", errNoTickDeadline)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: already expired (%w)", errNoTickDeadline, err)
	}
	if !s.hung {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// stallShipRepo is a sector.ShipRepo whose every method honours dbStall.
// savedShips records the rows that were actually written.
type stallShipRepo struct {
	dbStall
	saved   []domain.Ship
	batches int
	deletes []domain.ShipID
}

func (r *stallShipRepo) Save(ctx context.Context, s domain.Ship) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	r.saved = append(r.saved, s)
	return nil
}

func (r *stallShipRepo) SaveEquipment(ctx context.Context, s domain.Ship) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	r.saved = append(r.saved, s)
	return nil
}

func (r *stallShipRepo) BatchUpdate(ctx context.Context, _ []domain.Ship) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	r.batches++
	return nil
}

func (r *stallShipRepo) Delete(ctx context.Context, id domain.ShipID) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	r.deletes = append(r.deletes, id)
	return nil
}

// stallContainerRepo is a sector.ContainerRepo that models the container's cargo
// ledger the way Postgres holds it: Pickup is one transaction, so a container is
// either still holding its goods or the ship is. commitOnStall turns the fake
// into the nastiest real case — the COMMIT lands AFTER the deadline fires, so the
// worker is told DeadlineExceeded about a transaction that actually succeeded.
type stallContainerRepo struct {
	dbStall
	// live is the set of container ids whose row still exists.
	live map[domain.ContainerID]bool
	// inHold counts how many container-loads this fake has moved into a ship —
	// the number the duplication test watches.
	inHold        int
	commitOnStall bool
	spawns        int
	kills         []domain.ShipID
}

func newStallContainerRepo(live ...domain.ContainerID) *stallContainerRepo {
	r := &stallContainerRepo{live: map[domain.ContainerID]bool{}}
	for _, id := range live {
		r.live[id] = true
	}
	return r
}

// pickup is the transaction body: it is a no-op unless the row is still there.
func (r *stallContainerRepo) pickup(id domain.ContainerID) error {
	if !r.live[id] {
		return sector.ErrContainerNotFound
	}
	delete(r.live, id)
	r.inHold++
	return nil
}

func (r *stallContainerRepo) Pickup(ctx context.Context, id domain.ContainerID, _ domain.ShipID) error {
	if err := r.enter(ctx); err != nil {
		if r.commitOnStall {
			// The deadline fired while COMMIT was in flight: Postgres committed
			// anyway and pgx reported the context error. This is the one outcome
			// the atomicity invariant cannot cover, and the one AC#3 is about.
			_ = r.pickup(id)
		}
		return err
	}
	return r.pickup(id)
}

func (r *stallContainerRepo) ShipCargo(ctx context.Context, _ domain.ShipID) ([]domain.CargoItem, error) {
	if err := r.enter(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *stallContainerRepo) RecordKill(ctx context.Context, victim domain.ShipID, _ domain.SectorID, _ []domain.ContainerDrop) ([]domain.Container, error) {
	if err := r.enter(ctx); err != nil {
		return nil, err
	}
	r.kills = append(r.kills, victim)
	return nil, nil
}

func (r *stallContainerRepo) SpawnContainer(ctx context.Context, _ domain.SectorID, _ domain.ContainerDrop) (domain.Container, error) {
	if err := r.enter(ctx); err != nil {
		return domain.Container{}, err
	}
	r.spawns++
	return domain.Container{}, nil
}

func (r *stallContainerRepo) Delete(ctx context.Context, id domain.ContainerID) error {
	if err := r.enter(ctx); err != nil {
		return err
	}
	delete(r.live, id)
	return nil
}

// stallLogistics is a sector.TraderLogistics whose Haul honours dbStall.
type stallLogistics struct {
	dbStall
	hauled int64
}

func (l *stallLogistics) Haul(ctx context.Context, _, _ domain.EntityRef, _ domain.GoodsTypeID, units int64) error {
	if err := l.enter(ctx); err != nil {
		return err
	}
	l.hauled += units
	return nil
}

// stallRobber is a sector.StationRobber whose Rob honours dbStall.
type stallRobber struct {
	dbStall
	robbed int64
}

func (s *stallRobber) Rob(ctx context.Context, _ domain.EntityRef, _ domain.RaceID,
	_ domain.PlayerID, _ domain.EntityRef, _ bool) (sector.RobResult, error) {
	if err := s.enter(ctx); err != nil {
		return sector.RobResult{}, err
	}
	s.robbed += 10
	return sector.RobResult{GoodsType: 2, Robbed: 10}, nil
}

// stallCfg is the shared worker config: a RepoTimeout short enough that a stalled
// call resolves inside the test rather than the go-test timeout.
func stallCfg() sector.Config {
	return sector.Config{
		TickInterval: time.Second,
		AOIRadius:    2000,
		PickupRange:  30,
		HackRange:    50,
		RepoTimeout:  20 * time.Millisecond,
	}
}

// tickWithin runs one tick and fails the test if it does not return promptly.
// This is the AC#2 assertion in reusable form: before TASK-148 every one of
// these paths parked here forever.
func tickWithin(t *testing.T, w *sector.Worker) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		w.Tick(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tick blocked on a hung DB call")
	}
}

// --- AC#3: the deadline does not silently cost the player cargo ---------------

// TestUnit_Pickup_HungDBKeepsContainerAndReportsDeadline: the pickup's DB call is
// bounded, the tick survives it, and the container is still in the sector —
// the worker only drops it from RAM once the transaction has confirmed.
func TestUnit_Pickup_HungDBKeepsContainerAndReportsDeadline(t *testing.T) {
	t.Parallel()
	repo := newStallContainerRepo(7)
	repo.hung = true
	w := stallPickupWorker(t, repo)

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.PickupContainerCommand{
		PlayerID: 100, ShipID: 1, ContainerID: 7, Reply: reply,
	}))
	tickWithin(t, w)

	require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
	assert.Len(t, w.Snapshot(testSector).Containers, 1,
		"an unconfirmed pickup must leave the container in the sector, not delete it on a maybe")
}

// TestUnit_Pickup_DeadlineOnCommittedTxCannotDuplicateCargo is the AC#3 proof for
// the nastiest ordering: the transaction COMMITs after the deadline fires, so the
// worker is told DeadlineExceeded about a pickup that really happened.
//
// The container then survives in RAM as a ghost — which is deliberate, and is
// exactly why the pickup commits before it touches RAM. Picking the ghost up
// again re-enters the same transaction, finds no row and is refused, so the goods
// reach the hold exactly once. The opposite order (drop from RAM, then commit)
// would answer the same deadline by deleting a container whose transaction had
// rolled back: cargo gone from the game with its row intact, which is the silent
// loss the AC forbids.
func TestUnit_Pickup_DeadlineOnCommittedTxCannotDuplicateCargo(t *testing.T) {
	t.Parallel()
	repo := newStallContainerRepo(7)
	repo.hung = true
	repo.commitOnStall = true
	w := stallPickupWorker(t, repo)

	first := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.PickupContainerCommand{
		PlayerID: 100, ShipID: 1, ContainerID: 7, Reply: first,
	}))
	tickWithin(t, w)
	require.ErrorIs(t, (<-first).Err, context.DeadlineExceeded)
	require.Equal(t, 1, repo.inHold, "the transaction landed: the goods are in the hold")
	require.Len(t, w.Snapshot(testSector).Containers, 1, "the ghost container is still in RAM")

	// The DB answers again and the player picks the ghost up.
	repo.hung = false
	second := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.PickupContainerCommand{
		PlayerID: 100, ShipID: 1, ContainerID: 7, Reply: second,
	}))
	tickWithin(t, w)

	require.ErrorIs(t, (<-second).Err, sector.ErrContainerNotFound)
	assert.Equal(t, 1, repo.inHold,
		"the ledger is the DB: a ghost container must not pay out a second load of cargo")
}

// TestUnit_TransportCargo_HungDBChargesNoEnergy: the teleport's haul is bounded,
// the tick survives, the player is told, and — because the energy debit sits
// AFTER the commit — nothing was charged for a move that did not confirm.
func TestUnit_TransportCargo_HungDBChargesNoEnergy(t *testing.T) {
	t.Parallel()
	log := &stallLogistics{}
	log.hung = true
	cfg := stallCfg()
	cfg.TransporterRange = 250
	cfg.TransporterEnergyCost = 40

	dest := domain.Ship{
		ID: 1, PlayerID: 100, SectorID: testSector, Energy: 500, MaxEnergy: 500,
		Equipment: []domain.InstalledEquipment{{EquipmentID: 5, Type: "up_transporter", Level: 1}},
	}
	src := domain.Ship{ID: 2, PlayerID: 100, SectorID: testSector, Pos: domain.Vec2{X: 10}}
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {dest, src}},
		sector.WithTraderLogistics(log))

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.TransportCargoCommand{
		PlayerID: 100, ShipID: 1, SourceShipID: 2, GoodsType: 3, Quantity: 5, Reply: reply,
	}))
	tickWithin(t, w)

	require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
	assert.Zero(t, log.hauled, "a stalled haul moves nothing")
	assert.EqualValues(t, 500, liveShip(t, w, 1).Energy,
		"energy is debited after the haul commits, so an unconfirmed teleport is free")
}

// TestUnit_HackStation_HungDBChargesNothingAndDropsNoLoot: the rob is bounded and
// every side effect that follows it — the energy debit, the loot container, the
// journal event — is gated on it having committed.
func TestUnit_HackStation_HungDBChargesNothingAndDropsNoLoot(t *testing.T) {
	t.Parallel()
	rob := &stallRobber{}
	rob.hung = true
	containers := newStallContainerRepo()

	ship := domain.Ship{
		ID: 1, PlayerID: 100, SectorID: testSector, Energy: 500, MaxEnergy: 500,
		Equipment: []domain.InstalledEquipment{{EquipmentID: 122, Type: "up_hack", Level: 1}},
	}
	station := domain.TradeStation{ID: 3, SectorID: testSector, Pos: domain.Vec2{X: 10}, Race: 1, Built: true}
	w := sector.NewWorker(0, stallCfg(), clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithStationRobber(rob),
		sector.WithContainers(containers, nil),
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			testSector: {TradeStations: []domain.TradeStation{station}},
		}))

	reply := make(chan sector.HackResult, 1)
	require.NoError(t, w.Send(testSector, sector.HackStationCommand{
		PlayerID: 100, ShipID: 1,
		Target:     domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3},
		EnergyCost: 60, Reply: reply,
	}))
	tickWithin(t, w)

	res := <-reply
	require.ErrorIs(t, res.Err, context.DeadlineExceeded)
	assert.Zero(t, res.Robbed)
	assert.EqualValues(t, 500, liveShip(t, w, 1).Energy, "an unconfirmed hack is free")
	assert.Zero(t, containers.spawns, "no loot container for loot that may never have been taken")
}

// TestUnit_Capture_HungDBLeavesShipWithItsOwner: the ownership transfer writes
// the row BEFORE it re-owns the ship in RAM (TASK-148), because player_id/race
// are Save-only columns — the periodic BatchUpdate writes FinalTarget/HP/Shield
// and would never repair them. A stalled save therefore reports a failed capture
// and leaves the target with its old owner on both sides.
func TestUnit_Capture_HungDBLeavesShipWithItsOwner(t *testing.T) {
	t.Parallel()
	repo := &stallShipRepo{}
	repo.hung = true
	cfg := stallCfg()
	cfg.CaptureChance, cfg.KhaakCaptureChance, cfg.CaptureRange = 819, 876, 50

	w := sector.NewWorker(0, cfg, clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {captureAttacker(1), captureTarget(0)}},
		sector.WithRelations(atWar()),
		sector.WithRNG(staticRNG{v: 0.99})) // roll succeeds

	reply := make(chan sector.CaptureResult, 1)
	require.NoError(t, w.Send(testSector, sector.CaptureShipCommand{
		PlayerID: captPlayer, ShipID: captShipID,
		Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(captTargetID)},
		EnergyCost: 100, Reply: reply,
	}))
	tickWithin(t, w)

	res := <-reply
	require.ErrorIs(t, res.Err, context.DeadlineExceeded)
	assert.False(t, res.Captured)
	assert.Equal(t, captOldOwner, liveShip(t, w, captTargetID).PlayerID,
		"an unpersisted capture must not re-own the ship in RAM: nothing would ever write it")
}

// --- AC#1/#2: no in-tick DB call can park the worker -------------------------

// TestUnit_SetShipAccess_HungDBRollsBackAndReportsDeadline covers the plain
// "immediate save" family (dock, undock, ship-dock, access flag): the save is
// bounded and its RAM change is rolled back when it does not land.
func TestUnit_SetShipAccess_HungDBRollsBackAndReportsDeadline(t *testing.T) {
	t.Parallel()
	repo := &stallShipRepo{}
	repo.hung = true
	ship := domain.Ship{ID: 1, PlayerID: 100, SectorID: testSector, IsOpen: false}
	w := sector.NewWorker(0, stallCfg(), clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}})

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.SetShipAccessCommand{
		PlayerID: 100, ShipID: 1, Open: true, Reply: reply,
	}))
	tickWithin(t, w)

	require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
	assert.False(t, liveShip(t, w, 1).IsOpen, "the flag is rolled back when its save does not land")
}

// TestUnit_RemoveShip_HungDBDoesNotParkTick: the despawn's row delete used to run
// under context.Background(); quest cleanup could therefore park every sector the
// worker owns.
func TestUnit_RemoveShip_HungDBDoesNotParkTick(t *testing.T) {
	t.Parallel()
	repo := &stallShipRepo{}
	repo.hung = true
	w := sector.NewWorker(0, stallCfg(), clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {{ID: 1, PlayerID: 100, SectorID: testSector}}})

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.RemoveShipCommand{ShipID: 1, Reply: reply}))
	tickWithin(t, w)

	require.NoError(t, (<-reply).Err) // the despawn is idempotent and never fails
	assert.Equal(t, 1, repo.calls, "the delete was attempted under a deadline")
	assert.Empty(t, w.Snapshot(testSector).Ships)
}

// TestUnit_Jump_HungDBKeepsShipInSourceSector: the handoff's ship save is bounded,
// and a jump that cannot be persisted is refused rather than half-applied — the
// ship stays where it is, with no JumpEvent published for a row that was never
// written.
func TestUnit_Jump_HungDBKeepsShipInSourceSector(t *testing.T) {
	t.Parallel()
	repo := &stallShipRepo{}
	repo.hung = true
	b := &fakeBus{}
	cfg := stallCfg()
	cfg.GateRange = 50
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 100, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0},
		}}},
		sector.WithHandoff(handoffTopology(), b))

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(1, sector.JumpCommand{
		PlayerID: 100, ShipID: 1, GateID: 10, Reply: reply,
	}))
	tickWithin(t, w)

	require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
	assert.Len(t, w.Snapshot(1).Ships, 1, "a jump that could not be persisted keeps the ship")
	assert.Empty(t, b.snapshot(), "and hands it to nobody")
}

// TestUnit_Jump_HungBusDoesNotParkTick: bus.InMemory.Publish blocks once a
// subscriber falls SubscriberBuffer messages behind (back-pressure by design), and
// under context.Background() that block had no end — a worker whose intake
// subscriber stopped draining could park the worker publishing to it. The publish
// is bounded now; the ship stays in the source sector, which is correct for the
// in-memory bus because a context error there means this subscriber did NOT
// receive the payload (see logHandoffError for what an external broker changes).
func TestUnit_Jump_HungBusDoesNotParkTick(t *testing.T) {
	t.Parallel()
	// A real bus with a 1-slot buffer and a subscriber that never drains: the
	// second publish to that topic blocks.
	b := bus.NewInMemory(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	require.NoError(t, b.Subscribe(ctx, sector.IntakeTopic(2), func([]byte) { <-block }))

	cfg := stallCfg()
	cfg.GateRange = 50
	ships := []domain.Ship{
		{ID: 1, PlayerID: 100, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}},
		{ID: 2, PlayerID: 101, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}},
		{ID: 3, PlayerID: 102, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}},
	}
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{1: ships},
		sector.WithHandoff(handoffTopology(), b))

	var last sector.CmdResult
	for _, id := range []domain.ShipID{1, 2, 3} {
		reply := make(chan sector.CmdResult, 1)
		require.NoError(t, w.Send(1, sector.JumpCommand{
			PlayerID: domain.PlayerID(99 + int64(id)), ShipID: id, GateID: 10, Reply: reply,
		}))
		tickWithin(t, w)
		last = <-reply
	}

	require.ErrorIs(t, last.Err, context.DeadlineExceeded,
		"a publish that cannot be delivered must time out, not park the worker")
	assert.Len(t, w.Snapshot(1).Ships, 1, "the undelivered jump keeps its ship here")
}

// TestUnit_Tick_HungPeriodicFlushDoesNotParkTick: the periodic BatchUpdate is on
// the tick path, not the command path, and ran under the tick's own (deadline-less)
// context. It fires every SnapshotInterval, so a hung Postgres parked the worker
// within seconds no matter what the players did. The dirty set survives the failed
// flush, so nothing is lost — the next interval retries.
func TestUnit_Tick_HungPeriodicFlushDoesNotParkTick(t *testing.T) {
	t.Parallel()
	repo := &stallShipRepo{}
	repo.hung = true
	cfg := stallCfg()
	cfg.SnapshotInterval = time.Nanosecond // due on the very first tick
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {{ID: 1, PlayerID: 100, SectorID: testSector}}})

	// AttackCommand on a missing target is rejected without I/O; the dirty mark
	// the periodic flush needs comes from the move below.
	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 100, ShipID: 1, Target: domain.Vec2{X: 500},
	}))
	tickWithin(t, w)

	assert.Positive(t, repo.calls, "the periodic flush ran and hit its deadline")
	assert.Zero(t, repo.batches, "and wrote nothing")
}

// TestUnit_Kill_HungDBDoesNotParkTick: the death path (cargo read, RecordKill,
// the killed event) is reached from the tick's sweep, so it too could park the
// worker. A ship that died still leaves the sector — the RAM removal is not
// gated on the write, matching the pre-existing best-effort contract.
func TestUnit_Kill_HungDBDoesNotParkTick(t *testing.T) {
	t.Parallel()
	containers := newStallContainerRepo()
	containers.hung = true
	dead := domain.Ship{ID: 1, PlayerID: 100, SectorID: testSector, HP: 0, MaxHP: 100}
	w := sector.NewWorker(0, stallCfg(), clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {dead}},
		sector.WithContainers(containers, nil))

	tickWithin(t, w)

	assert.Positive(t, containers.calls, "the drop ran under a deadline")
	assert.Empty(t, w.Snapshot(testSector).Ships, "the dead ship still leaves the sector")
}

// --- the drain budget now covers every writing command -----------------------

// TestUnit_Pickup_DrainBudgetBoundsOneDrain: RepoTimeout bounds ONE call, so a
// queue of them against a hung Postgres would stall the Run goroutine for
// InboxCapacity × RepoTimeout with no tick in between. TASK-144 gave the drain a
// budget but charged only installs; a player spamming pickups (or docks, or
// hacks) walked straight past it. Every DB call charges it now.
//
// What the budget is charged is stated by the injected duration source rather
// than measured, exactly as in TestUnit_Install_DrainBudgetBoundsOneDrain — the
// wall-clock form of this assertion races the scheduler.
func TestUnit_Pickup_DrainBudgetBoundsOneDrain(t *testing.T) {
	t.Parallel()
	const (
		queued      = 10
		repoTimeout = 50 * time.Millisecond
	)
	ids := make([]domain.ContainerID, 0, queued)
	live := make([]domain.Container, 0, queued)
	for i := 1; i <= queued; i++ {
		ids = append(ids, domain.ContainerID(i))
		live = append(live, domain.Container{
			ID: domain.ContainerID(i), SectorID: testSector,
			ExpiresAt: time.Now().Add(time.Hour),
		})
	}
	repo := newStallContainerRepo(ids...)
	repo.hung = true

	cfg := stallCfg()
	cfg.RepoTimeout = repoTimeout
	cfg.InboxCapacity = 64
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {{ID: 1, PlayerID: 100, SectorID: testSector}}},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: live}),
		sector.WithDBDurationSource(modelledDBCost(func() bool { return repo.hung }, repoTimeout)))

	for _, id := range ids {
		require.NoError(t, w.Send(testSector, sector.PickupContainerCommand{
			PlayerID: 100, ShipID: 1, ContainerID: id,
		}))
	}

	tickWithin(t, w)
	assert.Equal(t, 1, repo.calls,
		"the budget stops the drain after the first stall instead of chaining a RepoTimeout per queued pickup")

	// The DB answers again: nothing was dropped, only deferred.
	repo.hung = false
	tickWithin(t, w)
	assert.Equal(t, queued, repo.calls, "the remainder was still in the inbox")
	assert.Equal(t, queued-1, repo.inHold, "every pickup but the stalled one landed")
}

// --- helpers ------------------------------------------------------------------

func stallPickupWorker(t *testing.T, repo sector.ContainerRepo) *sector.Worker {
	t.Helper()
	ship := domain.Ship{ID: 1, PlayerID: 100, SectorID: testSector}
	container := domain.Container{
		ID: 7, SectorID: testSector, ExpiresAt: time.Now().Add(time.Hour),
	}
	return sector.NewWorker(0, stallCfg(), clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: {container}}))
}

// liveShip reads one ship out of the worker's published snapshot.
func liveShip(t *testing.T, w *sector.Worker, id domain.ShipID) domain.Ship {
	t.Helper()
	return shipByID(t, w.Snapshot(testSector), id)
}
