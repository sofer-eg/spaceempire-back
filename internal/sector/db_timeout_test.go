package sector_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/containers"
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
//
// violations is what makes the contract enforceable rather than decorative. On a
// command path a missing deadline surfaces through the ack (the call fails with
// errNoTickDeadline instead of DeadlineExceeded), but on a BEST-EFFORT path —
// the periodic flush, the despawn delete, the kill sweep's loot drop — the error
// is swallowed by a logger and the test passes either way. That is exactly what
// happened on the first cut of TASK-148: deleting dbCall from persistDirty,
// RemoveShipCommand or dropLoot left the suite green. Every test here asserts
// violations == 0, so those mutations now fail.
//
// The deadline check runs BEFORE the hung wait on purpose: a call with no
// deadline and hung == true would otherwise block forever on a tick context that
// nobody cancels, hanging the suite instead of failing it.
type dbStall struct {
	hung       bool
	calls      int
	violations int
}

func (s *dbStall) enter(ctx context.Context) error {
	s.calls++
	if _, ok := ctx.Deadline(); !ok {
		s.violations++
		return fmt.Errorf("%w: none set", errNoTickDeadline)
	}
	if err := ctx.Err(); err != nil {
		s.violations++
		return fmt.Errorf("%w: already expired (%w)", errNoTickDeadline, err)
	}
	if !s.hung {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// noViolations is the assertion every test in this file ends with: no DB call
// reached a fake without a live cfg.RepoTimeout deadline on its context.
func noViolations(t *testing.T, s *dbStall) {
	t.Helper()
	assert.Zero(t, s.violations,
		"a DB call reached the repo without a live deadline: some call site is not going through Worker.dbCall")
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
// The sentinel is the PERSISTENCE one (containers.ErrContainerNotFound), which is
// what Repository.Pickup really returns after its containerExistsSQL check — not
// sector.ErrContainerNotFound. Modelling the sector-level sentinel here tested a
// branch production never takes, and hid that the API's writePickupError knew
// only the sector one and answered 500 for a container that simply is not there.
func (r *stallContainerRepo) pickup(id domain.ContainerID) error {
	if !r.live[id] {
		return containers.ErrContainerNotFound
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

// stallRobber is a sector.StationRobber whose Rob honours dbStall. With
// commitOnStall set it models the deadline-with-COMMIT-in-flight outcome: the raid
// landed in the DB — stock deducted AND, since TASK-160, the loot container
// created in the same transaction — while pgx reported the context error, so the
// worker learns nothing about the container it should have published.
type stallRobber struct {
	dbStall
	robbed        int64
	commitOnStall bool
	committedLoot int
}

func (s *stallRobber) Rob(ctx context.Context, _ domain.EntityRef, _ domain.RaceID,
	_ domain.PlayerID, _ domain.EntityRef, _ bool, _ sector.LootDrop) (sector.RobResult, error) {
	if err := s.enter(ctx); err != nil {
		if s.commitOnStall {
			s.robbed += 10
			s.committedLoot++
		}
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
	noViolations(t, &repo.dbStall)
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

	// The repo answers with its own sentinel (containers.ErrContainerNotFound);
	// the worker translates it to the sector one so the HTTP layer reads 404
	// rather than falling through to a 500, and sweeps the ghost on the way out
	// instead of leaving it on the radar for the rest of its 600 s TTL.
	require.ErrorIs(t, (<-second).Err, sector.ErrContainerNotFound)
	assert.Equal(t, 1, repo.inHold,
		"the ledger is the DB: a ghost container must not pay out a second load of cargo")
	assert.Empty(t, w.Snapshot(testSector).Containers, "the ghost is swept once the DB confirms it is gone")
	noViolations(t, &repo.dbStall)
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
	noViolations(t, &log.dbStall)
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
	assert.Empty(t, w.Snapshot(testSector).Containers)
	noViolations(t, &rob.dbStall)
}

// TestUnit_HackStation_DeadlineOnCommittedTxKeepsLootInTheDB is the TASK-160 proof
// for the ordering that used to destroy goods: the raid's COMMIT lands after the
// deadline fires, so the worker is told DeadlineExceeded about a hack that really
// happened.
//
// Before TASK-160 the loot container was a SECOND transaction the worker opened
// after Rob returned, so this outcome deducted the station's stock and created
// nothing — the goods left the game. Now the container is part of the raid's own
// commit: it exists, holds the loot, and a cold start's LoadAll puts it back on
// the radar. What the worker loses is only the RAM side (addContainer never ran),
// and the hacker pays no energy for an outcome it was told failed.
func TestUnit_HackStation_DeadlineOnCommittedTxKeepsLootInTheDB(t *testing.T) {
	t.Parallel()
	rob := &stallRobber{commitOnStall: true}
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

	require.ErrorIs(t, (<-reply).Err, context.DeadlineExceeded)
	assert.Equal(t, 1, rob.committedLoot,
		"the loot container rode the same commit as the stock deduction: the goods are not lost")
	assert.Empty(t, w.Snapshot(testSector).Containers,
		"the worker cannot publish a container it was never told about — it surfaces at the next cold start")
	assert.EqualValues(t, 500, liveShip(t, w, 1).Energy, "an unconfirmed hack is free")
	assert.Zero(t, containers.spawns, "the worker opens no second transaction of its own")
	noViolations(t, &rob.dbStall)
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
	noViolations(t, &repo.dbStall)
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
	noViolations(t, &repo.dbStall)
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
	noViolations(t, &repo.dbStall)
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
	noViolations(t, &repo.dbStall)
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
	noViolations(t, &repo.dbStall)
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
	noViolations(t, &containers.dbStall)
}

// TestUnit_Jump_UndeliveredPublishRestoresTheSourceSectorRow: executeJump writes
// the relocated row BEFORE it publishes, so a publish that does not land leaves
// the row naming a sector the ship never reached. BatchUpdate does not write the
// sector column, so nothing repairs it — a crash before the next Save would
// resurrect the ship on the far side of the gate. The window predates TASK-148
// (the order is unchanged) but was theoretical while a publish could realistically
// only fail with ErrClosed; a deadline on the publish makes back-pressure a
// routine outcome, so the compensating write has to be routine too.
func TestUnit_Jump_UndeliveredPublishRestoresTheSourceSectorRow(t *testing.T) {
	t.Parallel()
	// A real bus whose only intake subscriber never drains: the second publish
	// blocks and times out.
	b := bus.NewInMemory(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wedged := make(chan struct{})
	defer close(wedged)
	require.NoError(t, b.Subscribe(ctx, sector.IntakeTopic(2), func([]byte) { <-wedged }))

	repo := &stallShipRepo{}
	cfg := stallCfg()
	cfg.GateRange = 50
	ships := []domain.Ship{
		{ID: 1, PlayerID: 100, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}},
		{ID: 2, PlayerID: 101, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}},
		{ID: 3, PlayerID: 102, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}},
	}
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), repo, nil,
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

	require.ErrorIs(t, last.Err, context.DeadlineExceeded)
	require.NotEmpty(t, repo.saved)
	assert.EqualValues(t, 1, repo.saved[len(repo.saved)-1].SectorID,
		"the last write must put the ship back in the sector it never left")
	assert.Len(t, w.Snapshot(1).Ships, 1, "and it is still here in RAM")
	noViolations(t, &repo.dbStall)
}

// --- side-effect topics: delivery IS the state change ------------------------

// TestUnit_Kill_SlowSubscriberCannotStarveTheSpacesuit is the regression test for
// the hole the first cut of TASK-148 opened. EntityKilledTopic has four
// subscribers in app.go, each doing irreversible DB work: bounty payout,
// insurance payout, quest credit, and the spacesuit respawn — the only thing
// between a dead player and an account with no ship. bus.InMemory.Publish walks
// them in registration order.
//
// With a deadline on that publish and a loop that returned at the first blocked
// send, ONE slow handler (a bounty payout waiting on a busy table, say) filled
// its buffer and every handler registered after it — including the spacesuit —
// silently received nothing. The victim's row is already gone by then, and
// nothing retries. Before the deadline existed Publish blocked until all four had
// taken it, so the loss was impossible; the deadline invented it.
//
// Two things fix it and both are asserted here: the publish for this topic
// carries no deadline at all (publishEffect), and bus.Publish attempts every
// subscriber instead of bailing at the first one that blocks.
func TestUnit_Kill_SlowSubscriberCannotStarveTheSpacesuit(t *testing.T) {
	t.Parallel()
	b := bus.NewInMemory(1) // 1-deep buffers: the slow handler backs up immediately
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Subscriber 0 is the slow one: it drains, but slowly enough that its 1-deep
	// buffer is full when the next kill publishes. Slow, not wedged — a handler
	// waiting on a busy table is the case that actually happens, and it is the one
	// that used to cost the subscribers behind it their events. (A permanently
	// wedged handler is a different failure: back-pressure then blocks the tick,
	// which is the deliberate trade publishEffect makes — losing the spacesuit is
	// worse than stalling.)
	slowSeen := make(chan struct{}, 8)
	require.NoError(t, b.Subscribe(ctx, sector.EntityKilledTopic, func([]byte) {
		time.Sleep(30 * time.Millisecond)
		slowSeen <- struct{}{}
	}))
	// Subscriber 1 stands in for the spacesuit respawner: registered AFTER the
	// slow one, which is half the point — it must not be starved by a handler
	// ahead of it in the list. It is slow itself too, so its OWN 1-deep buffer
	// backs up: that is the other half, and the one only the missing deadline
	// (publishEffect) covers. A bounded publish would give up on it here.
	suited := make(chan sector.EntityKilledEvent, 8)
	require.NoError(t, b.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		time.Sleep(25 * time.Millisecond)
		var ev sector.EntityKilledEvent
		require.NoError(t, json.Unmarshal(payload, &ev))
		suited <- ev
	}))

	dead := []domain.Ship{
		{ID: 1, PlayerID: 100, SectorID: testSector, HP: 0, MaxHP: 100},
		{ID: 2, PlayerID: 101, SectorID: testSector, HP: 0, MaxHP: 100},
		{ID: 3, PlayerID: 102, SectorID: testSector, HP: 0, MaxHP: 100},
	}
	w := sector.NewWorker(0, stallCfg(), clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: dead},
		sector.WithHandoff(handoffTopology(), b))

	// Prime both handlers so they are already busy (and their buffers already
	// occupied) when the sweep publishes the three kills.
	require.NoError(t, b.Publish(context.Background(), sector.EntityKilledTopic,
		[]byte(`{"SectorID":1}`)))
	<-suited
	<-slowSeen

	tickWithin(t, w)

	victims := map[domain.PlayerID]bool{}
	for i := 0; i < len(dead); i++ {
		select {
		case ev := <-suited:
			victims[ev.VictimPlayer] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d kills reached the spacesuit subscriber: a slow handler ahead of it starved it",
				len(victims), len(dead))
		}
	}
	assert.Len(t, victims, len(dead),
		"every dead player must reach the respawn subscriber, whatever another handler is doing")
}

