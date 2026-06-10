package sector_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeContainerRepo is a no-DB stand-in for the worker's ContainerRepo.
// It echoes one container per drop so tests can assert what the kill
// handler planned without a Postgres round-trip.
type fakeContainerRepo struct {
	cargoByShip map[domain.ShipID][]domain.CargoItem
	nextID      int64
	killed      []domain.ShipID
	lastDrops   []domain.ContainerDrop
	pickups     []containerPickup
	deleted     []domain.ContainerID
	pickupErr   error
}

type containerPickup struct {
	container domain.ContainerID
	ship      domain.ShipID
}

func (f *fakeContainerRepo) ShipCargo(_ context.Context, ship domain.ShipID) ([]domain.CargoItem, error) {
	return f.cargoByShip[ship], nil
}

func (f *fakeContainerRepo) RecordKill(_ context.Context, victim domain.ShipID, sectorID domain.SectorID, drops []domain.ContainerDrop) ([]domain.Container, error) {
	f.killed = append(f.killed, victim)
	f.lastDrops = drops
	out := make([]domain.Container, 0, len(drops))
	for _, d := range drops {
		f.nextID++
		out = append(out, domain.Container{
			ID:        domain.ContainerID(f.nextID),
			SectorID:  sectorID,
			Pos:       d.Pos,
			ExpiresAt: d.ExpiresAt,
		})
	}
	return out, nil
}

func (f *fakeContainerRepo) Pickup(_ context.Context, container domain.ContainerID, ship domain.ShipID) error {
	if f.pickupErr != nil {
		return f.pickupErr
	}
	f.pickups = append(f.pickups, containerPickup{container: container, ship: ship})
	return nil
}

func (f *fakeContainerRepo) Delete(_ context.Context, id domain.ContainerID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// staticRNG always returns v — used where the drop plan must not branch
// on randomness (regular cargo never rolls).
type staticRNG struct{ v float64 }

func (r staticRNG) Float64() float64 { return r.v }

// killerVictim wires a heavy-laser attacker (id 1) onto a victim (id 2)
// that dies in one tick, plus a worker holding the given container repo.
func killerVictim(t *testing.T, repo sector.ContainerRepo, rng sector.RNG) *sector.Worker {
	t.Helper()
	killer := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 1000
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	victim := laserShip(2, 200, domain.Vec2{X: 30, Y: 0})

	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {killer, victim}},
		sector.WithContainers(repo, nil),
		sector.WithRNG(rng),
	)
}

func TestUnit_KillShip_DropsCargoIntoContainer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repo := &fakeContainerRepo{cargoByShip: map[domain.ShipID][]domain.CargoItem{
		2: {{GoodsType: 7, Quantity: 100}},
	}}
	w := killerVictim(t, repo, staticRNG{v: 0})

	w.Tick(ctx) // killer one-shots the victim; sweep drops its cargo

	snap := w.Snapshot(testSector)
	for _, s := range snap.Ships {
		require.NotEqual(t, domain.ShipID(2), s.ID, "victim must be swept out")
	}
	require.Equal(t, []domain.ShipID{2}, repo.killed)
	require.Len(t, repo.lastDrops, 1)
	require.Equal(t, domain.GoodsTypeID(7), repo.lastDrops[0].GoodsType)
	require.Equal(t, int64(100), repo.lastDrops[0].Quantity)
	require.Len(t, snap.Containers, 1, "one container per surviving stack")
	require.Equal(t, testSector, snap.Containers[0].SectorID)
}

func TestUnit_KillShip_DropsSlavesForPassengerShip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repo := &fakeContainerRepo{} // victim carries no cargo — only passengers
	killer := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 1000
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	victim := laserShip(2, 200, domain.Vec2{X: 30, Y: 0})
	victim.Passengers = 10

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {killer, victim}},
		sector.WithContainers(repo, nil),
		sector.WithRNG(staticRNG{v: 0}), // pct = 20 → floor(10 * 20/100) = 2 slaves
	)

	w.Tick(ctx)

	require.Equal(t, []domain.ShipID{2}, repo.killed)
	require.Len(t, repo.lastDrops, 1, "a passenger ship with no cargo drops just the slaves container")
	require.Equal(t, domain.GoodsTypeID(323), repo.lastDrops[0].GoodsType)
	require.EqualValues(t, 2, repo.lastDrops[0].Quantity)
	require.Len(t, w.Snapshot(testSector).Containers, 1)
}

func TestUnit_KillShip_NoCargoNoContainer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	repo := &fakeContainerRepo{} // victim carries nothing
	w := killerVictim(t, repo, staticRNG{v: 0})

	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	require.Equal(t, []domain.ShipID{2}, repo.killed, "ship still deleted via RecordKill")
	require.Empty(t, repo.lastDrops)
	require.Empty(t, snap.Containers, "no cargo → no container")
}

func TestUnit_KillShip_PublishesEntityKilled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := bus.NewInMemory(8)
	t.Cleanup(b.Close)
	got := make(chan sector.EntityKilledEvent, 1)
	require.NoError(t, b.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			got <- ev
		}
	}))

	killer := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 1000
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	victim := laserShip(2, 200, domain.Vec2{X: 30, Y: 0})
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {killer, victim}},
		sector.WithHandoff(nil, b),
	)

	w.Tick(ctx)

	select {
	case ev := <-got:
		require.Equal(t, domain.EntityKindShip, ev.Victim.Kind)
		require.Equal(t, int64(2), ev.Victim.ID)
		require.Equal(t, testSector, ev.SectorID)
		// Killer attribution (6.3): killer ship has PlayerID 100, victim 200.
		require.Equal(t, domain.PlayerID(100), ev.Killer)
		require.Equal(t, domain.PlayerID(200), ev.VictimPlayer)
		require.False(t, ev.VictimIsSpacesuit, "a normal ship is not a spacesuit")
	case <-time.After(time.Second):
		t.Fatal("entity_killed event was not published")
	}
}

// TestUnit_KillShip_EntityKilledCarriesSpacesuit verifies the kill event flags
// a destroyed spacesuit (phase 10.1) so the respawn handler respawns at home
// instead of dropping another suit.
func TestUnit_KillShip_EntityKilledCarriesSpacesuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := bus.NewInMemory(8)
	t.Cleanup(b.Close)
	got := make(chan sector.EntityKilledEvent, 1)
	require.NoError(t, b.Subscribe(ctx, sector.EntityKilledTopic, func(payload []byte) {
		var ev sector.EntityKilledEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			got <- ev
		}
	}))

	killer := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 1000
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	victim := laserShip(2, 200, domain.Vec2{X: 30, Y: 0})
	victim.IsSpacesuit = true
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {killer, victim}},
		sector.WithHandoff(nil, b),
	)

	w.Tick(ctx)

	select {
	case ev := <-got:
		require.True(t, ev.VictimIsSpacesuit, "destroyed ship was a spacesuit")
		require.Equal(t, domain.PlayerID(200), ev.VictimPlayer)
	case <-time.After(time.Second):
		t.Fatal("entity_killed event was not published")
	}
}

func TestUnit_KillShip_MissileCargoBurnsUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Missile-only hold; a low roll (0.0 → chance 0 < 12) destroys it.
	repo := &fakeContainerRepo{cargoByShip: map[domain.ShipID][]domain.CargoItem{
		2: {{GoodsType: 50, Quantity: 100000}},
	}}
	w := killerVictim(t, repo, staticRNG{v: 0})

	w.Tick(ctx)

	require.Empty(t, repo.lastDrops, "missile stack burns up on a low roll")
	require.Empty(t, w.Snapshot(testSector).Containers)
}
