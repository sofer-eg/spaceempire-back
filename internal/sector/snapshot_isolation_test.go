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

// Isolation guards for the per-tick effect buffers a Snapshot carries
// (TASK-159). Every buffer — laserEffects, missileImpacts, droneImpacts,
// torpedoImpacts — is truncated with [:0] right after publishSnapshotFor, and
// the NEXT event of that kind appends straight back into the same backing
// array. A published snapshot must therefore hold a COPY: an aliased one is
// silently rewritten under its holder a tick later while len() stays put, so
// asserting the LENGTH of a held snapshot proves nothing. Each guard below
// instead produces a second, distinguishable event and asserts the VALUE the
// earlier snapshot still carries. TorpedoImpacts has the same guard in
// TestUnit_Torpedo_SnapshotCarriesFlightAndImpacts (TASK-114).
//
// The guards deliberately keep exactly ONE event in flight per tick: the buffer
// then never grows past a single element, so an aliased snapshot is overwritten
// in place at index 0 instead of surviving a reallocation.

// snapshotWhen returns the sector's current snapshot if it already satisfies
// want, otherwise ticks the worker until one does. Fails the test when nothing
// matches within maxTicks.
func snapshotWhen(t *testing.T, w *sector.Worker, maxTicks int, want func(sector.Snapshot) bool) sector.Snapshot {
	t.Helper()
	for i := 0; ; i++ {
		if snap := w.Snapshot(testSector); want(snap) {
			return snap
		}
		if i == maxTicks {
			t.Fatalf("no snapshot satisfied the condition within %d ticks", maxTicks)
		}
		w.Tick(context.Background())
	}
}

// TestUnit_Snapshot_LaserEffectsAreCopiedNotAliased: a held snapshot keeps the
// beam of the shooter that fired on ITS tick after the engagement is handed
// over to a different shooter. A LaserBeam carries no id, so the attacker is
// what makes the second beam distinguishable by value.
func TestUnit_Snapshot_LaserEffectsAreCopiedNotAliased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	shooterA := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	shooterA.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	target := laserShip(2, 200, domain.Vec2{X: 50, Y: 0})
	// The target has to outlive both engagements: a corpse is swept out of the
	// sector and the second shooter would never get a shot off.
	target.HP, target.MaxHP = 5000, 5000
	shooterB := laserShip(3, 300, domain.Vec2{X: 0, Y: 50}) // in range, not yet firing

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{shooterA, target, shooterB})

	w.Tick(ctx)
	held := w.Snapshot(testSector)
	require.Len(t, held.LaserEffects, 1, "exactly one ship is firing on this tick")
	require.Equal(t, domain.ShipID(1), held.LaserEffects[0].AttackerShipID)

	// Hand the engagement over: both commands are drained at the top of the next
	// tick, before fireLasers, so that tick again records exactly one beam — this
	// time from ship 3.
	require.NoError(t, w.Send(testSector, sector.CeaseFireCommand{PlayerID: 100, ShipID: 1}))
	require.NoError(t, w.Send(testSector, sector.AttackCommand{
		PlayerID: 300, ShipID: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}))
	w.Tick(ctx)

	fresh := w.Snapshot(testSector)
	require.Len(t, fresh.LaserEffects, 1)
	require.Equal(t, domain.ShipID(3), fresh.LaserEffects[0].AttackerShipID,
		"the new shooter's beam is the one in the fresh snapshot")

	require.Equal(t, domain.ShipID(1), held.LaserEffects[0].AttackerShipID,
		"the earlier held snapshot must NOT be rewritten by a later tick's beam")
}

// TestUnit_Snapshot_MissileImpactsAreCopiedNotAliased: a held snapshot keeps the
// impact of the missile that detonated on ITS tick after a second missile lands.
func TestUnit_Snapshot_MissileImpactsAreCopiedNotAliased(t *testing.T) {
	t.Parallel()

	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := missileShip(2, 200, domain.Vec2{X: 100, Y: 0})
	b.HP, b.MaxHP = 5000, 5000 // survives the first hit, so the second missile has a target
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	launch := func() sector.LaunchMissileResult {
		return sendMissile(t, w, sector.LaunchMissileCommand{
			PlayerID: 100, ShipID: 1,
			Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		})
	}
	hasImpact := func(s sector.Snapshot) bool { return len(s.MissileImpacts) > 0 }

	first := launch()
	require.NoError(t, first.Err)
	held := snapshotWhen(t, w, 6, hasImpact)
	require.Len(t, held.MissileImpacts, 1)
	require.Equal(t, first.MissileID, held.MissileImpacts[0].MissileID)

	second := launch()
	require.NoError(t, second.Err)
	require.NotEqual(t, first.MissileID, second.MissileID, "a second, distinguishable missile")

	fresh := snapshotWhen(t, w, 6, hasImpact)
	require.Len(t, fresh.MissileImpacts, 1)
	require.Equal(t, second.MissileID, fresh.MissileImpacts[0].MissileID,
		"the second detonation is the one in the fresh snapshot")

	require.Equal(t, first.MissileID, held.MissileImpacts[0].MissileID,
		"the earlier held snapshot must NOT be rewritten by a later tick's impact")
}

// TestUnit_Snapshot_DroneImpactsAreCopiedNotAliased: a held snapshot keeps the
// impact of the drone that shot on ITS tick after that drone is recalled and a
// replacement opens fire. A recall emits no impact of its own (TASK-152), so the
// only impact that can reach the buffer afterwards belongs to the new drone.
func TestUnit_Snapshot_DroneImpactsAreCopiedNotAliased(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	owner := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	target := droneShip(2, 200, domain.Vec2{X: 40, Y: 0}) // inside the drone's FireRange
	target.HP, target.MaxHP = 5000, 5000                  // outlives both drones
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, []domain.Ship{owner, target})

	hasImpact := func(s sector.Snapshot) bool { return len(s.DroneImpacts) > 0 }

	require.Equal(t, 1, launchDrones(t, w, 100, 1, 2, 1).Spawned)
	held := snapshotWhen(t, w, 10, hasImpact)
	require.Len(t, held.DroneImpacts, 1, "one drone in flight — at most one impact per tick")
	firstDrone := held.DroneImpacts[0].DroneID

	reply := make(chan sector.RecallDronesResult, 1)
	require.NoError(t, w.Send(testSector, sector.RecallDronesCommand{
		PlayerID: 100, ShipID: 1, GoodsType: testDroneGoods, Reply: reply,
	}))
	w.Tick(ctx)
	require.NoError(t, (<-reply).Err)
	require.Empty(t, w.Snapshot(testSector).Drones, "the first drone is out of the fight")

	require.Equal(t, 1, launchDrones(t, w, 100, 1, 2, 1).Spawned)
	fresh := snapshotWhen(t, w, 10, hasImpact)
	require.Len(t, fresh.DroneImpacts, 1)
	require.NotEqual(t, firstDrone, fresh.DroneImpacts[0].DroneID, "a second, distinguishable drone")

	require.Equal(t, firstDrone, held.DroneImpacts[0].DroneID,
		"the earlier held snapshot must NOT be rewritten by a later tick's impact")
}
