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

// droneShip is a sturdy ship for drone tests: enough HP/Shield that a
// few drone hits do not wipe it mid-test.
func droneShip(id int64, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID:        domain.ShipID(id),
		PlayerID:  domain.PlayerID(playerID),
		SectorID:  testSector,
		Pos:       pos,
		Direction: domain.Vec2{X: 1, Y: 0},
		HP:        300,
		MaxHP:     300,
		Shield:    100,
		MaxShield: 100,
		// up_drone_control: phase 10.14b gates drones on this module and caps
		// the live count at its level. Level 8 leaves headroom for the
		// multi-drone salvo tests; the cap itself is covered by a dedicated test.
		Equipment: []domain.InstalledEquipment{{Type: "up_drone_control", Level: 8}},
	}
}

func launchDrones(t *testing.T, w *sector.Worker, player domain.PlayerID, ship domain.ShipID, target int64, count int) sector.LaunchDroneResult {
	t.Helper()
	reply := make(chan sector.LaunchDroneResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchDroneCommand{
		PlayerID: player,
		ShipID:   ship,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: target},
		Count:    count,
		Reply:    reply,
	}))
	w.Tick(context.Background())
	return <-reply
}

// TestUnit_LaunchDrone_OK spawns a salvo and verifies the snapshot
// reports exactly Count drones owned by the launcher.
func TestUnit_LaunchDrone_OK(t *testing.T) {
	t.Parallel()
	a := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 200, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	res := launchDrones(t, w, 100, 1, 2, 3)
	require.NoError(t, res.Err)
	require.Equal(t, 3, res.Spawned)

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Drones, 3)
	for _, d := range snap.Drones {
		require.Equal(t, domain.ShipID(1), d.OwnerShipID)
		require.Equal(t, domain.EntityKindShip, d.Target.Kind)
		require.NotZero(t, d.ID)
	}
}

// TestUnit_Drone_AttacksTarget: a drone launched at a nearby enemy ship
// chips its shield/HP down and emits a damage-carrying DroneImpact.
func TestUnit_Drone_AttacksTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 40, Y: 0}) // inside FireRange
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	require.NoError(t, launchDrones(t, w, 100, 1, 2, 1).Err)

	var fired bool
	for i := 0; i < 5 && !fired; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).DroneImpacts {
			if !imp.Expired && imp.Damage > 0 {
				fired = true
				require.Equal(t, domain.ShipID(1), imp.OwnerShipID)
			}
		}
	}
	require.True(t, fired, "drone must fire at a nearby target")

	for _, s := range w.Snapshot(testSector).Ships {
		if s.ID == 2 {
			require.True(t, s.Shield < 100 || s.HP < 300, "target absorbed drone damage")
		}
	}
}

// TestUnit_Drone_SelfDestructsOnOwnerDeath: once the owner ship dies, its
// drones disappear (SP Orders=8) and a one-frame Expired impact fires.
func TestUnit_Drone_SelfDestructsOnOwnerDeath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Owner survives one laser hit then dies on the second.
	owner := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	owner.HP = 150
	owner.Shield = 0
	owner.MaxShield = 0

	// Killer trains a heavy laser on the owner.
	killer := droneShip(2, 200, domain.Vec2{X: 30, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 100
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 1} // aim at the owner

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, []domain.Ship{owner, killer})

	// Launch + first laser tick: owner 150 -> 50, drone is alive.
	res := launchDrones(t, w, 100, 1, 2, 2)
	require.NoError(t, res.Err)
	require.Equal(t, 2, res.Spawned)
	require.Len(t, w.Snapshot(testSector).Drones, 2)

	// Second tick: owner 50 -> dead, drones self-destruct.
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	require.Empty(t, snap.Drones, "drones vanish when their owner dies")
	var expired int
	for _, imp := range snap.DroneImpacts {
		if imp.Expired {
			expired++
		}
	}
	require.Equal(t, 2, expired, "each lost drone emits an Expired impact")
}

