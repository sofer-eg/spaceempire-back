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
		HP:        200,
		MaxHP:     200,
		Shield:    50,
		MaxShield: 50,
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
	b := missileShip(2, 200, domain.Vec2{X: 100, Y: 0})
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

	// Run up to TTL/tick ticks; default TTL=15s, Speed=80 — distance 100
	// closes within 2 ticks comfortably.
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
			require.True(t, s.Shield < 50 || s.HP < 200, "target absorbed damage")
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
	// 10 000 units away — at Speed=80 and TTL=15s the missile travels at
	// most ~1200 units before timing out.
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
			require.Equal(t, 50, s.Shield, "target untouched")
			require.Equal(t, 200, s.HP)
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
