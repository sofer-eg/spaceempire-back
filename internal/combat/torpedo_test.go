package combat_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// torpedoAttacker is a minimal launcher ship at the origin facing +X.
func torpedoAttacker() *domain.Ship {
	return &domain.Ship{
		ID:        1,
		PlayerID:  100,
		SectorID:  1,
		Pos:       domain.Vec2{X: 0, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
	}
}

// TestUnit_TickTorpedo_HomesAndDetonates: a class-3 torpedo closes on a moving
// target over a series of ticks and detonates (TorpedoHit) once within HitRadius
// (ЧТЗ AC-4). The range strictly shrinks while it homes, proving the homing
// integrator converges rather than the test just running long enough.
func TestUnit_TickTorpedo_HomesAndDetonates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	spec := combat.DefaultTorpedoSpec(3)
	target := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	targetPos := domain.Vec2{X: 300, Y: 0}
	torp := combat.LaunchTorpedo(7, 3, spec, torpedoAttacker(), target, targetPos, now)

	const dt = 1.0
	prevRange := targetPos.Sub(torp.Pos).Length()
	hit := false
	for i := 0; i < 60 && !hit; i++ {
		// Target drifts laterally each tick so the torpedo must keep steering.
		targetPos = targetPos.Add(domain.Vec2{X: 0, Y: 4})
		switch combat.TickTorpedo(torp, targetPos, true, dt, now) {
		case combat.TorpedoHit:
			hit = true
		case combat.TorpedoKeep:
			rng := targetPos.Sub(torp.Pos).Length()
			assert.LessOrEqual(t, rng, prevRange+1e-9, "range must not grow while homing")
			prevRange = rng
		case combat.TorpedoExpired:
			t.Fatalf("torpedo expired before reaching the target (tick %d)", i)
		}
	}
	require.True(t, hit, "torpedo must converge on a moving target and detonate")
}

// TestUnit_TickTorpedo_ExpiresOnTTL: with the clock advanced past ExpiresAt the
// torpedo expires regardless of its position, and an expiring tick never reports
// a hit (ЧТЗ AC-8: no damage on TTL).
func TestUnit_TickTorpedo_ExpiresOnTTL(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	spec := combat.DefaultTorpedoSpec(2)
	target := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	// Target sits right on the torpedo so a position-only check would "hit";
	// the TTL must win.
	torp := combat.LaunchTorpedo(7, 2, spec, torpedoAttacker(), target, domain.Vec2{}, now)

	after := torp.ExpiresAt.Add(time.Second)
	assert.Equal(t, combat.TorpedoExpired, combat.TickTorpedo(torp, domain.Vec2{}, true, 1.0, after))
}

// TestUnit_TickTorpedo_FallbackToLastTargetPos: a lost target (targetAlive=false)
// steers the torpedo toward its remembered LastTargetPos and suppresses hit
// detection — it can only run out the TTL (ЧТЗ FR-005).
func TestUnit_TickTorpedo_FallbackToLastTargetPos(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	spec := combat.DefaultTorpedoSpec(3)
	target := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	last := domain.Vec2{X: 400, Y: 0}
	torp := combat.LaunchTorpedo(7, 3, spec, torpedoAttacker(), target, last, now)

	startRange := last.Sub(torp.Pos).Length()
	for i := 0; i < 5; i++ {
		// Even though we pass a coincident targetPos, targetAlive=false means it
		// is ignored in favour of LastTargetPos and no hit can be reported.
		require.Equal(t, combat.TorpedoKeep,
			combat.TickTorpedo(torp, torp.Pos, false, 1.0, now))
	}
	assert.Less(t, last.Sub(torp.Pos).Length(), startRange,
		"torpedo must close on its remembered LastTargetPos when the target is lost")
}
