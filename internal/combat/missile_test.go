package combat_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

func attackerShip() *domain.Ship {
	return &domain.Ship{
		ID:        7,
		PlayerID:  1,
		SectorID:  1,
		Pos:       domain.Vec2{X: 0, Y: 0},
		Vel:       domain.Vec2{X: 5, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
	}
}

func TestUnit_LaunchMissile_InheritsAttackerKinematics(t *testing.T) {
	t.Parallel()
	a := attackerShip()
	target := domain.EntityRef{Kind: domain.EntityKindShip, ID: 99}
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec(1)

	m := combat.LaunchMissile(42, spec, a, target, domain.Vec2{X: 500, Y: 0}, now)
	require.NotNil(t, m)
	require.Equal(t, domain.MissileID(42), m.ID)
	require.Equal(t, a.SectorID, m.SectorID)
	require.Equal(t, a.ID, m.OwnerShipID)
	require.Equal(t, a.PlayerID, m.PlayerID)
	require.Equal(t, a.Pos, m.Pos)
	require.Equal(t, a.Vel, m.Vel, "missile inherits attacker velocity")
	require.Equal(t, a.Direction, m.Direction)
	require.Equal(t, target, m.Target)
	require.Equal(t, domain.Vec2{X: 500, Y: 0}, m.LastTargetPos)
	require.Equal(t, spec.Damage, m.Damage)
	require.Equal(t, now.Add(spec.TTL), m.ExpiresAt)
}

// TestUnit_LaunchMissile_CopiesClassProfile: the whole per-class feature rests on
// the launch copying the spec's flight magnitudes into the missile, because
// TickMissile reads them from there (TASK-175) — a missile that only carried Damage
// would fly the integrator's defaults. Checked on class 5, the profile furthest
// from class 1.
func TestUnit_LaunchMissile_CopiesClassProfile(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultMissileSpec(5)
	m := combat.LaunchMissile(1, spec, attackerShip(),
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 99},
		domain.Vec2{X: 500, Y: 0}, time.Unix(1_700_000_000, 0))

	require.Equal(t, spec.Damage, m.Damage)
	require.Equal(t, spec.Speed, m.Speed)
	require.Equal(t, spec.Accel, m.Accel)
	require.Equal(t, spec.TurnRate, m.TurnRate)
	require.Equal(t, spec.HitRadius, m.HitRadius)
}

func TestUnit_LaunchMissile_ZeroDirectionFallback(t *testing.T) {
	t.Parallel()
	a := attackerShip()
	a.Direction = domain.Vec2{}
	now := time.Unix(1_700_000_000, 0)
	m := combat.LaunchMissile(1, combat.DefaultMissileSpec(1), a,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 99},
		domain.Vec2{X: 100, Y: 0}, now)
	require.Equal(t, domain.Vec2{X: 1, Y: 0}, m.Direction,
		"a zero-direction attacker spawns a missile pointing along +X")
}

// launchedMissile builds a class-1 missile at the origin flying along +X at the
// given speed, aimed at targetPos. Built through LaunchMissile rather than by hand
// so the flight magnitudes TickMissile reads come from the class profile, the way
// production supplies them.
func launchedMissile(t *testing.T, speed float64, targetPos domain.Vec2, now time.Time) *domain.Missile {
	t.Helper()
	a := attackerShip()
	a.Vel = domain.Vec2{X: speed, Y: 0}
	return combat.LaunchMissile(1, combat.DefaultMissileSpec(1), a,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 99}, targetPos, now)
}

func TestUnit_TickMissile_Expired(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec(1)
	// Launched a full TTL + 1s ago → already past ExpiresAt at `now`.
	m := launchedMissile(t, 0, domain.Vec2{X: 100, Y: 0}, now.Add(-spec.TTL-time.Second))

	out := combat.TickMissile(m, domain.Vec2{X: 100, Y: 0}, true, 1.0, now)
	require.Equal(t, combat.MissileExpired, out)
}

