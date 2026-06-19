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

// newAutoDockWorker mirrors newApproachWorker but fits the ship with a Docking
// Computer (up_docking) so the tick-driven auto-dock (phase 10.3.10) completes
// the dock on approach instead of just parking at DockRange/2.
func newAutoDockWorker(t *testing.T, shipPos, stationPos domain.Vec2) *sector.Worker {
	t.Helper()
	const sec domain.SectorID = 1
	target := domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, DockRange: 3},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{sec: {{
			ID: 1, PlayerID: 7, SectorID: sec,
			Pos:       shipPos,
			MaxSpeed:  10,
			Equipment: []domain.InstalledEquipment{{Type: "up_docking", Level: 1}},
			FinalTarget: &domain.Course{
				Sector:   sec,
				Pos:      stationPos,
				Approach: &target,
			},
		}}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{sec: {
			Stations: []domain.Station{{ID: 5, SectorID: sec, Pos: stationPos, Built: true}},
		}}),
		sector.WithRouter(&stubRouter{}),
	)
}

func TestUnit_AutoDock_WithDockingModule_Docks(t *testing.T) {
	t.Parallel()
	stationPos := domain.Vec2{X: 200, Y: 0}
	shipPos := domain.Vec2{X: 0, Y: 0}
	w := newAutoDockWorker(t, shipPos, stationPos)

	// 200 unit / 10 unit per tick = 20 ticks of motion, +10 margin. With
	// up_docking the ship docks the first tick it falls inside DockRange.
	for i := 0; i < 30; i++ {
		w.Tick(context.Background())
	}

	got := w.Snapshot(1).Ships[0]
	require.NotNil(t, got.Docked, "ship with up_docking auto-docks on approach")
	assert.Equal(t, domain.EntityKindStation, got.Docked.Kind)
	assert.Equal(t, int64(5), got.Docked.ID)
	assert.Equal(t, stationPos, got.Pos, "executeDock snaps ship to static pos")
	assert.Nil(t, got.FinalTarget, "dock clears the autopilot course")
}