// TestUnit_Kill_EffectPublishIsCancelledByShutdown is the shutdown half of the
// side-effect publish contract, and the reason publishEffect takes a parent
// context instead of using context.Background().
//
// bus.InMemory's subscriber goroutine exits the moment its context is cancelled
// — it does NOT drain what is buffered — and takes the channel's only reader with
// it. A publisher already blocked on that full channel therefore has exactly one
// way out: its own context. Under context.Background() there is none, so the
// sequence "Postgres hangs → subscribers back up → tick blocks in publish →
// SIGTERM → subscriber goroutine exits" left Run parked forever: no
// `case <-ctx.Done(): w.flushAll()`, no graceful flush of ship positions, and an
// app.go wg.Wait() that only SIGKILL ends. Background was not "how it was before
// TASK-148" either — the pre-TASK-148 publishKilled used the tick's context.
//
// The test is deterministic rather than racy: the subscriber's buffer is filled
// first and the tick's context is cancelled BEFORE the tick runs, so the publish
// must observe a dead context on a channel that will never drain. With the
// parent honoured it returns at once; with context.Background() it blocks
// forever and the tick never returns. EffectPublishTimeout is left at a minute so
// that a passing run cannot be the deadline rescuing us.
func TestUnit_Kill_EffectPublishIsCancelledByShutdown(t *testing.T) {
	t.Parallel()
	b := bus.NewInMemory(1)
	subCtx, cancelSub := context.WithCancel(context.Background())
	defer cancelSub()

	wedged := make(chan struct{})
	defer close(wedged)
	ran := make(chan struct{}, 1)
	require.NoError(t, b.Subscribe(subCtx, sector.EntityKilledTopic, func([]byte) {
		ran <- struct{}{}
		<-wedged
	}))
	// One payload into the handler (which then wedges), one into the 1-deep
	// buffer: the next publish to this topic has nowhere to go, ever.
	require.NoError(t, b.Publish(context.Background(), sector.EntityKilledTopic, []byte(`{}`)))
	<-ran
	require.NoError(t, b.Publish(context.Background(), sector.EntityKilledTopic, []byte(`{}`)))

	cfg := stallCfg()
	cfg.EffectPublishTimeout = time.Minute // must not be what saves this tick
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 100, SectorID: testSector, HP: 0, MaxHP: 100},
		}},
		sector.WithHandoff(handoffTopology(), b))

	// Shutdown has already happened by the time the tick publishes.
	tickCtx, cancelTick := context.WithCancel(context.Background())
	cancelTick()

	done := make(chan struct{})
	go func() {
		w.Tick(tickCtx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("tick never returned: the side-effect publish ignored its parent context, " +
			"so a cancelled subscriber leaves it blocked forever and shutdown hangs")
	}
	assert.Empty(t, w.Snapshot(testSector).Ships, "the dead ship still left the sector")
}

