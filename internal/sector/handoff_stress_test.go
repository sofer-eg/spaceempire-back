package sector_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// TestUnit_Handoff_Stress_100ConcurrentJumps fires 100 JumpCommands at the
// same gate from sector A in parallel. After convergence: A is empty, B
// has all 100 ships, every ship-id appears exactly once. Runs without a
// real DB — the goal is to exercise the in-memory handoff path (bus,
// state mutations) under contention.
func TestUnit_Handoff_Stress_100ConcurrentJumps(t *testing.T) {
	t.Parallel()

	const (
		shipCount    = 100
		srcSector    = domain.SectorID(1)
		dstSector    = domain.SectorID(2)
		gateID       = domain.GateID(10)
		jumpRange    = float64(50)
		playerID     = domain.PlayerID(1)
		stressTopo   = "stress"
		tickInterval = 20 * time.Millisecond
	)

	topo := world.New(
		[]domain.Sector{{ID: srcSector, Name: "A"}, {ID: dstSector, Name: "B"}},
		[]domain.Gate{{
			ID:      gateID,
			SectorA: srcSector, PosA: domain.Vec2{X: 100, Y: 0},
			SectorB: dstSector, PosB: domain.Vec2{X: -100, Y: 0},
		}},
	)

	initial := make(map[domain.SectorID][]domain.Ship, 2)
	ships := make([]domain.Ship, shipCount)
	for i := 0; i < shipCount; i++ {
		ships[i] = domain.Ship{
			ID: domain.ShipID(i + 1), PlayerID: playerID, SectorID: srcSector,
			Pos: domain.Vec2{X: 100, Y: 0}, HP: 100, Shield: 100,
		}
	}
	initial[srcSector] = ships
	initial[dstSector] = nil

	b := bus.NewInMemory(shipCount * 4)
	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 2, Worker: sector.Config{
			TickInterval: tickInterval,
			GateRange:    jumpRange,
		}},
		[]domain.SectorID{srcSector, dstSector},
		clock.NewRealClock(),
		nil, // no repo — RAM-only stress
		nil,
		initial,
		sector.WithHandoff(topo, b),
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p.Start(ctx)
	go func() { _ = p.Run(ctx) }()

	replies := make(chan sector.CmdResult, shipCount)
	var wg sync.WaitGroup
	for i := 0; i < shipCount; i++ {
		wg.Add(1)
		go func(id domain.ShipID) {
			defer wg.Done()
			require.NoError(t, p.Send(srcSector, sector.JumpCommand{
				PlayerID: playerID, ShipID: id, GateID: gateID, Reply: replies,
			}))
		}(domain.ShipID(i + 1))
	}
	wg.Wait()

	deadline := time.After(5 * time.Second)
	successes := 0
	for successes < shipCount {
		select {
		case res := <-replies:
			require.NoError(t, res.Err, "JumpCommand must succeed for every ship")
			successes++
		case <-deadline:
			t.Fatalf("only %d/%d ships succeeded", successes, shipCount)
		}
	}

	require.Eventually(t, func() bool {
		return len(p.Snapshot(srcSector).Ships) == 0 &&
			len(p.Snapshot(dstSector).Ships) == shipCount
	}, 5*time.Second, 20*time.Millisecond, "all ships must converge in sector B")

	seen := make(map[domain.ShipID]int)
	for _, s := range p.Snapshot(dstSector).Ships {
		seen[s.ID]++
		assert.Equal(t, dstSector, s.SectorID)
		assert.Equal(t, domain.Vec2{X: -100, Y: 0}, s.Pos)
	}
	assert.Len(t, seen, shipCount, "every ship-id must appear exactly once")
	for id, n := range seen {
		assert.Equal(t, 1, n, "duplicated ship id %d (count=%d)", id, n)
	}

	stats := p.Stats().HandoffsTotal()
	assert.Equal(t, uint64(shipCount), stats[sector.HandoffEdge{From: srcSector, To: dstSector}])
}
