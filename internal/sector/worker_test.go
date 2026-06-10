package sector_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

const testSector = domain.SectorID(1)

func newSingleSectorWorker(t *testing.T, cfg sector.Config, clk clock.Clock, repo sector.ShipRepo, initial []domain.Ship) *sector.Worker {
	t.Helper()
	return sector.NewWorker(0, cfg, clk, repo, nil, map[domain.SectorID][]domain.Ship{testSector: initial})
}

func TestUnit_Worker_MoveCommand_ReachesTarget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 10}},
	)

	if err := w.Send(testSector, sector.MoveCommand{
		ShipID: 1,
		Target: domain.Vec2{X: 100, Y: 0},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 10 ticks × 1 s × speed 10 = 100 units → exactly reaches.
	for i := 0; i < 10; i++ {
		w.Tick(ctx)
	}

	snap := w.Snapshot(testSector)
	if len(snap.Ships) != 1 {
		t.Fatalf("ships count = %d, want 1", len(snap.Ships))
	}
	s := snap.Ships[0]
	if s.Pos.X != 100 || s.Pos.Y != 0 {
		t.Fatalf("pos = %+v, want (100, 0)", s.Pos)
	}
	if s.Target != nil {
		t.Fatalf("Target = %+v, want nil after reaching", *s.Target)
	}
	if snap.Tick != 10 {
		t.Fatalf("Tick = %d, want 10", snap.Tick)
	}
}

func TestUnit_Worker_MoveCommand_DiagonalLength(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 5}},
	)

	if err := w.Send(testSector, sector.MoveCommand{
		ShipID: 1,
		Target: domain.Vec2{X: 3, Y: 4},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Distance is 5 (3-4-5 triangle), speed 5 → one tick reaches exactly.
	w.Tick(ctx)

	s := w.Snapshot(testSector).Ships[0]
	if s.Pos.X != 3 || s.Pos.Y != 4 {
		t.Fatalf("pos = %+v, want (3, 4)", s.Pos)
	}
	if s.Target != nil {
		t.Fatalf("Target = %+v, want nil", *s.Target)
	}
}

func TestUnit_Worker_FiveShipsHundredTicks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	initial := make([]domain.Ship, 5)
	for i := range initial {
		initial[i] = domain.Ship{
			ID:       domain.ShipID(i + 1),
			Pos:      domain.Vec2{X: float64(i * 10), Y: 0},
			MaxSpeed: 1,
		}
	}

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		initial,
	)

	// Each ship moves +50 along X.
	for i := 1; i <= 5; i++ {
		target := domain.Vec2{X: float64((i-1)*10) + 50, Y: 0}
		if err := w.Send(testSector, sector.MoveCommand{ShipID: domain.ShipID(i), Target: target}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	for i := 0; i < 100; i++ {
		w.Tick(ctx)
	}

	snap := w.Snapshot(testSector)
	if len(snap.Ships) != 5 {
		t.Fatalf("ships count = %d, want 5", len(snap.Ships))
	}
	if snap.Tick != 100 {
		t.Fatalf("Tick = %d, want 100", snap.Tick)
	}

	for i, s := range snap.Ships {
		wantID := domain.ShipID(i + 1)
		wantX := float64(i*10) + 50
		if s.ID != wantID {
			t.Fatalf("snap.Ships[%d].ID = %d, want %d", i, s.ID, wantID)
		}
		if s.Pos.X != wantX || s.Pos.Y != 0 {
			t.Fatalf("ship %d pos = %+v, want (%v, 0)", s.ID, s.Pos, wantX)
		}
		if s.Target != nil {
			t.Fatalf("ship %d Target = %+v, want nil", s.ID, *s.Target)
		}
	}
}

func TestUnit_Worker_UnknownShipMoveCommand_Ignored(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	if err := w.Send(testSector, sector.MoveCommand{ShipID: 99, Target: domain.Vec2{X: 50, Y: 0}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	if len(snap.Ships) != 1 {
		t.Fatalf("ships count = %d, want 1", len(snap.Ships))
	}
	if snap.Ships[0].Pos.X != 0 {
		t.Fatalf("ship 1 was moved by unknown-target command, pos = %+v", snap.Ships[0].Pos)
	}
}

func TestUnit_Worker_InboxFull_ReturnsError(t *testing.T) {
	t.Parallel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, InboxCapacity: 2},
		clock.NewRealClock(),
		nil,
		nil,
	)

	if err := w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{}}); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if err := w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{}}); err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if err := w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{}}); err == nil {
		t.Fatal("third Send: want ErrInboxFull, got nil")
	}
}

func TestUnit_Worker_Send_UnknownSector_ReturnsError(t *testing.T) {
	t.Parallel()

	w := newSingleSectorWorker(t, sector.Config{TickInterval: time.Second}, clock.NewRealClock(), nil, nil)

	err := w.Send(domain.SectorID(999), sector.MoveCommand{ShipID: 1})
	if err == nil {
		t.Fatal("Send to unknown sector returned nil, want ErrSectorNotFound")
	}
}