// TestUnit_Kill_EffectPublishIsBounded: even with nobody cancelling anything, a
// wedged subscriber must not park the worker for good. The infinite-block version
// of publishEffect was rejected for a sharper reason than "unbounded is untidy":
// SpawnSpacesuit re-enters THIS worker with an AddShipCommand and waits for the
// ack, so a Run goroutine parked in the kill publish cannot deliver the very
// spacesuit the block was protecting.
func TestUnit_Kill_EffectPublishIsBounded(t *testing.T) {
	t.Parallel()
	b := bus.NewInMemory(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wedged := make(chan struct{})
	defer close(wedged)
	ran := make(chan struct{}, 1)
	require.NoError(t, b.Subscribe(ctx, sector.EntityKilledTopic, func([]byte) {
		ran <- struct{}{}
		<-wedged
	}))
	require.NoError(t, b.Publish(context.Background(), sector.EntityKilledTopic, []byte(`{}`)))
	<-ran
	require.NoError(t, b.Publish(context.Background(), sector.EntityKilledTopic, []byte(`{}`)))

	cfg := stallCfg()
	cfg.EffectPublishTimeout = 30 * time.Millisecond
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 100, SectorID: testSector, HP: 0, MaxHP: 100},
		}},
		sector.WithHandoff(handoffTopology(), b))

	tickWithin(t, w) // an uncancelled context: only the deadline can end this
	assert.Empty(t, w.Snapshot(testSector).Ships)
}