// TestUnit_RecallDrones returns every live drone to cargo and clears the
// live set.
func TestUnit_RecallDrones(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 500, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	require.Equal(t, 4, launchDrones(t, w, 100, 1, 2, 4).Spawned)

	reply := make(chan sector.RecallDronesResult, 1)
	require.NoError(t, w.Send(testSector, sector.RecallDronesCommand{
		PlayerID: 100, ShipID: 1, Reply: reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.NoError(t, res.Err)
	require.Equal(t, 4, res.Recalled)
	require.Empty(t, w.Snapshot(testSector).Drones)
}

// TestUnit_Drone_ExpiresOnTTL: with the clock advanced past the drone's
// TTL it self-destructs even with a live target it cannot reach.
func TestUnit_Drone_ExpiresOnTTL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	a := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 100000, Y: 0}) // unreachable
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 200000},
		clk, nil, []domain.Ship{a, b})

	require.Equal(t, 1, launchDrones(t, w, 100, 1, 2, 1).Spawned)

	var expired bool
	for i := 0; i < 20 && !expired; i++ {
		clk.Advance(10 * time.Second) // 20×10s = 200s > TTL 120s
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).DroneImpacts {
			if imp.Expired {
				expired = true
			}
		}
	}
	require.True(t, expired, "drone self-destructs at TTL")
	require.Empty(t, w.Snapshot(testSector).Drones)
}

func TestUnit_LaunchDrone_Rejects(t *testing.T) {
	t.Parallel()
	a := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 50, Y: 0})

	t.Run("self target", func(t *testing.T) {
		w := newSingleSectorWorker(t, sector.Config{TickInterval: time.Second},
			clock.NewRealClock(), nil, []domain.Ship{a})
		require.ErrorIs(t, launchDrones(t, w, 100, 1, 1, 1).Err, sector.ErrInvalidAttackTarget)
	})
	t.Run("not owner", func(t *testing.T) {
		w := newSingleSectorWorker(t, sector.Config{TickInterval: time.Second},
			clock.NewRealClock(), nil, []domain.Ship{a, b})
		require.ErrorIs(t, launchDrones(t, w, 999, 1, 2, 1).Err, sector.ErrForbidden)
	})
	t.Run("zero count", func(t *testing.T) {
		w := newSingleSectorWorker(t, sector.Config{TickInterval: time.Second},
			clock.NewRealClock(), nil, []domain.Ship{a, b})
		require.ErrorIs(t, launchDrones(t, w, 100, 1, 2, 0).Err, sector.ErrInvalidAttackTarget)
	})
	t.Run("no drone control module", func(t *testing.T) {
		noMod := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
		noMod.Equipment = nil
		w := newSingleSectorWorker(t, sector.Config{TickInterval: time.Second},
			clock.NewRealClock(), nil, []domain.Ship{noMod, b})
		require.ErrorIs(t, launchDrones(t, w, 100, 1, 2, 1).Err, sector.ErrEquipmentRequired)
	})
}

// TestUnit_LaunchDrone_CapByModuleLevel: the up_drone_control level limits
// how many drones may fly at once. A level-2 ship spawns at most 2 of a
// 5-drone request, and a follow-up salvo is refused until some are recalled.
func TestUnit_LaunchDrone_CapByModuleLevel(t *testing.T) {
	t.Parallel()
	a := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Equipment = []domain.InstalledEquipment{{Type: "up_drone_control", Level: 2}}
	b := droneShip(2, 200, domain.Vec2{X: 200, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	// Request 5 but the level-2 cap allows only 2.
	res := launchDrones(t, w, 100, 1, 2, 5)
	require.NoError(t, res.Err)
	require.Equal(t, 2, res.Spawned)
	require.Len(t, w.Snapshot(testSector).Drones, 2)

	// At cap → a further launch is refused.
	require.ErrorIs(t, launchDrones(t, w, 100, 1, 2, 1).Err, sector.ErrDroneCapReached)
}
