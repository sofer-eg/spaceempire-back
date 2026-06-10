package sector_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

type fakeShipRepo struct {
	mu         sync.Mutex
	batches    [][]domain.Ship
	batchCalls int
	saves      []domain.Ship
	deletes    []domain.ShipID
	batchErr   error
	saveErr    error
	deleteErr  error
}

func (r *fakeShipRepo) Save(_ context.Context, s domain.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saves = append(r.saves, s)
	return nil
}

func (r *fakeShipRepo) BatchUpdate(_ context.Context, ships []domain.Ship) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batchCalls++
	if r.batchErr != nil {
		return r.batchErr
	}
	cp := make([]domain.Ship, len(ships))
	copy(cp, ships)
	r.batches = append(r.batches, cp)
	return nil
}

func (r *fakeShipRepo) Delete(_ context.Context, id domain.ShipID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deletes = append(r.deletes, id)
	return nil
}

func (r *fakeShipRepo) batchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.batchCalls
}

func (r *fakeShipRepo) lastBatch() []domain.Ship {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		return nil
	}
	out := make([]domain.Ship, len(r.batches[len(r.batches)-1]))
	copy(out, r.batches[len(r.batches)-1])
	return out
}

// savedShips returns the latest Save call recorded per ship id. Several
// Save calls for the same ship collapse to the most recent one, which is
// what the shutdown-flush test wants to assert against.
func (r *fakeShipRepo) savedShips() map[domain.ShipID]domain.Ship {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[domain.ShipID]domain.Ship, len(r.saves))
	for _, s := range r.saves {
		out[s.ID] = s
	}
	return out
}

func TestUnit_Worker_Snapshot_NotFlushedBeforeInterval(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: 5 * time.Second},
		clk,
		repo,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	ctx := context.Background()
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 100, Y: 0}}))

	// 4 ticks at 1s — still under SnapshotInterval=5s.
	for i := 0; i < 4; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}

	assert.Equal(t, 0, repo.batchCount(),
		"BatchUpdate should not be called before SnapshotInterval elapses")
}

func TestUnit_Worker_Snapshot_FlushesDirtyAndClears(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: 5 * time.Second},
		clk,
		repo,
		[]domain.Ship{
			{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1},
			{ID: 2, Pos: domain.Vec2{X: 100, Y: 0}, MaxSpeed: 1}, // no Target → stays idle
		},
	)

	ctx := context.Background()
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 100, Y: 0}}))

	// 5 ticks * 1s ≥ SnapshotInterval=5s → flushes once on tick #5.
	for i := 0; i < 5; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}

	require.Equal(t, 1, repo.batchCount(), "expected exactly one BatchUpdate call")
	batch := repo.lastBatch()
	require.Len(t, batch, 1, "only ship 1 moved; ship 2 must not be dirty")
	assert.Equal(t, domain.ShipID(1), batch[0].ID)
	assert.Greater(t, batch[0].Pos.X, 0.0)

	// More ticks below interval → no extra batch.
	for i := 0; i < 4; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}
	assert.Equal(t, 1, repo.batchCount(),
		"after flush dirty must be cleared so no new BatchUpdate before next interval")
}

func TestUnit_Worker_Snapshot_NoDirtyNoCall(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: 5 * time.Second},
		clk,
		repo,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{X: 5, Y: 5}, MaxSpeed: 1}},
	)

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}

	assert.Equal(t, 0, repo.batchCount(),
		"idle ships produce no dirty set, so BatchUpdate must never be called")
}

func TestUnit_Worker_Snapshot_RetriesOnError(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{batchErr: errors.New("db down")}
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: 5 * time.Second},
		clk,
		repo,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	ctx := context.Background()
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 100, Y: 0}}))

	for i := 0; i < 5; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}
	first := repo.batchCount()
	assert.Equal(t, 1, first, "first flush attempted")

	// dirty must still hold the ship — next interval will retry.
	repo.mu.Lock()
	repo.batchErr = nil
	repo.mu.Unlock()

	for i := 0; i < 5; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}
	assert.Equal(t, 2, repo.batchCount(), "retry on next interval")
}

func TestUnit_Worker_Snapshot_NoExtraWritesPerTickedShip(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: 2 * time.Second},
		clk,
		repo,
		[]domain.Ship{
			{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1},
			{ID: 2, Pos: domain.Vec2{}, MaxSpeed: 1},
			{ID: 3, Pos: domain.Vec2{X: 50, Y: 0}, MaxSpeed: 1},
		},
	)

	ctx := context.Background()
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 100, Y: 0}}))
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 2, Target: domain.Vec2{X: 100, Y: 0}}))

	// 2 ticks → triggers a snapshot. Both ship 1 and ship 2 moved, ship 3 idle.
	for i := 0; i < 2; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}

	require.Equal(t, 1, repo.batchCount())
	batch := repo.lastBatch()
	require.Len(t, batch, 2, "exactly the two moving ships should be in the batch")
	sort.Slice(batch, func(i, j int) bool { return batch[i].ID < batch[j].ID })
	assert.Equal(t, domain.ShipID(1), batch[0].ID)
	assert.Equal(t, domain.ShipID(2), batch[1].ID)
}

func TestUnit_Worker_Snapshot_NilRepoIsNoop(t *testing.T) {
	t.Parallel()

	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: time.Second},
		clk,
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	ctx := context.Background()
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 10, Y: 0}}))

	// With nil repo persistence must be entirely skipped and not panic.
	for i := 0; i < 5; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}

	assert.Greater(t, w.Snapshot(testSector).Ships[0].Pos.X, 0.0,
		"sim should still advance when repo is nil")
}

func TestUnit_Worker_Snapshot_BatchIsolatedFromWorker(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, SnapshotInterval: time.Second},
		clk,
		repo,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	ctx := context.Background()
	require.NoError(t, w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 50, Y: 0}}))
	clk.Advance(time.Second)
	w.Tick(ctx)

	require.Equal(t, 1, repo.batchCount())
	batch := repo.lastBatch()
	require.Len(t, batch, 1)

	// Mutate the consumer copy; worker state must not change.
	batch[0].Pos.X = -1
	if batch[0].Target != nil {
		batch[0].Target.X = -1
	}

	got := w.Snapshot(testSector).Ships[0]
	assert.GreaterOrEqual(t, got.Pos.X, 0.0)
	if got.Target != nil {
		assert.GreaterOrEqual(t, got.Target.X, 0.0)
	}
}
