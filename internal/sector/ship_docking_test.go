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

// fakeHangers is a sector.HangerStats resolver keyed by ship-class id.
type fakeHangers map[domain.ShipClassID]domain.Hanger

func (f fakeHangers) HangerOf(id domain.ShipClassID) domain.Hanger { return f[id] }

const (
	hostClassID    domain.ShipClassID = 100 // carrier with a small-ship hangar
	fighterClassID domain.ShipClassID = 200 // small fighter occupying a small slot
)

// standardHangers: host (100) carries 20 units in its small hangar; a fighter
// (200) occupies the small slot with a footprint of 4.
func standardHangers() fakeHangers {
	return fakeHangers{
		hostClassID:    {Small: 20},
		fighterClassID: {ShipType: 2, ShipSpace: 4},
	}
}

func shipDockWorker(t *testing.T, ships []domain.Ship, hangers sector.HangerStats, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append(opts, sector.WithHangerStats(hangers))
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, DockRange: 3, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		opts...,
	)
}

// fighter is a small player ship at pos, eligible to dock into a host hangar.
func fighter(id domain.ShipID, player domain.PlayerID, pos domain.Vec2) domain.Ship {
	return domain.Ship{ID: id, PlayerID: player, ShipClassID: fighterClassID, SectorID: testSector, Pos: pos}
}

// host is a carrier ship at pos with a small hangar; open controls whether
// outsiders may dock.
func host(id domain.ShipID, player domain.PlayerID, pos domain.Vec2, open bool) domain.Ship {
	return domain.Ship{
		ID: id, PlayerID: player, ShipClassID: hostClassID, SectorID: testSector,
		Pos: pos, IsOpen: open,
	}
}

func dockToShip(t *testing.T, w *sector.Worker, player domain.PlayerID, ship, target domain.ShipID) error {
	t.Helper()
	return sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{
			PlayerID: player, ShipID: ship,
			Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target)},
			Reply:  reply,
		}
	})
}

func TestUnit_DockCommand_ShipToShip_Success(t *testing.T) {
	t.Parallel()
	pos := domain.Vec2{X: 50, Y: -20}
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, pos),
		host(2, 7, pos, false), // own ship — closed hangar still allowed
	}, standardHangers())

	require.NoError(t, dockToShip(t, w, 7, 1, 2))

	snap := w.Snapshot(testSector)
	docked := shipByID(t, snap, 1)
	require.NotNil(t, docked.Docked)
	assert.Equal(t, domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}, *docked.Docked)
	assert.Equal(t, pos, docked.Pos)
	assert.Equal(t, domain.Vec2{}, docked.Vel)
	assert.Nil(t, docked.Target)
	assert.Nil(t, docked.FinalTarget)
	// Host is unaffected.
	assert.Nil(t, shipByID(t, snap, 2).Docked)
}

func TestUnit_DockCommand_ShipToShip_OutOfRange(t *testing.T) {
	t.Parallel()
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{X: 1000}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())

	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrDockOutOfRange)
	assert.Nil(t, shipByID(t, w.Snapshot(testSector), 1).Docked)
}

func TestUnit_DockCommand_ShipToShip_Self(t *testing.T) {
	t.Parallel()
	w := shipDockWorker(t, []domain.Ship{fighter(1, 7, domain.Vec2{})}, standardHangers())
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 1), sector.ErrDockSelf)
}

func TestUnit_DockCommand_ShipToShip_TargetNotFound(t *testing.T) {
	t.Parallel()
	w := shipDockWorker(t, []domain.Ship{fighter(1, 7, domain.Vec2{})}, standardHangers())
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 999), sector.ErrTargetNotFound)
}

func TestUnit_DockCommand_ShipToShip_ClosedHostOtherPlayerFails(t *testing.T) {
	t.Parallel()
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 8, domain.Vec2{}, false), // other player's closed hangar
	}, standardHangers())
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrDockNotOpen)
}