func TestUnit_Worker_Snapshot_TargetIsolatedFromWorker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	if err := w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 100, Y: 0}}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w.Tick(ctx)

	snap1 := w.Snapshot(testSector)
	if snap1.Ships[0].Target == nil {
		t.Fatal("snapshot Target is nil after MoveCommand")
	}
	// Mutate consumer-side copy. A fresh snapshot from the next tick must
	// read worker state, which should be unaffected.
	snap1.Ships[0].Target.X = -999

	w.Tick(ctx)
	snap2 := w.Snapshot(testSector)
	if snap2.Ships[0].Target == nil {
		t.Fatal("snapshot Target nil after second tick")
	}
	if snap2.Ships[0].Target.X == -999 {
		t.Fatal("mutating snapshot leaked into worker state")
	}
}

func TestUnit_Worker_Snapshot_ExposesTickDuration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t, sector.Config{TickInterval: time.Second}, clock.NewRealClock(), nil, nil)

	w.Tick(ctx)

	if got := w.Snapshot(testSector).LastTickDuration; got < 0 {
		t.Fatalf("LastTickDuration = %v, want >= 0", got)
	}
}

func TestUnit_Worker_FakeClock1000Ticks_Instant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	if err := w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 1e9, Y: 0}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	start := time.Now()
	for i := 0; i < 1000; i++ {
		w.Tick(ctx)
	}
	wall := time.Since(start)

	if wall > 200*time.Millisecond {
		t.Fatalf("1000 ticks took %v wall clock, want < 200ms", wall)
	}

	snap := w.Snapshot(testSector)
	if snap.Tick != 1000 {
		t.Fatalf("Tick = %d, want 1000", snap.Tick)
	}
	// Ship moved 1000 units (speed 1 × 1 s × 1000 ticks).
	if snap.Ships[0].Pos.X != 1000 {
		t.Fatalf("Pos.X = %v, want 1000", snap.Ships[0].Pos.X)
	}
}

// TestUnit_Worker_FlushAllShipsOnShutdown verifies the phase-3.19 approach-B
// invariant: when Run's context is cancelled, the worker checkpoints EVERY
// ship's live position via Save — not just the dirty set. Position is no
// longer batched periodically, so this graceful flush is the only thing that
// leaves the DB with fresh coordinates after a clean shutdown.
func TestUnit_Worker_FlushAllShipsOnShutdown(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := &fakeShipRepo{}
	docked := domain.EntityRef{Kind: domain.EntityKindStation, ID: 7}
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Millisecond, InboxCapacity: 1024, ShutdownTimeout: 5 * time.Second},
		clock.NewRealClock(),
		repo,
		[]domain.Ship{
			// Moving ship: its position drifts every tick and is never dirty-
			// batched to the DB, so the flush must capture where it ended up.
			{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 10},
			// Parked-in-space ship: idle, never dirty, but still must be saved.
			{ID: 2, Pos: domain.Vec2{X: 42, Y: -17}},
			// Docked ship: position is the static's, must be saved too.
			{ID: 3, Pos: domain.Vec2{X: 5, Y: 5}, Docked: &docked},
		},
	)

	if err := w.Send(testSector, sector.MoveCommand{ShipID: 1, Target: domain.Vec2{X: 1e6, Y: 0}}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()

	// Wait until the moving ship has actually advanced before shutting down.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Snapshot(testSector).Tick > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	<-runDone

	saved := repo.savedShips()
	if len(saved) != 3 {
		t.Fatalf("flushed %d ships, want all 3 (ids: %v)", len(saved), saved)
	}
	if s1, ok := saved[1]; !ok || s1.Pos.X <= 0 {
		t.Fatalf("ship 1 (moving) flushed pos = %+v, want X > 0", s1.Pos)
	}
	if s2, ok := saved[2]; !ok || s2.Pos != (domain.Vec2{X: 42, Y: -17}) {
		t.Fatalf("ship 2 (parked) flushed pos = %+v, want (42, -17)", s2.Pos)
	}
	if s3, ok := saved[3]; !ok || s3.Docked == nil {
		t.Fatalf("ship 3 (docked) not flushed with Docked set: %+v", s3)
	}
}

func TestUnit_Worker_Run_ConcurrentSendAndSnapshot(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Millisecond, InboxCapacity: 1024},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 10}},
	)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()

	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		for i := 0; i < 200; i++ {
			_ = w.Send(testSector, sector.MoveCommand{
				ShipID: 1,
				Target: domain.Vec2{X: float64(i + 1), Y: 0},
			})
		}
	}()

	for i := 0; i < 500; i++ {
		_ = w.Snapshot(testSector)
	}
	sendWG.Wait()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if w.Snapshot(testSector).Tick > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	<-runDone

	if got := w.Snapshot(testSector).Tick; got == 0 {
		t.Fatal("Tick = 0 after Run with 1ms ticker, want > 0")
	}
}
