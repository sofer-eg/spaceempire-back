package sector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// dockTargetCase parameterises the four static dockable kinds so each test
// runs against every kind without duplicated setup.
type dockTargetCase struct {
	name    string
	statics domain.SectorStatics
	target  domain.EntityRef
	pos     domain.Vec2
}

func dockTargetCases() []dockTargetCase {
	const sec = testSector
	stationPos := domain.Vec2{X: 100, Y: 50}
	shipyardPos := domain.Vec2{X: -75, Y: 200}
	tradePos := domain.Vec2{X: 300, Y: -100}
	pirbasePos := domain.Vec2{X: 0, Y: -500}
	return []dockTargetCase{
		{
			name: "station",
			statics: domain.SectorStatics{
				Stations: []domain.Station{{ID: 7, SectorID: sec, Pos: stationPos, HP: 500, Shield: 800, Built: true}},
			},
			target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 7},
			pos:    stationPos,
		},
		{
			name: "shipyard",
			statics: domain.SectorStatics{
				Shipyards: []domain.Shipyard{{ID: 3, SectorID: sec, Pos: shipyardPos, HP: 1000, Shield: 1000, Built: true}},
			},
			target: domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 3},
			pos:    shipyardPos,
		},
		{
			name: "trade_station",
			statics: domain.SectorStatics{
				TradeStations: []domain.TradeStation{{ID: 9, SectorID: sec, Pos: tradePos, HP: 400, Shield: 600, Built: true}},
			},
			target: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 9},
			pos:    tradePos,
		},
		{
			name: "pirbase",
			statics: domain.SectorStatics{
				Pirbases: []domain.Pirbase{{ID: 1, SectorID: sec, Pos: pirbasePos, HP: 100, Shield: 100, Built: true}},
			},
			target: domain.EntityRef{Kind: domain.EntityKindPirbase, ID: 1},
			pos:    pirbasePos,
		},
	}
}

// newDockWorker spawns a worker holding one player ship and the given
// statics. shipPos lets the test place the ship inside or outside dock
// range. Phase 3.12 fixed DockRange at 3 so the bench/in-range setups
// place the ship right on the static.
func newDockWorker(t *testing.T, statics domain.SectorStatics, shipPos domain.Vec2) *sector.Worker {
	t.Helper()
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, DockRange: 3},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{testSector: {{
			ID: 1, PlayerID: 7, SectorID: testSector,
			Pos: shipPos, MaxSpeed: 0,
			// up_autopilot so the SetCourseCommand gate (10.3.11) passes;
			// the module is inert for dock/undock/move tests.
			Equipment: []domain.InstalledEquipment{{Type: "up_autopilot", Level: 1}},
		}}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: statics}),
	)
}

// sendAndWait posts a command and blocks until the worker replies. Returns
// the CmdResult error so the caller can assert against sentinel errors.
func sendAndWait(t *testing.T, w *sector.Worker, cmdFactory func(chan<- sector.CmdResult) sector.Command) error {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, cmdFactory(reply)))
	w.Tick(context.Background())
	select {
	case res := <-reply:
		return res.Err
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for command reply")
		return nil
	}
}

func TestUnit_DockCommand_SucceedsForEveryStaticKind(t *testing.T) {
	t.Parallel()
	for _, tc := range dockTargetCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newDockWorker(t, tc.statics, tc.pos)
			err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
				return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
			})
			require.NoError(t, err)
			snap := w.Snapshot(testSector)
			require.Len(t, snap.Ships, 1)
			ship := snap.Ships[0]
			require.NotNil(t, ship.Docked, "ship must be docked")
			assert.Equal(t, tc.target, *ship.Docked)
			assert.Equal(t, tc.pos, ship.Pos, "ship snaps to target position")
			assert.Equal(t, domain.Vec2{}, ship.Vel)
			assert.Nil(t, ship.Target)
			assert.Nil(t, ship.FinalTarget)
		})
	}
}

func TestUnit_DockCommand_OutOfRangeFails(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0] // station
	// Ship 1000 units away — well outside DockRange=3.
	w := newDockWorker(t, tc.statics, domain.Vec2{X: tc.pos.X + 1000, Y: tc.pos.Y})
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	})
	require.ErrorIs(t, err, sector.ErrDockOutOfRange)
	snap := w.Snapshot(testSector)
	assert.Nil(t, snap.Ships[0].Docked, "ship must remain in space on rejection")
}

func TestUnit_DockCommand_AlreadyDockedFails(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	}))
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	})
	require.ErrorIs(t, err, sector.ErrAlreadyDocked)
}

func TestUnit_DockCommand_TargetNotFoundFails(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{
			PlayerID: 7, ShipID: 1,
			Target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 999},
			Reply:  reply,
		}
	})
	require.ErrorIs(t, err, sector.ErrTargetNotFound)
}

func TestUnit_DockCommand_InvalidKindFails(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{
			PlayerID: 7, ShipID: 1,
			// Containers are not dockable; ship targets are (phase 10.3.24).
			Target: domain.EntityRef{Kind: domain.EntityKindContainer, ID: 1},
			Reply:  reply,
		}
	})
	require.ErrorIs(t, err, sector.ErrInvalidDockTarget)
}

