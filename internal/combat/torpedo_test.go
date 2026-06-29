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

// splashShip is a minimal damageable ship at pos with a given id, owner, and
// shieldless hull for the area-damage tests (HP-only damage is easy to read).
func splashShip(id, player int64, pos domain.Vec2, hp int) *domain.Ship {
	return &domain.Ship{
		ID:       domain.ShipID(id),
		PlayerID: domain.PlayerID(player),
		Pos:      pos,
		HP:       hp,
		MaxHP:    hp,
	}
}

// TestUnit_ApplyDamageInRadius_HitsInRadiusSkipsOutside: a blast damages every
// ship inside SplashRadius and leaves a ship outside it untouched, reporting
// exactly the in-radius refs (ЧТЗ AC-6 N≥2 targets; AC #4 radius bound).
func TestUnit_ApplyDamageInRadius_HitsInRadiusSkipsOutside(t *testing.T) {
	t.Parallel()
	center := domain.Vec2{X: 0, Y: 0}
	near1 := splashShip(1, 100, domain.Vec2{X: 10, Y: 0}, 1000)
	near2 := splashShip(2, 200, domain.Vec2{X: 0, Y: 30}, 1000)
	far := splashShip(3, 300, domain.Vec2{X: 500, Y: 0}, 1000)
	ships := map[domain.ShipID]*domain.Ship{1: near1, 2: near2, 3: far}

	hits := combat.ApplyDamageInRadius(ships, nil, center, 40, 150, 999)

	require.ElementsMatch(t, []domain.EntityRef{
		{Kind: domain.EntityKindShip, ID: 1},
		{Kind: domain.EntityKindShip, ID: 2},
	}, hits, "only the two ships inside SplashRadius are reported hit")
	require.Equal(t, 850, near1.HP, "in-radius ship took splash damage")
	require.Equal(t, 850, near2.HP, "in-radius ship took splash damage")
	require.Equal(t, 1000, far.HP, "a ship outside SplashRadius is untouched (proves the radius bound)")
}

// TestUnit_ApplyDamageInRadius_FriendlyFireHitsOwnShips: the splash is
// indiscriminate — the firing player's own launching ship and another ship of
// the same player, both in the blast, take damage and are attributed to the
// firing player (ЧТЗ AC-6 friendly-fire, R-02). No owner/ally filtering exists.
func TestUnit_ApplyDamageInRadius_FriendlyFireHitsOwnShips(t *testing.T) {
	t.Parallel()
	const attacker = domain.PlayerID(100)
	center := domain.Vec2{X: 0, Y: 0}
	owner := splashShip(1, int64(attacker), domain.Vec2{X: 5, Y: 0}, 500) // the launching ship
	ally := splashShip(2, int64(attacker), domain.Vec2{X: 0, Y: 20}, 500) // another own ship
	ships := map[domain.ShipID]*domain.Ship{1: owner, 2: ally}

	hits := combat.ApplyDamageInRadius(ships, nil, center, 40, 100, attacker)

	require.Len(t, hits, 2, "splash hits both friendly ships — no owner exclusion")
	require.Equal(t, 400, owner.HP, "the firing player's own launching ship takes damage")
	require.Equal(t, 400, ally.HP, "a friendly ship of the firing player takes damage")
	require.Equal(t, attacker, owner.LastAttacker, "a self-hit is attributed to the firing player")
	require.Equal(t, attacker, ally.LastAttacker)
}

// TestUnit_ApplyDamageInRadius_DamagesStatic: a destructible static inside the
// blast takes damage through domain.Damageable, one outside is untouched, and a
// dead ship in range is skipped (the kill sweep already owns it) — ЧТЗ AC #3.
func TestUnit_ApplyDamageInRadius_DamagesStatic(t *testing.T) {
	t.Parallel()
	center := domain.Vec2{X: 0, Y: 0}
	insideRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	inside := &domain.DestructibleStatic{Ref: insideRef, Pos: domain.Vec2{X: 10, Y: 0}, HP: 10000}
	outsideRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: 6}
	outside := &domain.DestructibleStatic{Ref: outsideRef, Pos: domain.Vec2{X: 500, Y: 0}, HP: 10000}
	statics := map[domain.EntityRef]*domain.DestructibleStatic{insideRef: inside, outsideRef: outside}

	dead := splashShip(1, 100, domain.Vec2{X: 0, Y: 0}, 0) // HP<=0, already dead
	ships := map[domain.ShipID]*domain.Ship{1: dead}

	hits := combat.ApplyDamageInRadius(ships, statics, center, 40, 600, 999)

	require.Equal(t, []domain.EntityRef{insideRef}, hits, "only the in-radius static is hit; the dead ship is skipped")
	require.Equal(t, 9400, inside.HP, "static in radius took splash damage via domain.Damageable")
	require.Equal(t, 10000, outside.HP, "static outside SplashRadius is untouched")
}
