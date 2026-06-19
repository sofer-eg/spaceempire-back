package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_Worker_Subscribe_DeliversAsteroids proves the WS delta stream carries
// asteroids the same way it carries containers: the first non-empty patch lists
// the in-AOI asteroid in AsteroidsAdded, and once the player starts mining the
// shrinking mass arrives in AsteroidsUpdated. Mirrors
// TestUnit_Worker_Subscribe_DeliversContainers.
func TestUnit_Worker_Subscribe_DeliversAsteroids(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := snapshotEveryCfg()
	cfg.TickInterval = 5 * time.Millisecond
	cfg.InboxCapacity = 64
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, newFakeAsteroidRepo(), newFakeLogistics())
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, miningPlayer)
	require.NoError(t, err)
	defer unsub()

	// The first non-empty patch must carry the existing asteroid (in AOI).
	var startMass int64
	deadline := time.After(2 * time.Second)
	for startMass == 0 {
		select {
		case patch := <-sub.Patch:
			for _, a := range patch.AsteroidsAdded {
				if a.ID == miningAstID {
					startMass = a.Mass
				}
			}
		case <-deadline:
			t.Fatal("asteroid not delivered in AsteroidsAdded")
		}
	}
	require.Equal(t, int64(100), startMass, "added asteroid carries full mass")

	// Mining lowers the mass — it must surface in AsteroidsUpdated.
	target := miningAstID
	require.NoError(t, w.Send(testSector, sector.MineCommand{
		PlayerID: miningPlayer, ShipID: miningShipID, Asteroid: &target,
	}))
	var sawLowerMass bool
	deadline = time.After(2 * time.Second)
	for !sawLowerMass {
		select {
		case patch := <-sub.Patch:
			for _, a := range patch.AsteroidsUpdated {
				if a.ID == miningAstID && a.Mass < 100 {
					sawLowerMass = true
				}
			}
		case <-deadline:
			t.Fatal("mined-down asteroid mass not delivered in AsteroidsUpdated")
		}
	}
}

// TestUnit_Worker_Subscribe_AsteroidDepletion_SurfacesRemoved proves that mining
// a body to mass<=0 surfaces it in AsteroidsRemoved — never as an
// AsteroidsUpdated frame carrying Mass==0. depleteAsteroid drops the body from
// RAM inside tickPlayerMining (before broadcastPatches), so the per-tick diff
// goes straight from "last mass>0" to removed (TASK-100.3.21 AC #1).
func TestUnit_Worker_Subscribe_AsteroidDepletion_SurfacesRemoved(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := snapshotEveryCfg()
	cfg.TickInterval = 5 * time.Millisecond
	cfg.InboxCapacity = 64
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, newFakeAsteroidRepo(), newFakeLogistics())
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, miningPlayer)
	require.NoError(t, err)
	defer unsub()

	awaitAsteroidAdded(t, sub.Patch, miningAstID)

	target := miningAstID
	require.NoError(t, w.Send(testSector, sector.MineCommand{
		PlayerID: miningPlayer, ShipID: miningShipID, Asteroid: &target,
	}))

	deadline := time.After(2 * time.Second)
	for {
		select {
		case patch := <-sub.Patch:
			for _, a := range patch.AsteroidsUpdated {
				if a.ID == miningAstID {
					require.NotZero(t, a.Mass,
						"depletion must surface as removed, not an AsteroidsUpdated mass=0 frame")
				}
			}
			for _, id := range patch.AsteroidsRemoved {
				if id == miningAstID {
					return // success
				}
			}
		case <-deadline:
			t.Fatal("depleted asteroid never delivered in AsteroidsRemoved within 2s")
		}
	}
}

// TestUnit_Worker_Subscribe_AsteroidLeavesAndReentersAOI proves an asteroid is
// removed from a subscriber's stream when it leaves the AOI window and re-added
// when it returns. The AOI centre tracks the player's own ship, so flying the
// observer far from the (static) asteroid drops it; flying back surfaces it
// again — the same add/remove path the ship/container/missile diffs use
// (TASK-100.3.21 AC #2).
func TestUnit_Worker_Subscribe_AsteroidLeavesAndReentersAOI(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A small AOI so a single hop carries the static asteroid out of view; the
	// observer's MaxSpeed alone (legacy fixture mode) snaps it to the target.
	cfg := sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 100}
	ship := domain.Ship{
		ID: miningShipID, PlayerID: miningPlayer, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		HP: 100, MaxHP: 100, MaxSpeed: 1e6,
	}
	asteroid := domain.Asteroid{ID: miningAstID, SectorID: testSector, Pos: domain.Vec2{X: 5, Y: 0}, Mass: 100, OreType: miningOre}
	w := sector.NewWorker(0, cfg,
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithAsteroids(newFakeAsteroidRepo(), map[domain.SectorID][]domain.Asteroid{testSector: {asteroid}}),
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, miningPlayer)
	require.NoError(t, err)
	defer unsub()

	// Visible at the start.
	awaitAsteroidAdded(t, sub.Patch, miningAstID)

	// Fly the observer far away → asteroid leaves the AOI → removed.
	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: miningPlayer, ShipID: miningShipID, Target: domain.Vec2{X: 10000, Y: 0},
	}))
	awaitAsteroidRemoved(t, sub.Patch, miningAstID)

	// Fly back → asteroid re-enters the AOI → added again.
	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: miningPlayer, ShipID: miningShipID, Target: domain.Vec2{X: 0, Y: 0},
	}))
	awaitAsteroidAdded(t, sub.Patch, miningAstID)
}

// awaitAsteroidAdded blocks until a patch lists want in AsteroidsAdded, or fails
// the test after 2s.
func awaitAsteroidAdded(t *testing.T, patches <-chan sector.Patch, want domain.AsteroidID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case patch := <-patches:
			for _, a := range patch.AsteroidsAdded {
				if a.ID == want {
					return
				}
			}
		case <-deadline:
			t.Fatalf("asteroid %d not delivered in AsteroidsAdded within 2s", want)
		}
	}
}

// awaitAsteroidRemoved blocks until a patch lists want in AsteroidsRemoved, or
// fails the test after 2s.
func awaitAsteroidRemoved(t *testing.T, patches <-chan sector.Patch, want domain.AsteroidID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case patch := <-patches:
			for _, id := range patch.AsteroidsRemoved {
				if id == want {
					return
				}
			}
		case <-deadline:
			t.Fatalf("asteroid %d not delivered in AsteroidsRemoved within 2s", want)
		}
	}
}