func TestUnit_DockCommand_ShipToShip_OpenHostOtherPlayerSucceeds(t *testing.T) {
	t.Parallel()
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 8, domain.Vec2{}, true), // other player's open hangar
	}, standardHangers())
	require.NoError(t, dockToShip(t, w, 7, 1, 2))
}

func TestUnit_DockCommand_ShipToShip_HostileFails(t *testing.T) {
	t.Parallel()
	rel := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{7, 8}: domain.RelationHostile,
	}}
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 8, domain.Vec2{}, true),
	}, standardHangers(), sector.WithRelations(rel))
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrDockHostile)
}

func TestUnit_DockCommand_ShipToShip_NoHangarOnHost(t *testing.T) {
	t.Parallel()
	hangers := fakeHangers{
		hostClassID:    {Small: 0}, // host has no small hangar
		fighterClassID: {ShipType: 2, ShipSpace: 4},
	}
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, hangers)
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrNoHangar)
}

func TestUnit_DockCommand_ShipToShip_NoFootprintFails(t *testing.T) {
	t.Parallel()
	hangers := fakeHangers{
		hostClassID:    {Small: 20},
		fighterClassID: {ShipType: 0}, // ship cannot be carried in any hangar
	}
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, hangers)
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrNoHangar)
}

func TestUnit_DockCommand_ShipToShip_HangarFull(t *testing.T) {
	t.Parallel()
	hangers := fakeHangers{
		hostClassID:    {Small: 6}, // room for one fighter (4), not two
		fighterClassID: {ShipType: 2, ShipSpace: 4},
	}
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
		// ship 3 already parked in host 2's hangar — consumes 4 of 6.
		{
			ID: 3, PlayerID: 7, ShipClassID: fighterClassID, SectorID: testSector,
			Docked: &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		},
	}, hangers)
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrHangarFull)
}

func TestUnit_DockCommand_ShipToShip_DisabledWithoutResolver(t *testing.T) {
	t.Parallel()
	// No WithHangerStats option — ship-to-ship docking is off.
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, DockRange: 3, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			fighter(1, 7, domain.Vec2{}),
			host(2, 7, domain.Vec2{}, true),
		}},
	)
	require.ErrorIs(t, dockToShip(t, w, 7, 1, 2), sector.ErrNoHangar)
}

func TestUnit_UndockCommand_ShipToShip_Releases(t *testing.T) {
	t.Parallel()
	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		host(2, 7, domain.Vec2{}, true),
	}, standardHangers())
	require.NoError(t, dockToShip(t, w, 7, 1, 2))
	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.UndockCommand{PlayerID: 7, ShipID: 1, Reply: reply}
	}))
	assert.Nil(t, shipByID(t, w.Snapshot(testSector), 1).Docked)
}

// TestUnit_CarryDockedShips_RideAlong: a ship docked to a moving host is
// carried to the host's position each tick.
func TestUnit_CarryDockedShips_RideAlong(t *testing.T) {
	t.Parallel()
	movingHost := host(2, 7, domain.Vec2{}, true)
	movingHost.MaxSpeed = 5
	movingHost.Target = &domain.Vec2{X: 1000}

	w := shipDockWorker(t, []domain.Ship{
		fighter(1, 7, domain.Vec2{}),
		movingHost,
	}, standardHangers())

	require.NoError(t, dockToShip(t, w, 7, 1, 2))
	w.Tick(context.Background())

	snap := w.Snapshot(testSector)
	hostPos := shipByID(t, snap, 2).Pos
	dockedPos := shipByID(t, snap, 1).Pos
	require.NotEqual(t, domain.Vec2{}, hostPos, "host must have moved")
	assert.Equal(t, hostPos, dockedPos, "docked ship rides along with host")
}

func shipByID(t *testing.T, snap sector.Snapshot, id domain.ShipID) domain.Ship {
	t.Helper()
	for _, s := range snap.Ships {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("ship %d not in snapshot", id)
	return domain.Ship{}
}
