package sector_test

import (
	"context"
	"testing"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

func BenchmarkWorker_Tick_1000Ships(b *testing.B) {
	ctx := context.Background()
	target := domain.Vec2{X: 1e6, Y: 0}

	initial := make([]domain.Ship, 1000)
	for i := range initial {
		t := target
		initial[i] = domain.Ship{
			ID:       domain.ShipID(i + 1),
			Pos:      domain.Vec2{X: float64(i), Y: 0},
			MaxSpeed: 1,
			Target:   &t,
		}
	}

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, InboxCapacity: 1024},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{testSector: initial},
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Tick(ctx)
	}
}

// BenchmarkWorker_Tick_AOI_500Ships_100Subs is the phase 2.6 sizing target
// (task acceptance: tick ≤ 100 ms under this load). It exercises the AOI
// broadcast path: 500 ships scattered across a 10000×10000 area and 100
// subscribers spread among them, each receiving a per-tick patch filtered
// to its 5000-unit AOI.
//
// Each subscriber's Patch channel is drained in a sink goroutine — without
// it the buffered chan fills after 16 ticks and the broadcast starts
// dropping, hiding the real work the AOI path performs.
func BenchmarkWorker_Tick_AOI_500Ships_100Subs(b *testing.B) {
	const (
		shipCount = 500
		subCount  = 100
		area      = 10000.0
	)
	ships := make([]domain.Ship, shipCount)
	for i := range ships {
		// Deterministic but spread positions; first subCount ships own
		// PlayerID i+1 so we can subscribe each player to "their" ship.
		x := float64(i) * (area / float64(shipCount))
		ships[i] = domain.Ship{
			ID:       domain.ShipID(i + 1),
			PlayerID: domain.PlayerID(i + 1),
			Pos:      domain.Vec2{X: x, Y: x * 0.7},
			MaxSpeed: 10,
			Target:   &domain.Vec2{X: x + 1, Y: x + 1},
		}
	}

	// TickInterval=1ms during setup so Run's ticker drains the Subscribe
	// inbox commands. After Subscribe is done we cancel Run and drive Tick()
	// manually — by the time runDone closes, no concurrent ticker can fire
	// alongside the measured loop.
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Millisecond, InboxCapacity: 4096, AOIRadius: 5000},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
	)

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(runCtx)
	}()

	for i := 1; i <= subCount; i++ {
		sub, _, err := w.Subscribe(runCtx, testSector, domain.PlayerID(i))
		if err != nil {
			b.Fatalf("subscribe %d: %v", i, err)
		}
		// Drain the patch channel so a slow consumer never blocks/drops.
		go func(p <-chan sector.Patch) {
			for range p {
			}
		}(sub.Patch)
	}
	cancelRun()
	<-runDone

	tickCtx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Tick(tickCtx)
	}
}

// BenchmarkWorker_Tick_10Sectors_100Ships is the phase 2.2 sizing target:
// one worker × 10 sectors × 100 ships should comfortably fit under the
// default 1s tick budget (the README says ≤ 100 ms).
func BenchmarkWorker_Tick_10Sectors_100Ships(b *testing.B) {
	ctx := context.Background()

	initial := make(map[domain.SectorID][]domain.Ship, 10)
	for sid := 1; sid <= 10; sid++ {
		ships := make([]domain.Ship, 100)
		target := domain.Vec2{X: 1e6, Y: 0}
		for i := range ships {
			t := target
			ships[i] = domain.Ship{
				ID:       domain.ShipID(sid*1000 + i + 1),
				Pos:      domain.Vec2{X: float64(i), Y: 0},
				MaxSpeed: 1,
				Target:   &t,
			}
		}
		initial[domain.SectorID(sid)] = ships
	}

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, InboxCapacity: 1024},
		clock.NewRealClock(),
		nil,
		nil,
		initial,
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Tick(ctx)
	}
}
