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
	spec := combat.DefaultMissileSpec()

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

func TestUnit_LaunchMissile_ZeroDirectionFallback(t *testing.T) {
	t.Parallel()
	a := attackerShip()
	a.Direction = domain.Vec2{}
	now := time.Unix(1_700_000_000, 0)
	m := combat.LaunchMissile(1, combat.DefaultMissileSpec(), a,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 99},
		domain.Vec2{X: 100, Y: 0}, now)
	require.Equal(t, domain.Vec2{X: 1, Y: 0}, m.Direction,
		"a zero-direction attacker spawns a missile pointing along +X")
}

func TestUnit_TickMissile_Expired(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	m := &domain.Missile{
		Pos:       domain.Vec2{X: 0, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
		ExpiresAt: now.Add(-time.Second),
	}
	out := combat.TickMissile(m, domain.Vec2{X: 100, Y: 0}, true,
		combat.DefaultMissileSpec(), 1.0, now)
	require.Equal(t, combat.MissileExpired, out)
}

func TestUnit_TickMissile_StraightHit(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec()
	m := &domain.Missile{
		Pos:           domain.Vec2{X: 0, Y: 0},
		Vel:           domain.Vec2{X: spec.Speed, Y: 0},
		Direction:     domain.Vec2{X: 1, Y: 0},
		Target:        domain.EntityRef{Kind: domain.EntityKindShip, ID: 99},
		LastTargetPos: domain.Vec2{X: 100, Y: 0},
		ExpiresAt:     now.Add(spec.TTL),
	}
	// Head-on at full speed: with Speed=80 and dt=1 the missile should
	// hit a target 100 units ahead within 2 ticks.
	out := combat.TickMissile(m, domain.Vec2{X: 100, Y: 0}, true, spec, 1.0, now)
	require.Equal(t, combat.MissileKeep, out)
	require.InDelta(t, 80.0, m.Pos.X, 1.0)

	out = combat.TickMissile(m, domain.Vec2{X: 100, Y: 0}, true, spec, 1.0, now.Add(time.Second))
	require.Equal(t, combat.MissileHit, out)
}

func TestUnit_TickMissile_NoHitOutsideRadius(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec()
	m := &domain.Missile{
		Pos:           domain.Vec2{X: 0, Y: 0},
		Vel:           domain.Vec2{X: 1, Y: 0},
		Direction:     domain.Vec2{X: 1, Y: 0},
		LastTargetPos: domain.Vec2{X: 1000, Y: 0},
		ExpiresAt:     now.Add(spec.TTL),
	}
	out := combat.TickMissile(m, domain.Vec2{X: 1000, Y: 0}, true, spec, 0.1, now)
	require.Equal(t, combat.MissileKeep, out)
}

func TestUnit_TickMissile_TargetLost_NoHit(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec()
	m := &domain.Missile{
		Pos:           domain.Vec2{X: 95, Y: 0},
		Vel:           domain.Vec2{X: spec.Speed, Y: 0},
		Direction:     domain.Vec2{X: 1, Y: 0},
		LastTargetPos: domain.Vec2{X: 100, Y: 0},
		ExpiresAt:     now.Add(spec.TTL),
	}
	// targetAlive=false → even though we are within HitRadius of
	// LastTargetPos, the missile must NOT register a hit.
	out := combat.TickMissile(m, domain.Vec2{}, false, spec, 0.05, now)
	require.Equal(t, combat.MissileKeep, out)
}

func TestUnit_TickMissile_InstantTurnWhenAgile(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec()
	spec.TurnRate = 4 * math.Pi // dt=1 → 4π rad → always snap
	m := &domain.Missile{
		Pos:           domain.Vec2{X: 0, Y: 0},
		Vel:           domain.Vec2{X: 0, Y: 0},
		Direction:     domain.Vec2{X: 1, Y: 0},
		LastTargetPos: domain.Vec2{X: 0, Y: 100},
		ExpiresAt:     now.Add(spec.TTL),
	}
	combat.TickMissile(m, domain.Vec2{X: 0, Y: 100}, true, spec, 1.0, now)
	require.InDelta(t, 0.0, m.Direction.X, 1e-6,
		"after instant turn missile points at +Y")
	require.InDelta(t, 1.0, m.Direction.Y, 1e-6)
}

func TestUnit_TickMissile_GradualTurn(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	spec := combat.DefaultMissileSpec()
	spec.TurnRate = math.Pi / 8 // 22.5°/s — needs several ticks for 90°
	m := &domain.Missile{
		Pos:           domain.Vec2{X: 0, Y: 0},
		Vel:           domain.Vec2{X: 0, Y: 0},
		Direction:     domain.Vec2{X: 1, Y: 0},
		LastTargetPos: domain.Vec2{X: 0, Y: 100},
		ExpiresAt:     now.Add(spec.TTL),
	}
	combat.TickMissile(m, domain.Vec2{X: 0, Y: 100}, true, spec, 1.0, now)
	// After one tick Direction must have rotated counter-clockwise by ~22.5°,
	// i.e. (cos 22.5°, sin 22.5°).
	require.InDelta(t, math.Cos(math.Pi/8), m.Direction.X, 1e-6)
	require.InDelta(t, math.Sin(math.Pi/8), m.Direction.Y, 1e-6)
}