func TestUnit_DockCommand_ForbiddenForOtherPlayer(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 999, ShipID: 1, Target: tc.target, Reply: reply}
	})
	require.ErrorIs(t, err, sector.ErrForbidden)
}

func TestUnit_UndockCommand_ReleasesDockedShip(t *testing.T) {
	t.Parallel()
	for _, tc := range dockTargetCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newDockWorker(t, tc.statics, tc.pos)
			require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
				return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
			}))
			require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
				return sector.UndockCommand{PlayerID: 7, ShipID: 1, Reply: reply}
			}))
			snap := w.Snapshot(testSector)
			assert.Nil(t, snap.Ships[0].Docked)
		})
	}
}

func TestUnit_UndockCommand_NotDockedFails(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.UndockCommand{PlayerID: 7, ShipID: 1, Reply: reply}
	})
	require.ErrorIs(t, err, sector.ErrNotDocked)
}

func TestUnit_MoveCommand_AutoUndocksDockedShip(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	}))
	// MoveCommand on a docked ship now auto-undocks and sets the target.
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.MoveCommand{PlayerID: 7, ShipID: 1, Target: domain.Vec2{X: 1, Y: 1}, Reply: reply}
	})
	require.NoError(t, err)
	snap := w.Snapshot(testSector)
	require.Nil(t, snap.Ships[0].Docked, "ship must be undocked after move command")
}

func TestUnit_SetCourseCommand_AutoUndocksDockedShip(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	w := newDockWorker(t, tc.statics, tc.pos)
	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	}))
	// SetCourseCommand on a docked ship now auto-undocks and sets the course.
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetCourseCommand{
			PlayerID: 7, ShipID: 1,
			Course: &domain.Course{Sector: testSector, Pos: domain.Vec2{X: 1, Y: 1}},
			Reply:  reply,
		}
	})
	require.NoError(t, err)
	snap := w.Snapshot(testSector)
	require.Nil(t, snap.Ships[0].Docked, "ship must be undocked after set-course command")
}

// TestUnit_SetCourseCommand_RequiresAutopilotModule gates the player autopilot
// on an installed up_autopilot module (phase 10.3.11): arming a course without
// the module is rejected and leaves FinalTarget unset, with the module it
// succeeds, and clearing the course (nil) is always allowed.
func TestUnit_SetCourseCommand_RequiresAutopilotModule(t *testing.T) {
	t.Parallel()

	armCourse := func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetCourseCommand{
			PlayerID: 7, ShipID: 1,
			Course: &domain.Course{Sector: testSector, Pos: domain.Vec2{X: 1, Y: 1}},
			Reply:  reply,
		}
	}

	// No module → rejected, course not armed.
	wNoMod := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second}, clock.NewRealClock(), nil,
		[]domain.Ship{{ID: 1, PlayerID: 7, SectorID: testSector, MaxSpeed: 1}},
	)
	require.ErrorIs(t, sendAndWait(t, wNoMod, armCourse), sector.ErrEquipmentRequired)
	require.Nil(t, wNoMod.Snapshot(testSector).Ships[0].FinalTarget,
		"course must not be armed without up_autopilot")

	// With module → accepted, course armed.
	wMod := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second}, clock.NewRealClock(), nil,
		[]domain.Ship{{ID: 1, PlayerID: 7, SectorID: testSector, MaxSpeed: 1,
			Equipment: []domain.InstalledEquipment{{Type: "up_autopilot", Level: 1}}}},
	)
	require.NoError(t, sendAndWait(t, wMod, armCourse))
	require.NotNil(t, wMod.Snapshot(testSector).Ships[0].FinalTarget,
		"course must be armed with up_autopilot")

	// Clearing the course (nil) is allowed even without the module.
	require.NoError(t, sendAndWait(t, wNoMod, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetCourseCommand{PlayerID: 7, ShipID: 1, Course: nil, Reply: reply}
	}))
}

func TestUnit_DockedShip_DoesNotMove(t *testing.T) {
	t.Parallel()
	tc := dockTargetCases()[0]
	// Dock first, then leave a stale Target — the docked ship must stay
	// pinned (movement loop skips it).
	w := newDockWorker(t, tc.statics, tc.pos)
	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	}))
	// Several ticks — position must not drift.
	before := w.Snapshot(testSector).Ships[0].Pos
	for i := 0; i < 5; i++ {
		w.Tick(context.Background())
	}
	after := w.Snapshot(testSector).Ships[0].Pos
	assert.Equal(t, before, after)
}

// ensure errors export from the package so callers can ErrorIs them.
func TestUnit_DockingErrorsAreSentinels(t *testing.T) {
	t.Parallel()
	all := []error{
		sector.ErrAlreadyDocked, sector.ErrNotDocked, sector.ErrTargetSectorMismatch,
		sector.ErrDockOutOfRange, sector.ErrTargetNotFound, sector.ErrInvalidDockTarget,
		sector.ErrShipDocked,
	}
	for _, e := range all {
		require.NotNil(t, e)
		assert.True(t, errors.Is(e, e))
	}
}
