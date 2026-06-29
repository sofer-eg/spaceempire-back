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

// TestUnit_Worker_Subscribe_DeliversTorpedos proves the WS delta stream carries
// torpedoes and their detonation through the AOI broadcaster (TASK-100.3.5.7,
// ЧТЗ FR-010 / AC-10):
//   - the in-AOI subscriber sees the launch in TorpedosAdded, the homing steps in
//     TorpedosUpdated, and on detonation a TorpedoImpact{Hit, SplashRadius>0} plus
//     the id in TorpedosRemoved;
//   - the far observer, whose own ship sits beyond its radar from the action,
//     receives no torpedo frame and no blast at all.
func TestUnit_Worker_Subscribe_DeliversTorpedos(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	owner := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	// Target a short hop away: > HitRadius (16) so the launch tick does not detonate
	// instantly (the torpedo must show up in TorpedosAdded first), but close enough
	// to detonate within a fraction of a second. Tough enough to survive the blast
	// so it stays a live homing target.
	target := torpedoShip(2, 200, domain.Vec2{X: 24, Y: 0})
	target.HP = 1_000_000
	target.MaxHP = 1_000_000
	// A far observer: its own ship sits 50000 away, so the torpedo region (~0..24)
	// is well outside its AOI radius (1000).
	observer := torpedoShip(3, 300, domain.Vec2{X: 50000, Y: 0})

	cfg := sector.Config{TickInterval: 20 * time.Millisecond, InboxCapacity: 64, AOIRadius: 1000}
	w := newTorpedoWorker(t, cfg, clock.NewRealClock(), newFakeTorpedoRepo(),
		[]domain.Ship{owner, target, observer})
	go func() { _ = w.Run(ctx) }()

	subA, unsubA, err := w.Subscribe(ctx, testSector, 100) // in-AOI (owner)
	require.NoError(t, err)
	defer unsubA()
	subB, unsubB, err := w.Subscribe(ctx, testSector, 300) // far observer
	require.NoError(t, err)
	defer unsubB()

	reply := make(chan sector.LaunchTorpedoResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:  reply,
	}))
	var torpID int64
	select {
	case res := <-reply:
		require.NoError(t, res.Err)
		require.NotZero(t, res.TorpedoID)
		torpID = int64(res.TorpedoID)
	case <-time.After(2 * time.Second):
		t.Fatal("launch was not acknowledged")
	}

	// In-AOI: the launch surfaces in TorpedosAdded.
	awaitTorpedoAdded(t, subA.Patch, domain.TorpedoID(torpID))

	// In-AOI: the detonation surfaces as a Hit impact carrying the blast centre and
	// radius, and the torpedo id is reported removed in the same lifecycle.
	awaitTorpedoHit(t, subA.Patch, domain.TorpedoID(torpID))

	// Out-of-AOI (ЧТЗ AC-10): the far observer never received a torpedo frame or a
	// blast. By now subA has seen the whole lifecycle, so every torpedo-bearing
	// tick has already been broadcast to subB too — drain and assert it stayed
	// clean across the radar gap.
	drainDeadline := time.After(300 * time.Millisecond)
	for {
		select {
		case p := <-subB.Patch:
			require.Empty(t, p.TorpedosAdded, "far observer must not see torpedoes")
			require.Empty(t, p.TorpedosUpdated, "far observer must not see torpedo movement")
			require.Empty(t, p.TorpedosRemoved, "far observer must not see torpedo removal")
			require.Empty(t, p.TorpedoImpacts, "far observer must not see the blast")
		case <-drainDeadline:
			return
		}
	}
}

// awaitTorpedoAdded blocks until a patch lists want in TorpedosAdded, or fails
// the test after 2s.
func awaitTorpedoAdded(t *testing.T, patches <-chan sector.Patch, want domain.TorpedoID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case p := <-patches:
			for _, tp := range p.TorpedosAdded {
				if tp.ID == want {
					return
				}
			}
		case <-deadline:
			t.Fatalf("torpedo %d not delivered in TorpedosAdded within 2s", want)
		}
	}
}

// awaitTorpedoHit blocks until a patch carries a Hit impact for want with a
// positive SplashRadius and reports the same torpedo in TorpedosRemoved, or fails
// the test after 2s.
func awaitTorpedoHit(t *testing.T, patches <-chan sector.Patch, want domain.TorpedoID) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case p := <-patches:
			for _, imp := range p.TorpedoImpacts {
				if imp.TorpedoID != want {
					continue
				}
				require.True(t, imp.Hit, "detonation reports a Hit outcome")
				require.False(t, imp.Expired)
				require.False(t, imp.Killed)
				require.Greater(t, imp.SplashRadius, float64(0), "detonation carries the splash radius")
				require.Contains(t, p.TorpedosRemoved, want, "the detonated torpedo is removed in the same patch")
				return
			}
		case <-deadline:
			t.Fatalf("torpedo %d detonation not delivered in TorpedoImpacts within 2s", want)
		}
	}
}
