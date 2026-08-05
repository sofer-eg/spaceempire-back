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

// missileShipHP / missileShipShield are the vitals every missile fixture ship
// carries. They were 200/50 while a missile did 30 damage; TASK-175 put every
// class on ct_missiles.power, so the weakest missile now deals 1000 and those
// numbers stopped meeting the helper's own contract — a single hit wiped the
// target, and tests that fire twice at it then failed on the *second* launch with
// ErrInvalidAttackTarget instead of what they were checking. Sized like a real
// hull instead (Разведчик, class 77: hull 4040, shield 6900) so a class-1 hit is a
// dent, not a kill.
const (
	missileShipHP     = 10000
	missileShipShield = 5000
)

// missileShip mirrors laserShip but defaults Energy/Shield/HP big enough
// that a few missile hits do not accidentally wipe the launcher in the
// course of a test.
func missileShip(id int64, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID:        domain.ShipID(id),
		PlayerID:  domain.PlayerID(playerID),
		SectorID:  testSector,
		Pos:       pos,
		Direction: domain.Vec2{X: 1, Y: 0},
		HP:        missileShipHP,
		MaxHP:     missileShipHP,
		Shield:    missileShipShield,
		MaxShield: missileShipShield,
		// up_launcher: phase 10.14b gates missile launch on this module.
		Equipment: []domain.InstalledEquipment{{Type: "up_launcher", Level: 1}},
		// no shield/energy recharge — keeps the math predictable across
		// the few ticks each test runs.
	}
}

// TestUnit_LaunchMissile_RequiresLauncher: a ship without up_launcher is
// refused (phase 10.14b capability gate) and no missile is spawned.
func TestUnit_LaunchMissile_RequiresLauncher(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Equipment = nil // strip the launcher
	b := missileShip(2, 200, domain.Vec2{X: 100, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
	require.Empty(t, w.Snapshot(testSector).Missiles)
}

// TestUnit_LaunchMissile_OK validates the happy path: command arms a
// new missile, snapshot reports it, reply carries a non-zero MissileID.
func TestUnit_LaunchMissile_OK(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	// 500 units out, not 100: a class-1 missile covers ~90 units a tick, so a
	// target within one tick's reach is hit and the missile removed before the
	// snapshot this test reads (TASK-175 raised Speed 80 → 90 and Accel 40 → 108,
	// which saturates the cap on the first tick).
	b := missileShip(2, 200, domain.Vec2{X: 500, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.NoError(t, res.Err)
	require.NotZero(t, res.MissileID)

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Missiles, 1)
	require.Equal(t, res.MissileID, snap.Missiles[0].ID)
	require.Equal(t, domain.ShipID(1), snap.Missiles[0].OwnerShipID)
	require.Equal(t, domain.EntityKindShip, snap.Missiles[0].Target.Kind)
}

// TestUnit_LaunchMissile_ActionEnergy: a launch is an "action" energy expense
// (phase 10.3.1). The first shot debits EnergyCost from the launcher's pool;
// once the pool can no longer cover the cost the next shot is refused with
// ErrNotEnoughEnergy and no energy is spent.
func TestUnit_LaunchMissile_ActionEnergy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Energy = 50
	a.MaxEnergy = 1000
	// Far enough that the first missile is still in flight for the assertion
	// below (see TestUnit_LaunchMissile_OK).
	b := missileShip(2, 200, domain.Vec2{X: 500, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	// First launch: 50 >= 30 → succeeds, energy debited to 20.
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID:   100,
		ShipID:     1,
		Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		EnergyCost: 30,
		Reply:      reply,
	}))
	w.Tick(ctx)
	require.NoError(t, (<-reply).Err)
	require.Len(t, w.Snapshot(testSector).Missiles, 1)
	require.Equal(t, 20, shipEnergyByID(t, w, 1), "first launch debits EnergyCost")

	// Second launch: 20 < 30 → rejected, energy unchanged.
	reply2 := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID:   100,
		ShipID:     1,
		Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		EnergyCost: 30,
		Reply:      reply2,
	}))
	w.Tick(ctx)
	require.ErrorIs(t, (<-reply2).Err, sector.ErrNotEnoughEnergy)
	require.Equal(t, 20, shipEnergyByID(t, w, 1), "rejected launch spends no energy")
}

// shipEnergyByID reads a ship's current Energy from the sector snapshot.
func shipEnergyByID(t *testing.T, w *sector.Worker, id domain.ShipID) int {
	t.Helper()
	for _, s := range w.Snapshot(testSector).Ships {
		if s.ID == id {
			return s.Energy
		}
	}
	t.Fatalf("ship %d not found in snapshot", id)
	return 0
}

