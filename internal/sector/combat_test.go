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

func laserShip(id int64, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID:              domain.ShipID(id),
		PlayerID:        domain.PlayerID(playerID),
		Pos:             pos,
		HP:              50,
		MaxHP:           50,
		Shield:          20,
		MaxShield:       20,
		ShieldRecharge:  0,
		Energy:          100,
		MaxEnergy:       100,
		EnergyRecharge:  0,
		LaserDamage:     10,
		LaserRange:      400,
		LaserEnergyCost: 5,
	}
}

// TestUnit_Tick_ChargesShields walks a ship with Shield=50 / MaxShield=100
// / ShieldRecharge=5 through three ticks and checks the snapshot reflects
// the accumulated 65.
func TestUnit_Tick_ChargesShields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{
			ID:             1,
			Shield:         50,
			MaxShield:      100,
			ShieldRecharge: 5,
			HP:             100,
			MaxHP:          100,
		}},
	)

	for i := 0; i < 3; i++ {
		w.Tick(ctx)
	}

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Ships, 1)
	require.Equal(t, 65, snap.Ships[0].Shield, "shield should accumulate by ShieldRecharge each tick")
}

// TestUnit_Tick_FullShield_NoChange: a ship already at MaxShield must not
// be mutated by the combat phase — the dirty-set stays empty and Shield
// stays at the cap.
func TestUnit_Tick_FullShield_NoChange(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{
			ID:             1,
			Shield:         100,
			MaxShield:      100,
			ShieldRecharge: 5,
			HP:             100,
			MaxHP:          100,
		}},
	)
	w.Tick(ctx)
	snap := w.Snapshot(testSector)
	require.Equal(t, 100, snap.Ships[0].Shield)
}

// TestUnit_Tick_ShieldClampsAtMax: the final tick that would overshoot
// must clamp to MaxShield, not exceed it.
func TestUnit_Tick_ShieldClampsAtMax(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{
			ID:             1,
			Shield:         97,
			MaxShield:      100,
			ShieldRecharge: 5,
			HP:             100,
			MaxHP:          100,
		}},
	)
	w.Tick(ctx)
	snap := w.Snapshot(testSector)
	require.Equal(t, 100, snap.Ships[0].Shield)
}

// TestUnit_Tick_AttackerKillsTarget: two ships in range, attacker has
// laser energy/damage, target dies after enough ticks; AttackTarget is
// cleared and the final beam carries Killed=true.
func TestUnit_Tick_AttackerKillsTarget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	a := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := laserShip(2, 200, domain.Vec2{X: 50, Y: 0})
	a.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)

	// b has 20 shield + 50 HP = 70 total; laser deals 10/tick → 7 ticks.
	var lastKilledBeam bool
	for i := 0; i < 8; i++ {
		w.Tick(ctx)
		snap := w.Snapshot(testSector)
		for _, beam := range snap.LaserEffects {
			if beam.Killed {
				lastKilledBeam = true
			}
		}
	}
	require.True(t, lastKilledBeam, "expected a Killed beam during the engagement")

	snap := w.Snapshot(testSector)
	// 4.6 kill handler: the dead target is swept out of the sector; the
	// attacker remains with its AttackTarget cleared.
	var attacker *domain.Ship
	for i := range snap.Ships {
		switch snap.Ships[i].ID {
		case 1:
			attacker = &snap.Ships[i]
		case 2:
			t.Fatalf("dead target must be removed from the sector, found ship 2: %+v", snap.Ships[i])
		}
	}
	require.NotNil(t, attacker, "attacker must remain in the sector")
	require.Nil(t, attacker.AttackTarget, "AttackTarget must be cleared after target dies")
}

// TestUnit_Tick_TargetOutOfRange_NoShot: attacker keeps AttackTarget set
// but no laser beams are emitted and the target is untouched.
func TestUnit_Tick_TargetOutOfRange_NoShot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	a := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := laserShip(2, 200, domain.Vec2{X: 1000, Y: 0}) // beyond LaserRange=400
	a.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)
	for i := 0; i < 3; i++ {
		w.Tick(ctx)
	}
	snap := w.Snapshot(testSector)
	require.Empty(t, snap.LaserEffects, "no shots when out of range")
	for _, s := range snap.Ships {
		if s.ID == 2 {
			require.Equal(t, 20, s.Shield, "target shield untouched")
			require.Equal(t, 50, s.HP)
		}
		if s.ID == 1 {
			require.NotNil(t, s.AttackTarget, "attacker still aiming, just not firing")
		}
	}
}

// TestUnit_Tick_AttackCommand_RejectsSelf: an AttackCommand whose target
// id equals the ship id replies with ErrInvalidAttackTarget and does
// not arm the laser.
func TestUnit_Tick_AttackCommand_RejectsSelf(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{laserShip(1, 100, domain.Vec2{X: 0, Y: 0})},
	)
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AttackCommand{
		PlayerID: 100,
		ShipID:   1,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 1},
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
	require.Nil(t, w.Snapshot(testSector).Ships[0].AttackTarget)
}

// TestUnit_Tick_CeaseFire_Clears: a CeaseFireCommand drops a previously
// set AttackTarget.
func TestUnit_Tick_CeaseFire_Clears(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	a := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	b := laserShip(2, 200, domain.Vec2{X: 50, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 200},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{a, b},
	)

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.CeaseFireCommand{
		PlayerID: 100,
		ShipID:   1,
		Reply:    reply,
	}))
	w.Tick(ctx)
	res := <-reply
	require.NoError(t, res.Err)

	snap := w.Snapshot(testSector)
	for _, s := range snap.Ships {
		if s.ID == 1 {
			require.Nil(t, s.AttackTarget)
		}
	}
}
