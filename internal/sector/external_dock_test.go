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

// exDockWorker builds a worker with a configurable ExternalDockTurns so tests
// can observe the in-progress phase (turns >= 2; with the default 1 the dock
// completes on the same tick the command is applied).
func exDockWorker(t *testing.T, turns int, ships []domain.Ship, hangers sector.HangerStats, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append(opts, sector.WithHangerStats(hangers))
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, DockRange: 3, AOIRadius: 2000, ExternalDockTurns: turns},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		opts...,
	)
}

// exFighter is a fighter fitted with the up_exdocking module.
func exFighter(id domain.ShipID, player domain.PlayerID, pos domain.Vec2) domain.Ship {
	s := fighter(id, player, pos)
	s.Equipment = []domain.InstalledEquipment{{Type: "up_exdocking", Level: 1}}
	return s
}

func externalDock(t *testing.T, w *sector.Worker, player domain.PlayerID, ship, target domain.ShipID) error {
	t.Helper()
	return sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.ExternalDockCommand{
			PlayerID: player, ShipID: ship,
			Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target)},
			Reply:  reply,
		}
	})
}

func TestUnit_ExternalDockCommand_RequiresModule(t *testing.T) {
	t.Parallel()
	// No module → rejected.
	wNoMod := exDockWorker(t, 2, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())
	require.ErrorIs(t, externalDock(t, wNoMod, 7, 1, 2), sector.ErrEquipmentRequired)
	assert.Nil(t, shipByID(t, wNoMod.Snapshot(testSector), 1).ExternalDock)

	// With module → process started (still in progress with turns=2).
	wMod := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())
	require.NoError(t, externalDock(t, wMod, 7, 1, 2))
	ed := shipByID(t, wMod.Snapshot(testSector), 1).ExternalDock
	require.NotNil(t, ed)
	assert.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}, ed.Target)
	assert.Nil(t, shipByID(t, wMod.Snapshot(testSector), 1).Docked, "not docked yet")
}

// TestUnit_ExternalDock_MultiTick_AttachesBypassingHangar: external dock
// attaches to a host with no hangar room — the whole point of up_exdocking.
func TestUnit_ExternalDock_MultiTick_AttachesBypassingHangar(t *testing.T) {
	t.Parallel()
	noHangar := fakeHangers{
		hostClassID:    {Small: 0}, // a normal dock here would be ErrNoHangar
		fighterClassID: {ShipType: 2, ShipSpace: 4},
	}
	w := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, noHangar)

	require.NoError(t, externalDock(t, w, 7, 1, 2)) // tick 1: in progress
	require.NotNil(t, shipByID(t, w.Snapshot(testSector), 1).ExternalDock)

	w.Tick(context.Background()) // tick 2: completion → attach

	snap := w.Snapshot(testSector)
	docked := shipByID(t, snap, 1)
	require.NotNil(t, docked.Docked, "external dock must attach despite no hangar room")
	assert.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}, *docked.Docked)
	assert.Nil(t, docked.ExternalDock, "process cleared on completion")
}

// TestUnit_ExternalDock_CancelledOutOfRange: if the host drifts out of range
// before completion, the process is silently cancelled.
func TestUnit_ExternalDock_CancelledOutOfRange(t *testing.T) {
	t.Parallel()
	movingHost := host(2, 7, domain.Vec2{}, true)
	movingHost.MaxSpeed = 5
	movingHost.Target = &domain.Vec2{X: 1000}

	w := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{}),
		movingHost,
	}, standardHangers())

	require.NoError(t, externalDock(t, w, 7, 1, 2)) // tick 1: host moves out of range
	w.Tick(context.Background())                    // tick 2: completion fails range → cancel

	snap := w.Snapshot(testSector)
	ship := shipByID(t, snap, 1)
	assert.Nil(t, ship.Docked, "must not attach out of range")
	assert.Nil(t, ship.ExternalDock, "process cancelled")
}

func TestUnit_ExternalDock_CancelledByMove(t *testing.T) {
	t.Parallel()
	w := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())

	require.NoError(t, externalDock(t, w, 7, 1, 2))
	require.NotNil(t, shipByID(t, w.Snapshot(testSector), 1).ExternalDock)

	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.MoveCommand{PlayerID: 7, ShipID: 1, Target: domain.Vec2{X: 100}, Reply: reply}
	}))

	snap := w.Snapshot(testSector)
	assert.Nil(t, shipByID(t, snap, 1).ExternalDock, "move cancels the external dock")
	assert.Nil(t, shipByID(t, snap, 1).Docked)
}

func TestUnit_ExternalDockCommand_HostileFails(t *testing.T) {
	t.Parallel()
	rel := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{7, 8}: domain.RelationHostile,
	}}
	w := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{}),
		host(2, 8, domain.Vec2{}, true),
	}, standardHangers(), sector.WithRelations(rel))
	require.ErrorIs(t, externalDock(t, w, 7, 1, 2), sector.ErrDockHostile)
}

func TestUnit_ExternalDockCommand_OutOfRangeAtInitiate(t *testing.T) {
	t.Parallel()
	w := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{X: 1000}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())
	require.ErrorIs(t, externalDock(t, w, 7, 1, 2), sector.ErrDockOutOfRange)
}

func TestUnit_ExternalDockCommand_TargetNotFound(t *testing.T) {
	t.Parallel()
	w := exDockWorker(t, 2, []domain.Ship{exFighter(1, 7, domain.Vec2{})}, standardHangers())
	require.ErrorIs(t, externalDock(t, w, 7, 1, 999), sector.ErrTargetNotFound)
}

func TestUnit_ExternalDockCommand_AlreadyDockedFails(t *testing.T) {
	t.Parallel()
	parked := exFighter(1, 7, domain.Vec2{})
	parked.Docked = &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	w := exDockWorker(t, 2, []domain.Ship{
		parked,
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())
	require.ErrorIs(t, externalDock(t, w, 7, 1, 2), sector.ErrAlreadyDocked)
}

func TestUnit_ExternalDockCommand_ForbiddenForOtherPlayer(t *testing.T) {
	t.Parallel()
	w := exDockWorker(t, 2, []domain.Ship{
		exFighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())
	require.ErrorIs(t, externalDock(t, w, 999, 1, 2), sector.ErrForbidden)
}
