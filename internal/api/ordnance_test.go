package api_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeOrdnance is an in-memory sector.Ordnance: it stands in for the app-side
// adapter that debits the magazine and INSERTs the projectile rows in ONE
// transaction (TASK-147). Both halves succeed or neither does, which is exactly
// the invariant the launch handlers now rely on.
//
// Note there is no api-side cargo dependency for any of this any more:
// api.Config carries no MissileCargo/TorpedoCargo and, since TASK-152, no
// DroneCargo either, so the HTTP layer cannot move ammunition even in principle.
// Every charge and every credit these tests observe was made inside the worker.
//
// RecallDrones moves the same stock a launch debits, so the tests can assert the
// round trip.
type fakeOrdnance struct {
	mu       sync.Mutex
	stock    map[domain.GoodsTypeID]int64
	debits   int
	refunds  int
	charged  map[domain.GoodsTypeID]int64
	missiles int
	torpedos int
	drones   int
	// goodsTypes records the id of every reached charge attempt (successful or
	// not), so a test can pin which catalog row the handler pointed at.
	goodsTypes []domain.GoodsTypeID
	nextID     int64
}

func newFakeOrdnance(stock map[domain.GoodsTypeID]int64) *fakeOrdnance {
	if stock == nil {
		stock = map[domain.GoodsTypeID]int64{}
	}
	return &fakeOrdnance{stock: stock, charged: map[domain.GoodsTypeID]int64{}}
}

// charge is the transaction's debit half: all-or-nothing, so a short magazine
// fails before any projectile is created.
func (f *fakeOrdnance) charge(gtype domain.GoodsTypeID, qty int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goodsTypes = append(f.goodsTypes, gtype)
	if f.stock[gtype] < qty {
		return cargo.ErrInsufficientQuantity
	}
	f.stock[gtype] -= qty
	f.debits++
	f.charged[gtype] += qty
	return nil
}

func (f *fakeOrdnance) SpendMissile(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID) error {
	if err := f.charge(gtype, 1); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.missiles++
	return nil
}

func (f *fakeOrdnance) LaunchTorpedo(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, _ domain.Torpedo) (domain.TorpedoID, error) {
	if err := f.charge(gtype, 1); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torpedos++
	f.nextID++
	return domain.TorpedoID(f.nextID), nil
}

// LaunchDrones models the real ordnance since TASK-176: the salvo is sized inside
// the transaction by what the hold carries, so it launches (and charges) what is
// there rather than refusing a salvo bigger than the magazine. An empty magazine is
// still cargo.ErrInsufficientQuantity — the 400 the handler owes the player.
func (f *fakeOrdnance) LaunchDrones(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, ds []domain.Drone) ([]domain.DroneID, error) {
	launch := int64(len(ds))
	if have := f.left(gtype); have < launch {
		launch = have
	}
	if launch == 0 {
		return nil, f.charge(gtype, 1) // records the attempt and reports the empty hold
	}
	if err := f.charge(gtype, launch); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]domain.DroneID, 0, launch)
	for i := int64(0); i < launch; i++ {
		f.nextID++
		f.drones++
		ids = append(ids, domain.DroneID(f.nextID))
	}
	return ids, nil
}

// RecallDrones is the launch's mirror (TASK-152): the DELETEs and the credit
// commit together inside the worker, so the recalled units land in the same stock
// a launch debits.
func (f *fakeOrdnance) RecallDrones(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, ids []domain.DroneID) (sector.RecallOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds++
	f.stock[gtype] += int64(len(ids))
	return sector.RecallOutcome{Removed: ids, Credited: len(ids)}, nil
}

// ordnanceState is a consistent read of the counters, so tests do not race the
// worker goroutine field by field.
type ordnanceState struct {
	debits, refunds    int
	missiles, torpedos int
	drones             int
}

func (f *fakeOrdnance) snapshot() ordnanceState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ordnanceState{
		debits: f.debits, refunds: f.refunds,
		missiles: f.missiles, torpedos: f.torpedos, drones: f.drones,
	}
}

func (f *fakeOrdnance) left(gtype domain.GoodsTypeID) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stock[gtype]
}

func (f *fakeOrdnance) chargedGoods() []domain.GoodsTypeID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.GoodsTypeID(nil), f.goodsTypes...)
}

// ordnanceTestWorker builds a worker whose launch path runs through ord.
func ordnanceTestWorker(ord *fakeOrdnance, initial []domain.Ship) *sector.Worker {
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
		sector.WithOrdnance(ord),
	)
}

// noOrdnanceWorker builds a worker with NO Ordnance wired — the misconfiguration
// the launch handlers must surface as 503 rather than firing for free.
func noOrdnanceWorker(_ *testing.T, initial []domain.Ship) *sector.Worker {
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
	)
}