// TestUnit_Bus_ExpiredContextStillDeliversToSubscribersWithRoom pins the other
// half of the bus fix at its own level: a subscriber that has room takes the
// payload even when the context is already dead. Without the non-blocking first
// attempt, a plain select over {send, ctx.Done()} picks randomly when both are
// ready — so an expired context robbed ready subscribers about half the time, and
// the resulting flake would have looked like anything but a bus bug.
func TestUnit_Bus_ExpiredContextStillDeliversToSubscribersWithRoom(t *testing.T) {
	t.Parallel()
	b := bus.NewInMemory(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan []byte, 32)
	require.NoError(t, b.Subscribe(ctx, "t", func(p []byte) { got <- p }))

	dead, cancelDead := context.WithCancel(context.Background())
	cancelDead()
	// The bug this pins is randomised: a plain select over {send, ctx.Done()}
	// picks either case when both are ready, so N iterations let it escape with
	// probability 2^-N. Four was ~6% — about one clean run in twenty. Twelve puts
	// it at ~0.02%, which is below the noise floor of the rest of the suite.
	const attempts = 12
	for i := 0; i < attempts; i++ {
		require.NoError(t, b.Publish(dead, "t", []byte("x")),
			"a subscriber with room must receive the payload regardless of the context")
	}
	for i := 0; i < attempts; i++ {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("payload not delivered to a subscriber that had room")
		}
	}
}

