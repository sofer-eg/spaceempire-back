package sector_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// These guard the explicit LastStep=0 at each Pos-mutation chokepoint that
// bypasses moveShip (TASK-119, AC #3). Each starts the ship with a stale
// non-zero LastStep so a future refactor dropping the zeroing is caught — the
// assertion would otherwise pass trivially (LastStep spawns zero).

// TestUnit_ExecuteDock_ZeroesLastStep: docking to a static clears the display
// velocity so a just-arrived ship shows no course vector while parked.
func TestUnit_ExecuteDock_ZeroesLastStep(t *testing.T) {
	t.Parallel()

	tc := dockTargetCases()[0] // station
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, DockRange: 3},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {{
			ID: 1, PlayerID: 7, SectorID: testSector, Pos: tc.pos, MaxSpeed: 0,
			LastStep:  domain.Vec2{X: 9, Y: 9}, // pretend it was moving in
			Equipment: []domain.InstalledEquipment{{Type: "up_autopilot", Level: 1}},
		}}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: tc.statics}),
	)

	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: 7, ShipID: 1, Target: tc.target, Reply: reply}
	}))

	ship := w.Snapshot(testSector).Ships[0]
	require.NotNil(t, ship.Docked, "ship must be docked")
	assert.True(t, ship.LastStep.IsZero(), "docking to a static must clear LastStep")
}

// TestUnit_ApplyShipDock_ZeroesLastStep: docking into a host hangar clears the
// display velocity (carried, not thrusting).
func TestUnit_ApplyShipDock_ZeroesLastStep(t *testing.T) {
	t.Parallel()

	pos := domain.Vec2{X: 50, Y: -20}
	f := fighter(1, 7, pos)
	f.LastStep = domain.Vec2{X: 9, Y: 9} // pretend it was moving in
	w := shipDockWorker(t, []domain.Ship{f, host(2, 7, pos, false)}, standardHangers())

	require.NoError(t, dockToShip(t, w, 7, 1, 2))

	docked := shipByID(t, w.Snapshot(testSector), 1)
	require.NotNil(t, docked.Docked, "ship must be docked to host")
	assert.True(t, docked.LastStep.IsZero(), "ship-to-ship docking must clear LastStep")
}

// TestUnit_CarryDockedShips_ZeroesLastStep: a ship carried along by a moving
// host shows no course vector — applyMovement skips docked ships, so
// carryDockedShips is the only place that can zero a stale LastStep.
func TestUnit_CarryDockedShips_ZeroesLastStep(t *testing.T) {
	t.Parallel()

	start := domain.Vec2{}
	movingHost := host(2, 7, start, true)
	movingHost.MaxSpeed = 5
	movingHost.Target = &domain.Vec2{X: 1000}

	docked := fighter(1, 7, start)
	docked.Docked = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	docked.LastStep = domain.Vec2{X: 9, Y: 9} // stale display vel

	w := shipDockWorker(t, []domain.Ship{docked, movingHost}, standardHangers())
	w.Tick(context.Background())

	snap := w.Snapshot(testSector)
	hostPos := shipByID(t, snap, 2).Pos
	carried := shipByID(t, snap, 1)
	require.NotEqual(t, start, hostPos, "host must have moved")
	assert.Equal(t, hostPos, carried.Pos, "docked ship rides along with host")
	assert.True(t, carried.LastStep.IsZero(), "carried docked ship must show no course vector")
}

// TestUnit_ExecuteJump_ZeroesLastStep: a gate jump is a discrete teleport, so
// the relocated ship (both the persisted row and the over-the-bus JumpEvent)
// carries a zero LastStep — no phantom vector on arrival in the target sector.
func TestUnit_ExecuteJump_ZeroesLastStep(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	w := newJumpWorker(t, repo, b, []domain.Ship{{
		ID: 1, PlayerID: 7, SectorID: 1,
		Pos: domain.Vec2{X: 100, Y: 0}, MaxSpeed: 10,
		LastStep: domain.Vec2{X: 9, Y: 9}, // stale display vel before the jump
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 7, ShipID: 1, GateID: 10})
	require.NoError(t, res.Err)

	require.Len(t, repo.saves, 1)
	assert.True(t, repo.saves[0].LastStep.IsZero(), "persisted relocated ship must have zero LastStep")

	msgs := b.snapshot()
	require.NotEmpty(t, msgs)
	var ev sector.JumpEvent
	require.NoError(t, json.Unmarshal(msgs[0].payload, &ev))
	assert.True(t, ev.Ship.LastStep.IsZero(), "JumpEvent.Ship.LastStep must be zero")
}
