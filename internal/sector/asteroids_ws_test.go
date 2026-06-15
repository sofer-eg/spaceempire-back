package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