// TestUnit_LaunchMissile_HitsTarget runs enough ticks for the missile
// to traverse the gap and land — expects a non-Expired impact and target
// HP/Shield reduced.
func TestUnit_LaunchMissile_HitsTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := missileShip(2, 200, domain.Vec2{X: 100, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:    reply,
	}))

	// Run up to TTL/tick ticks; class-1 TTL=15s, Speed=90 — distance 100 closes
	// within the first tick comfortably.
	var hit bool
	for i := 0; i < 6 && !hit; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if !imp.Expired {
				hit = true
				require.Equal(t, domain.ShipID(1), imp.AttackerShipID)
				require.True(t, imp.Damage > 0)
			}
		}
	}
	require.True(t, hit, "missile must hit a stationary target within a few ticks")

	snap := w.Snapshot(testSector)
	// Missile removed after the hit; target shield/HP reduced from default.
	require.Empty(t, snap.Missiles)
	for _, s := range snap.Ships {
		if s.ID == 2 {
			require.True(t, s.Shield < missileShipShield || s.HP < missileShipHP,
				"target absorbed damage")
		}
	}
}

// TestUnit_LaunchMissile_Expires: the target is so far away that the
// missile cannot reach it before TTL — expect an MissileImpact{Expired:true}
// and the missile removed without applying damage. Uses FakeClock so the
// per-tick `now` advances deterministically past the missile's ExpiresAt.
func TestUnit_LaunchMissile_Expires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	// 10 000 units away — at class-1 Speed=90 and TTL=15s the missile travels at
	// most ~1350 units before timing out.
	b := missileShip(2, 200, domain.Vec2{X: 10000, Y: 0})
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 20000},
		clk,
		nil,
		[]domain.Ship{a, b},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:    reply,
	}))

	var expired bool
	for i := 0; i < 30 && !expired; i++ {
		w.Tick(ctx)
		clk.Advance(time.Second)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if imp.Expired {
				expired = true
			}
		}
	}
	require.True(t, expired, "missile must expire when target is unreachable")
	require.Empty(t, w.Snapshot(testSector).Missiles)
	for _, s := range w.Snapshot(testSector).Ships {
		if s.ID == 2 {
			require.Equal(t, missileShipShield, s.Shield, "target untouched")
			require.Equal(t, missileShipHP, s.HP)
		}
	}
}

// TestUnit_LaunchMissile_RejectsSelfTarget: a self-aimed missile is
// rejected with ErrInvalidAttackTarget and no missile spawns.
func TestUnit_LaunchMissile_RejectsSelfTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{missileShip(1, 100, domain.Vec2{X: 0, Y: 0})},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 1},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
	require.Zero(t, res.MissileID)
	require.Empty(t, w.Snapshot(testSector).Missiles)
}

// TestUnit_LaunchMissile_RejectsNonTargetableKind: a kind that is neither a
// ship nor a destructible static (a container here) is rejected at the command
// boundary (TASK-113 FR-07: missileTargetable). Destructible statics are a
// separate, accepted path — see TestUnit_LaunchMissile_StaticTargetGate.
func TestUnit_LaunchMissile_RejectsNonTargetableKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{missileShip(1, 100, domain.Vec2{X: 0, Y: 0})},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindContainer, ID: 5},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
}

// TestUnit_LaunchMissile_NotOwner: another player cannot launch from
// somebody else's ship.
func TestUnit_LaunchMissile_NotOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := missileShip(2, 200, domain.Vec2{X: 50, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 999, // not the owner of ship 1
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrForbidden)
}

// TestUnit_LaunchMissile_Docked: a docked ship cannot fire.
func TestUnit_LaunchMissile_Docked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Docked = &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	b := missileShip(2, 200, domain.Vec2{X: 50, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrShipDocked)
}

// TestUnit_LaunchMissile_ClassSelectsProfile pins the seam between the command's
// ammunition class and the missile that actually flies: the worker must look the
// class up in combat.DefaultMissileSpec, so a class-5 «Шершень» spawns with 25 000
// damage at Speed 22 and not with the class-1 profile.
//
// This is the wiring the whole per-class feature rests on and it was the one thing
// no test covered while it was wrong: before TASK-175 the worker held a single
// package-level spec, so every launch — whatever the player paid for — flew a
// Москит. Swapping the lookup back to a fixed class here must fail.
func TestUnit_LaunchMissile_ClassSelectsProfile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	// 500 units out: a class-5 missile crawls at 22 units a tick, so it is still
	// airborne in the snapshot below.
	b := missileShip(2, 200, domain.Vec2{X: 500, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Class:    5,
		Reply:    reply,
	}))
	w.Tick(ctx)
	require.NoError(t, (<-reply).Err)

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Missiles, 1)
	// Literal ct_missiles values for class 5, not combat.DefaultMissileSpec(5):
	// asking the catalog what it says would pass even if the worker never consulted
	// the class at all.
	assert.Equal(t, 25000, snap.Missiles[0].Damage, "class-5 damage is ct_missiles.power")
	assert.Equal(t, 22.0, snap.Missiles[0].Speed, "class-5 speed is ct_missiles.speed")
}
