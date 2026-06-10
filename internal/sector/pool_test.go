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
)

func TestUnit_Pool_SingleWorker_OwnsEverySector(t *testing.T) {
	t.Parallel()

	sectors := []domain.SectorID{1, 2, 3, 4, 5}
	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 1, Worker: sector.Config{TickInterval: time.Second}},
		sectors,
		clock.NewRealClock(),
		nil,
		nil,
		nil,
	)

	require.Len(t, p.Workers(), 1)
	owned := p.Workers()[0].Sectors()
	assert.ElementsMatch(t, sectors, owned, "the one worker must own every sector")
}

func TestUnit_Pool_RoundRobinDistributesSectors(t *testing.T) {
	t.Parallel()

	sectors := []domain.SectorID{1, 2, 3, 4, 5}
	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 2, Worker: sector.Config{TickInterval: time.Second}},
		sectors,
		clock.NewRealClock(),
		nil,
		nil,
		nil,
	)

	require.Len(t, p.Workers(), 2)
	w0, w1 := p.Workers()[0].Sectors(), p.Workers()[1].Sectors()

	// Deterministic round-robin over the sorted sector list:
	// worker 0 gets {1, 3, 5}, worker 1 gets {2, 4}.
	assert.ElementsMatch(t, []domain.SectorID{1, 3, 5}, w0)
	assert.ElementsMatch(t, []domain.SectorID{2, 4}, w1)
}

func TestUnit_Pool_Send_RoutesToCorrectWorker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	initial := map[domain.SectorID][]domain.Ship{
		1: {{ID: 11, Pos: domain.Vec2{}, MaxSpeed: 1}},
		2: {{ID: 22, Pos: domain.Vec2{}, MaxSpeed: 1}},
		3: {{ID: 33, Pos: domain.Vec2{}, MaxSpeed: 1}},
		4: {{ID: 44, Pos: domain.Vec2{}, MaxSpeed: 1}},
		5: {{ID: 55, Pos: domain.Vec2{}, MaxSpeed: 1}},
	}
	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 2, Worker: sector.Config{TickInterval: time.Second, InboxCapacity: 16}},
		[]domain.SectorID{1, 2, 3, 4, 5},
		clock.NewRealClock(),
		nil,
		nil,
		initial,
	)

	for sid := domain.SectorID(1); sid <= 5; sid++ {
		shipID := domain.ShipID(sid * 11) // 11, 22, 33, 44, 55
		require.NoError(t, p.Send(sid, sector.MoveCommand{
			ShipID: shipID,
			Target: domain.Vec2{X: 10, Y: 0},
		}))
	}

	// Tick every worker so each sector applies its queued MoveCommand.
	for _, w := range p.Workers() {
		w.Tick(ctx)
	}

	for sid := domain.SectorID(1); sid <= 5; sid++ {
		snap := p.Snapshot(sid)
		require.Len(t, snap.Ships, 1, "sector %d", sid)
		require.NotNil(t, snap.Ships[0].Target, "sector %d ship has no target", sid)
		assert.Equal(t, 10.0, snap.Ships[0].Target.X, "sector %d", sid)
	}
}

func TestUnit_Pool_Send_UnknownSector_ReturnsErrSectorNotFound(t *testing.T) {
	t.Parallel()

	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 1, Worker: sector.Config{TickInterval: time.Second}},
		[]domain.SectorID{1},
		clock.NewRealClock(),
		nil,
		nil,
		nil,
	)

	err := p.Send(domain.SectorID(99), sector.MoveCommand{ShipID: 1})
	assert.ErrorIs(t, err, sector.ErrSectorNotFound)
}

func TestUnit_Pool_ShipsByPlayer_CollectsAcrossSectors(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 2, Worker: sector.Config{TickInterval: time.Second}},
		[]domain.SectorID{1, 2, 3},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{
			1: {{ID: 10, PlayerID: playerID, SectorID: 1}, {ID: 11, PlayerID: 8, SectorID: 1}},
			2: {{ID: 20, PlayerID: playerID, SectorID: 2}},
			3: {{ID: 30, PlayerID: 8, SectorID: 3}},
		},
	)

	ships := p.ShipsByPlayer(playerID)
	ids := make(map[int64]bool, len(ships))
	for _, s := range ships {
		ids[int64(s.ID)] = true
		assert.Equal(t, playerID, s.PlayerID)
	}
	assert.Len(t, ships, 2)
	assert.True(t, ids[10], "ship 10 (sector 1) must be present")
	assert.True(t, ids[20], "ship 20 (sector 2) must be present")
}

