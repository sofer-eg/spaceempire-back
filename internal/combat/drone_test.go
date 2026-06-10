package combat_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

func ownerShip() *domain.Ship {
	return &domain.Ship{
		ID:        7,
		PlayerID:  1,
		SectorID:  1,
		Pos:       domain.Vec2{X: 0, Y: 0},
		Vel:       domain.Vec2{X: 5, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
	}
}

func TestUnit_LaunchDrone_InheritsOwnerKinematics(t *testing.T) {
	t.Parallel()
	o := ownerShip()
	target := domain.EntityRef{Kind: domain.EntityKindShip, ID: 99}
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultDroneSpec()

	d := combat.LaunchDrone(42, spec, o, target, now)
	require.NotNil(t, d)
	require.Equal(t, domain.DroneID(42), d.ID)
	require.Equal(t, o.SectorID, d.SectorID)
	require.Equal(t, o.ID, d.OwnerShipID)
	require.Equal(t, o.PlayerID, d.PlayerID)
	require.Equal(t, o.Pos, d.Pos)
	require.Equal(t, o.Vel, d.Vel)
	require.Equal(t, o.Direction, d.Direction)
	require.Equal(t, target, d.Target)
	require.Equal(t, spec.HP, d.HP)
	require.Equal(t, spec.Damage, d.Damage)
	require.Equal(t, now.Add(spec.TTL), d.ExpiresAt)
}

func TestUnit_LaunchDrone_ZeroDirectionFallback(t *testing.T) {
	t.Parallel()
	o := ownerShip()
	o.Direction = domain.Vec2{}
	now := time.Unix(1_700_000_000, 0)
	d := combat.LaunchDrone(1, combat.DefaultDroneSpec(), o,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 99}, now)
	require.Equal(t, domain.Vec2{X: 1, Y: 0}, d.Direction)
}

func TestUnit_TickDrone_Expired(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	d := &domain.Drone{
		Direction: domain.Vec2{X: 1, Y: 0},
		ExpiresAt: now.Add(-time.Second),
	}
	out := combat.TickDrone(d, domain.Vec2{X: 100, Y: 0}, combat.DefaultDroneSpec(), 1, now)
	require.Equal(t, combat.DroneExpired, out)
}

func TestUnit_TickDrone_NilExpired(t *testing.T) {
	t.Parallel()
	out := combat.TickDrone(nil, domain.Vec2{}, combat.DefaultDroneSpec(), 1, time.Now())
	require.Equal(t, combat.DroneExpired, out)
}

// A drone that starts at rest far from its target should close the
// distance over successive ticks (range strictly decreases) — proves the
// turn+accelerate path works.
func TestUnit_TickDrone_ClosesOnDistantTarget(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultDroneSpec()
	d := &domain.Drone{
		Pos:       domain.Vec2{X: 0, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
		ExpiresAt: now.Add(spec.TTL),
	}
	dest := domain.Vec2{X: 1000, Y: 0}

	prev := dest.Sub(d.Pos).Length()
	for i := 0; i < 10; i++ {
		out := combat.TickDrone(d, dest, spec, 1, now)
		require.Equal(t, combat.DroneKeep, out)
		cur := dest.Sub(d.Pos).Length()
		require.Less(t, cur, prev, "drone should keep closing the gap")
		prev = cur
	}
}

// Once inside StandoffRange the drone must brake: its speed should not
// keep climbing toward Speed the way it does on the open approach. We
// drop it in already moving fast and confirm it sheds velocity.
func TestUnit_TickDrone_BrakesWithinStandoff(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultDroneSpec()
	dest := domain.Vec2{X: 0, Y: 0}
	d := &domain.Drone{
		Pos:       domain.Vec2{X: 10, Y: 0}, // inside StandoffRange (50)
		Vel:       domain.Vec2{X: -spec.Speed, Y: 0},
		Direction: domain.Vec2{X: -1, Y: 0},
		ExpiresAt: now.Add(spec.TTL),
	}
	before := d.Vel.Length()
	out := combat.TickDrone(d, dest, spec, 1, now)
	require.Equal(t, combat.DroneKeep, out)
	require.Less(t, d.Vel.Length(), before, "drone brakes inside the standoff ring")
}

func TestUnit_DroneCanFire(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultDroneSpec()
	d := &domain.Drone{
		Pos:       domain.Vec2{X: 0, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
	}

	require.True(t, combat.DroneCanFire(d, domain.Vec2{X: spec.FireRange - 1, Y: 0}, spec),
		"target in front and inside FireRange")
	require.False(t, combat.DroneCanFire(d, domain.Vec2{X: spec.FireRange + 50, Y: 0}, spec),
		"target out of FireRange")
	require.False(t, combat.DroneCanFire(d, domain.Vec2{X: -(spec.FireRange - 1), Y: 0}, spec),
		"target behind the drone's nose")
	require.False(t, combat.DroneCanFire(nil, domain.Vec2{}, spec))
}