func TestUnit_TickMissile_StraightHit(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec(1)
	targetPos := domain.Vec2{X: 200, Y: 0}
	m := launchedMissile(t, spec.Speed, targetPos, now)

	// Head-on at full speed: with class-1 Speed=90 and dt=1 the missile advances
	// ~90 units a tick (accel 108 immediately re-saturates the cap after friction)
	// and lands on a target 200 units ahead within 3 ticks.
	out := combat.TickMissile(m, targetPos, true, 1.0, now)
	require.Equal(t, combat.MissileKeep, out)
	require.InDelta(t, spec.Speed, m.Pos.X, 1.0)

	hit := false
	for i := 1; i < 3 && !hit; i++ {
		hit = combat.TickMissile(m, targetPos, true, 1.0, now.Add(time.Duration(i)*time.Second)) == combat.MissileHit
	}
	require.True(t, hit, "a straight-flying missile must reach a stationary target")
}

func TestUnit_TickMissile_NoHitOutsideRadius(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	m := launchedMissile(t, 1, domain.Vec2{X: 1000, Y: 0}, now)

	out := combat.TickMissile(m, domain.Vec2{X: 1000, Y: 0}, true, 0.1, now)
	require.Equal(t, combat.MissileKeep, out)
}

func TestUnit_TickMissile_TargetLost_NoHit(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec(1)
	m := launchedMissile(t, spec.Speed, domain.Vec2{X: 100, Y: 0}, now)
	m.Pos = domain.Vec2{X: 95, Y: 0} // right on top of the last known position

	// targetAlive=false → even though we are within HitRadius of
	// LastTargetPos, the missile must NOT register a hit.
	out := combat.TickMissile(m, domain.Vec2{}, false, 0.05, now)
	require.Equal(t, combat.MissileKeep, out)
}

func TestUnit_TickMissile_InstantTurnWhenAgile(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	m := launchedMissile(t, 0, domain.Vec2{X: 0, Y: 100}, now)
	m.Vel = domain.Vec2{}
	m.TurnRate = 4 * math.Pi // dt=1 → 4π rad → always snap

	combat.TickMissile(m, domain.Vec2{X: 0, Y: 100}, true, 1.0, now)
	require.InDelta(t, 0.0, m.Direction.X, 1e-6,
		"after instant turn missile points at +Y")
	require.InDelta(t, 1.0, m.Direction.Y, 1e-6)
}

func TestUnit_TickMissile_GradualTurn(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	m := launchedMissile(t, 0, domain.Vec2{X: 0, Y: 100}, now)
	m.Vel = domain.Vec2{}
	m.TurnRate = math.Pi / 8 // 22.5°/s — needs several ticks for 90°

	combat.TickMissile(m, domain.Vec2{X: 0, Y: 100}, true, 1.0, now)
	// After one tick Direction must have rotated counter-clockwise by ~22.5°,
	// i.e. (cos 22.5°, sin 22.5°).
	require.InDelta(t, math.Cos(math.Pi/8), m.Direction.X, 1e-6)
	require.InDelta(t, math.Sin(math.Pi/8), m.Direction.Y, 1e-6)
}

// TestUnit_TickMissile_HeavyClassFliesItsOwnProfile is the regression test for the
// bug TASK-175 fixed at the root: TickMissile used to take a spec argument and the
// sector tick passed one package-level spec, so a class-5 «Шершень» flew at the
// class-1 Speed of 90. Two missiles of different classes, ticked side by side,
// must advance by their own speeds.
func TestUnit_TickMissile_HeavyClassFliesItsOwnProfile(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	targetPos := domain.Vec2{X: 5000, Y: 0}
	a := attackerShip()
	a.Vel = domain.Vec2{}
	target := domain.EntityRef{Kind: domain.EntityKindShip, ID: 99}

	light := combat.LaunchMissile(1, combat.DefaultMissileSpec(1), a, target, targetPos, now)
	heavy := combat.LaunchMissile(2, combat.DefaultMissileSpec(5), a, target, targetPos, now)

	// Two ticks so both have saturated their own speed cap.
	for i := 0; i < 2; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		require.Equal(t, combat.MissileKeep, combat.TickMissile(light, targetPos, true, 1.0, at))
		require.Equal(t, combat.MissileKeep, combat.TickMissile(heavy, targetPos, true, 1.0, at))
	}
	require.InDelta(t, combat.DefaultMissileSpec(1).Speed, light.Vel.Length(), 0.5)
	require.InDelta(t, combat.DefaultMissileSpec(5).Speed, heavy.Vel.Length(), 0.5)
	require.Greater(t, light.Pos.X, heavy.Pos.X*2,
		"the class-1 missile must have outrun the class-5 one, not matched it")
}