func TestUnit_Pool_ShipsByPlayer_NoneReturnsEmpty(t *testing.T) {
	t.Parallel()

	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 1, Worker: sector.Config{TickInterval: time.Second}},
		[]domain.SectorID{1},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{
			1: {{ID: 11, PlayerID: 8, SectorID: 1}},
		},
	)

	assert.Empty(t, p.ShipsByPlayer(domain.PlayerID(7)))
}

func TestUnit_Pool_Snapshot_UnknownSector_ReturnsZero(t *testing.T) {
	t.Parallel()

	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 1, Worker: sector.Config{TickInterval: time.Second}},
		[]domain.SectorID{1},
		clock.NewRealClock(),
		nil,
		nil,
		nil,
	)

	snap := p.Snapshot(domain.SectorID(99))
	assert.Equal(t, sector.Snapshot{}, snap)
}

func TestUnit_Pool_Subscribe_DeliversInitialAddedForSector(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 2, Worker: sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64}},
		[]domain.SectorID{1, 2},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{
			1: {{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1}},
			2: {{ID: 2, PlayerID: 7, Pos: domain.Vec2{X: 5, Y: 5}, MaxSpeed: 1}},
		},
	)

	go func() { _ = p.Run(ctx) }()

	sub, unsub, err := p.Subscribe(ctx, domain.SectorID(2), 7)
	require.NoError(t, err)
	defer unsub()

	select {
	case patch := <-sub.Patch:
		require.Len(t, patch.Added, 1, "sector 2 has one ship")
		assert.Equal(t, domain.ShipID(2), patch.Added[0].ID)
	case <-time.After(time.Second):
		t.Fatal("no initial patch within 1s")
	}
}

func TestUnit_Pool_Run_AllWorkersExitOnCancel(t *testing.T) {
	t.Parallel()

	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 3, Worker: sector.Config{TickInterval: 5 * time.Millisecond}},
		[]domain.SectorID{1, 2, 3, 4, 5},
		clock.NewRealClock(),
		nil,
		nil,
		nil,
	)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()

	// Let the workers tick a couple of times so we know Run is actually
	// running before we cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Pool.Run did not return within 1s of ctx cancel")
	}
}

func TestUnit_Pool_ConcurrentSends_DoNotPanic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sectors = 8
	ids := make([]domain.SectorID, sectors)
	initial := make(map[domain.SectorID][]domain.Ship, sectors)
	for i := 0; i < sectors; i++ {
		id := domain.SectorID(i + 1)
		ids[i] = id
		initial[id] = []domain.Ship{{ID: domain.ShipID(i + 1), Pos: domain.Vec2{}, MaxSpeed: 1}}
	}

	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 3, Worker: sector.Config{TickInterval: time.Millisecond, InboxCapacity: 1024}},
		ids,
		clock.NewRealClock(),
		nil,
		nil,
		initial,
	)

	runDone := make(chan struct{})
	go func() { _ = p.Run(ctx); close(runDone) }()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(off int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				sid := domain.SectorID((off+j)%sectors + 1)
				_ = p.Send(sid, sector.MoveCommand{
					ShipID: domain.ShipID(sid),
					Target: domain.Vec2{X: float64(j + 1), Y: 0},
				})
			}
		}(i)
	}
	wg.Wait()

	// Read back stats — should not race or panic.
	stats := p.Stats()
	assert.Len(t, stats.Workers, 3)

	cancel()
	<-runDone
}

func TestUnit_Pool_Stats_TracksOverrun(t *testing.T) {
	t.Parallel()

	p := sector.NewPool(
		sector.PoolConfig{
			WorkersCount: 1,
			// Trivially short interval so even a near-empty Tick is "overrun".
			Worker: sector.Config{TickInterval: time.Nanosecond},
		},
		[]domain.SectorID{1, 2, 3},
		clock.NewRealClock(),
		nil,
		nil,
		nil,
	)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		p.Workers()[0].Tick(ctx)
	}

	stats := p.Stats()
	require.Len(t, stats.Workers, 1)
	assert.Greater(t, stats.Workers[0].Overruns, uint64(0),
		"overruns should accumulate when TickInterval is impossibly short")
	assert.Len(t, stats.Workers[0].Sectors, 3)
}
