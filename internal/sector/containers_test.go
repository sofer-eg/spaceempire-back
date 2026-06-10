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

func TestUnit_Container_ExpiresOnTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	repo := &fakeContainerRepo{}
	container := domain.Container{
		ID: 7, SectorID: testSector, Pos: domain.Vec2{},
		ExpiresAt: clk.Now().Add(10 * time.Second),
	}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clk, nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: nil},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: {container}}),
	)

	w.Tick(ctx) // t0: still inside TTL
	require.Len(t, w.Snapshot(testSector).Containers, 1)

	clk.Advance(15 * time.Second) // past the 10 s TTL
	w.Tick(ctx)

	require.Empty(t, w.Snapshot(testSector).Containers, "container swept after TTL")
	require.Equal(t, []domain.ContainerID{7}, repo.deleted, "expiry deletes the row immediately")
}

func TestUnit_Worker_ColdStartContainers(t *testing.T) {
	t.Parallel()

	repo := &fakeContainerRepo{}
	containers := []domain.Container{
		{ID: 1, SectorID: testSector, Pos: domain.Vec2{X: 1, Y: 2}, ExpiresAt: time.Now().Add(time.Hour)},
		{ID: 2, SectorID: testSector, Pos: domain.Vec2{X: 3, Y: 4}, ExpiresAt: time.Now().Add(time.Hour)},
	}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: nil},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: containers}),
	)

	require.Len(t, w.Snapshot(testSector).Containers, 2, "loaded containers visible from cold start")
}

func TestUnit_Worker_Subscribe_DeliversContainers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	repo := &fakeContainerRepo{}
	ship := droneShip(1, 7, domain.Vec2{X: 0, Y: 0}) // player 7, AOI centre
	container := domain.Container{
		ID: 7, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 2000, PickupRange: 30},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: {container}}),
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	// The first non-empty patch must carry the existing container.
	var sawAdded bool
	deadline := time.After(2 * time.Second)
	for !sawAdded {
		select {
		case patch := <-sub.Patch:
			for _, c := range patch.ContainersAdded {
				if c.ID == 7 {
					sawAdded = true
				}
			}
		case <-deadline:
			t.Fatal("container not delivered in ContainersAdded")
		}
	}

	// Picking it up must surface a ContainersRemoved delta.
	require.NoError(t, w.Send(testSector, sector.PickupContainerCommand{PlayerID: 7, ShipID: 1, ContainerID: 7}))
	var sawRemoved bool
	deadline = time.After(2 * time.Second)
	for !sawRemoved {
		select {
		case patch := <-sub.Patch:
			for _, id := range patch.ContainersRemoved {
				if id == 7 {
					sawRemoved = true
				}
			}
		case <-deadline:
			t.Fatal("picked-up container not delivered in ContainersRemoved")
		}
	}
}
