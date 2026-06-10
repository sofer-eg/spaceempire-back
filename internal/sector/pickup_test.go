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

func pickupWorker(t *testing.T, repo sector.ContainerRepo, containerPos domain.Vec2) *sector.Worker {
	t.Helper()
	ship := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	container := domain.Container{
		ID:        7,
		SectorID:  testSector,
		Pos:       containerPos,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000, PickupRange: 30},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: {container}}),
	)
}

func sendPickup(t *testing.T, w *sector.Worker, player domain.PlayerID, ship domain.ShipID, container domain.ContainerID) sector.CmdResult {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.PickupContainerCommand{
		PlayerID:    player,
		ShipID:      ship,
		ContainerID: container,
		Reply:       reply,
	}))
	w.Tick(context.Background())
	return <-reply
}

func TestUnit_PickupContainer_InRange(t *testing.T) {
	t.Parallel()

	repo := &fakeContainerRepo{}
	w := pickupWorker(t, repo, domain.Vec2{X: 10, Y: 0}) // dist 10 <= PickupRange 30

	res := sendPickup(t, w, 100, 1, 7)

	require.NoError(t, res.Err)
	require.Equal(t, []containerPickup{{container: 7, ship: 1}}, repo.pickups)
	require.Empty(t, w.Snapshot(testSector).Containers, "picked-up container leaves the sector")
}

func TestUnit_PickupContainer_OutOfRange(t *testing.T) {
	t.Parallel()

	repo := &fakeContainerRepo{}
	w := pickupWorker(t, repo, domain.Vec2{X: 100, Y: 0}) // dist 100 > PickupRange 30

	res := sendPickup(t, w, 100, 1, 7)

	require.ErrorIs(t, res.Err, sector.ErrContainerOutOfRange)
	require.Empty(t, repo.pickups, "no DB pickup when out of range")
	require.Len(t, w.Snapshot(testSector).Containers, 1, "container stays in the sector")
}

func TestUnit_PickupContainer_NotFound(t *testing.T) {
	t.Parallel()

	repo := &fakeContainerRepo{}
	w := pickupWorker(t, repo, domain.Vec2{X: 10, Y: 0})

	res := sendPickup(t, w, 100, 1, 999) // no such container

	require.ErrorIs(t, res.Err, sector.ErrContainerNotFound)
	require.Empty(t, repo.pickups)
}

func TestUnit_PickupContainer_NotOwner(t *testing.T) {
	t.Parallel()

	repo := &fakeContainerRepo{}
	w := pickupWorker(t, repo, domain.Vec2{X: 10, Y: 0})

	res := sendPickup(t, w, 999, 1, 7) // wrong player

	require.ErrorIs(t, res.Err, sector.ErrForbidden)
	require.Empty(t, repo.pickups)
	require.Len(t, w.Snapshot(testSector).Containers, 1)
}