// TestUnit_Bus_PartialDeliveryNamesWhoMissed: when a deadline does cost a
// subscriber its payload, the error says which one — the caller has no other way
// to know, since nothing retries and the publisher sees one error for the whole
// topic.
func TestUnit_Bus_PartialDeliveryNamesWhoMissed(t *testing.T) {
	t.Parallel()
	b := bus.NewInMemory(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wedged := make(chan struct{})
	defer close(wedged)
	ran := make(chan struct{}, 1)
	require.NoError(t, b.Subscribe(ctx, "t", func([]byte) { ran <- struct{}{}; <-wedged }))
	fast := make(chan struct{}, 8)
	require.NoError(t, b.Subscribe(ctx, "t", func([]byte) { fast <- struct{}{} }))

	require.NoError(t, b.Publish(context.Background(), "t", []byte("warm")))
	<-ran
	require.NoError(t, b.Publish(context.Background(), "t", []byte("fill"))) // slow buffer now full
	// Drain what the warm-up already delivered to the fast subscriber. Without
	// this, the `<-fast` at the end reads a stale warm-up signal out of the
	// buffered channel and passes whether or not the bounded publish reached the
	// subscriber behind the blocked one — which is precisely the behaviour this
	// test exists to hold, so the assertion was decorative.
	<-fast
	<-fast

	short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	err := b.Publish(short, "t", []byte("x"))

	var partial *bus.PartialDelivery
	require.ErrorAs(t, err, &partial)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the cause stays inspectable")
	assert.Equal(t, []int{0}, partial.Undelivered)
	assert.Equal(t, 2, partial.Subscribers)
	// And the subscriber behind the blocked one still got it.
	select {
	case <-fast:
	case <-time.After(5 * time.Second):
		t.Fatal("the second subscriber was starved by the first")
	}
}

// --- production: a loop of transactions under one deadline -------------------

// stubProduction is a sector.ProductionTicker that records the station ids it was
// handed, in order, and mutates them in place so the test can prove the rotation
// still writes through to the sector's own slice. failAfter > 0 makes it stop
// (and report) after that many stations in one Tick call, standing in for the
// deadline truncating the cycle.
type stubProduction struct {
	seen  [][]domain.StationID
	calls int
}

func (p *stubProduction) Tick(_ context.Context, stations []domain.Station, _ time.Time) (int, error) {
	p.calls++
	ids := make([]domain.StationID, 0, len(stations))
	for i := range stations {
		ids = append(ids, stations[i].ID)
		stations[i].Shield++ // in-place write: must land on the worker's own slice
	}
	p.seen = append(p.seen, ids)
	return len(stations), nil
}

// TestUnit_Production_RotatesTheCycleStartAndWritesThroughSubslices holds the
// three things the rotation quietly depends on. production.Service.Tick runs one
// transaction per station, so cfg.RepoTimeout bounds the whole cycle rather than
// a round trip; with a fixed entry point a degraded DB truncated the cycle at the
// same place every tick and the tail simply stopped producing. Rotating the entry
// point spreads that, but only if:
//
//   - the start really advances with the tick counter, and every station is still
//     visited exactly once per tick (rotation, not omission or duplication);
//   - the two calls share ONE dbCall, so the bound is unchanged;
//   - both sub-slices alias the sector's own backing array, so the ticker's
//     in-place mutation is not written to a copy and thrown away.
func TestUnit_Production_RotatesTheCycleStartAndWritesThroughSubslices(t *testing.T) {
	t.Parallel()
	prod := &stubProduction{}
	statics := domain.SectorStatics{Stations: []domain.Station{
		{ID: 10, SectorID: testSector, Built: true},
		{ID: 11, SectorID: testSector, Built: true},
		{ID: 12, SectorID: testSector, Built: true},
	}}
	w := sector.NewWorker(0, stallCfg(), clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {}},
		sector.WithProduction(prod),
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: statics}))

	for i := 0; i < 3; i++ {
		w.Tick(context.Background())
	}

	require.Equal(t, 6, prod.calls, "two calls per tick: the rotated head and the wrapped tail")
	// Ticks 0,1,2 start at stations[0], [1], [2] — the truncation point moves.
	assert.Equal(t, [][]domain.StationID{
		{10, 11, 12}, {},
		{11, 12}, {10},
		{12}, {10, 11},
	}, prod.seen, "the cycle's entry point must advance with the tick counter")

	// Every station was visited once per tick and the writes landed on the
	// worker's slice, not on a copy of it.
	for _, st := range w.Snapshot(testSector).Statics.Stations {
		assert.EqualValues(t, 3, st.Shield,
			"station %d: in-place production writes must reach the sector's own slice", st.ID)
	}
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
	noViolations(t, &repo.dbStall)
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
