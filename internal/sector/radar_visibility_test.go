package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// refIn reports whether refs contains want.
func refIn(refs []domain.EntityRef, want domain.EntityRef) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}

// TestUnit_Worker_Radar_VisibilityGroups is the TASK-117 acceptance test for the
// server-side visibility groups. A subscriber with a small personal radar sees,
// on the first patch:
//   - always-visible landmarks (station, asteroid) even though they sit far
//     beyond the radar — the station stays (not trimmed), the far asteroid is
//     delivered in AsteroidsAdded;
//   - radar-gated statics (laser tower, satellite) trimmed because they are
//     outside the radar (StaticsRemoved);
//   - own ship visible; a foreign ship outside the radar hidden.
func TestUnit_Worker_Radar_VisibilityGroups(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const far = 4000 // well beyond the 1000-unit radar below
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {
		Stations:    []domain.Station{{ID: 1, SectorID: testSector, Pos: domain.Vec2{X: far, Y: 0}, HP: 100, Built: true}},
		LaserTowers: []domain.LaserTower{{ID: 2, SectorID: testSector, Pos: domain.Vec2{X: far, Y: 0}, HP: 100, Built: true}},
		Satellites:  []domain.Satellite{{ID: 3, OwnerID: ownerPtr(8), SectorID: testSector, Pos: domain.Vec2{X: far, Y: 0}, HP: 100, Built: true}},
	}}
	asteroid := domain.Asteroid{ID: 5, SectorID: testSector, Pos: domain.Vec2{X: far, Y: 0}, Mass: 100, OreType: 1}

	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: far, Y: 0}, RadarRange: 1000, MaxSpeed: 1}, // foreign, far
		}},
		sector.WithStatics(statics),
		sector.WithAsteroids(newFakeAsteroidRepo(), map[domain.SectorID][]domain.Asteroid{testSector: {asteroid}}),
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	refStation := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	refTower := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 2}
	refSat := domain.EntityRef{Kind: domain.EntityKindSatellite, ID: 3}

	select {
	case p := <-sub.Patch:
		// Ships: own visible, foreign far one hidden.
		var ownVisible, foreignVisible bool
		for _, sh := range p.Added {
			if sh.ID == 1 {
				ownVisible = true
			}
			if sh.ID == 2 {
				foreignVisible = true
			}
		}
		assert.True(t, ownVisible, "own ship must always be visible")
		assert.False(t, foreignVisible, "foreign ship beyond the radar must be hidden")

		// Statics: station always visible (not trimmed); tower & satellite gated.
		assert.False(t, refIn(p.StaticsRemoved, refStation), "far station stays (always visible)")
		assert.True(t, refIn(p.StaticsRemoved, refTower), "far laser tower trimmed (radar-gated)")
		assert.True(t, refIn(p.StaticsRemoved, refSat), "far satellite trimmed (radar-gated)")

		// Asteroid always visible even though far beyond the radar.
		var asteroidVisible bool
		for _, a := range p.AsteroidsAdded {
			if a.ID == 5 {
				asteroidVisible = true
			}
		}
		assert.True(t, asteroidVisible, "far asteroid must always be visible")
	case <-time.After(time.Second):
		t.Fatal("no initial patch within 1s")
	}
}

// TestUnit_Worker_Radar_TowerReentersOnApproach proves the radar-gated statics
// (laser tower) come back via StaticsAdded when the player flies within radar
// range — the same enter/leave delta the ships use (TASK-117).
func TestUnit_Worker_Radar_TowerReentersOnApproach(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	statics := map[domain.SectorID]domain.SectorStatics{testSector: {
		LaserTowers: []domain.LaserTower{{ID: 2, SectorID: testSector, Pos: domain.Vec2{X: 4000, Y: 0}, HP: 100, Built: true}},
	}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000, MaxSpeed: 1e6},
		}},
		sector.WithStatics(statics),
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	refTower := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 2}

	// First patch: tower is beyond the radar → trimmed.
	select {
	case p := <-sub.Patch:
		require.True(t, refIn(p.StaticsRemoved, refTower), "far tower trimmed on first patch")
	case <-time.After(time.Second):
		t.Fatal("no initial patch within 1s")
	}

	// Fly next to the tower → it re-enters the radar → StaticsAdded.
	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 7, ShipID: 1, Target: domain.Vec2{X: 3500, Y: 0},
	}))
	deadline := time.After(2 * time.Second)
	for {
		select {
		case p := <-sub.Patch:
			for _, lt := range p.StaticsAdded.LaserTowers {
				if lt.ID == 2 {
					return // success: tower entered the radar
				}
			}
		case <-deadline:
			t.Fatal("laser tower never entered the radar after approach")
		}
	}
}
