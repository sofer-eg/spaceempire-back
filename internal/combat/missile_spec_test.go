package combat_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/config"
)

// ctMissilesRow is one row of the legacy `ct_missiles` table, spelled out from the
// dump (starwind/sql/db.sql, `INSERT INTO ct_missiles`) rather than derived from
// the Go catalog — a copy-paste of the wrong number must fail here instead of
// quietly agreeing with itself. Columns are the table's own:
// class, name, speed, acceleration, maneureability, power, ttl, cargo_id.
type ctMissilesRow struct {
	class    int
	name     string
	speed    float64
	accel    float64
	maneuv   float64
	power    int
	ttlTicks int
}

// ctMissiles is the acceptance-parity source for TASK-175: all five missile
// classes as the original ships them.
var ctMissiles = []ctMissilesRow{
	{1, "Москит", 90, 108.0, 320, 1000, 5},
	{2, "Оса", 78, 70.2, 230, 2500, 6},
	{3, "Стрекоза", 60, 48.0, 200, 5000, 6},
	{4, "Шелкопряд", 28, 21.0, 140, 12000, 9},
	{5, "Шершень", 22, 16.0, 80, 25000, 10},
}

// TestUnit_MissileSpecs_MatchCtMissiles pins every per-class magnitude against
// ct_missiles, one axis at a time, so each calibration decision is visible:
//
//   - Damage IS ct_missiles.power, literally (TASK-175 AC-8). Class 1 rose from 30
//     to 1000 here: hulls/shields/lasers were ported from StarWind literally
//     (Разведчик, class 77: hull 4040, shield 6900, ~146 laser damage per shot), so
//     30 was a fifth of one laser shot and the weapon was decorative.
//   - Speed / Accel are ct_missiles.speed / .acceleration, literally. See
//     missileSpecsByClass for why the per-tick→per-second unit shift is deliberate.
//   - TurnRate is the SP's own formula: maneureability × mob_to_grad_eq (1.16) is
//     degrees per TICK, converted to rad/s over the project's 3-second tick.
//   - TTL is ct_missiles.ttl, which the SP counts in TICKS (`mis_ttl >= mis_std_ttl`
//     against a per-tick `ttl = ttl + 1`), converted at the same 3 s.
func TestUnit_MissileSpecs_MatchCtMissiles(t *testing.T) {
	t.Parallel()

	for _, row := range ctMissiles {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			spec := combat.DefaultMissileSpec(row.class)

			assert.Equal(t, row.power, spec.Damage,
				"class %d Damage must be ct_missiles.power literally", row.class)
			assert.Equal(t, row.speed, spec.Speed,
				"class %d Speed must be ct_missiles.speed literally", row.class)
			assert.Equal(t, row.accel, spec.Accel,
				"class %d Accel must be ct_missiles.acceleration literally", row.class)
			assert.InDelta(t, row.maneuv*1.16*math.Pi/180/3, spec.TurnRate, 1e-9,
				"class %d TurnRate must be maneureability×1.16 deg/tick over a 3 s tick", row.class)
			assert.Equal(t, time.Duration(row.ttlTicks)*3*time.Second, spec.TTL,
				"class %d TTL must be ct_missiles.ttl ticks at 3 s/tick", row.class)
			assert.Positive(t, spec.HitRadius,
				"class %d needs a hit radius: ct_missiles has no such column, so the "+
					"project's uniform value stands in for SP std_hit_distance", row.class)
		})
	}
}

// TestUnit_MissileSpecs_HeavierClassIsSlowerAndHarder pins the shape of the
// ladder rather than the numbers: a heavier missile hits harder, flies slower,
// turns worse and loiters at least as long. This is the property the orchestrator
// asked to be preserved — classes 4 and 5 (28 / 22) are deliberately unable to run
// down a fast hull (the top ship classes reach MaxSpeed 130.6, 141 with an engine
// upgrade), because heavy ordnance in the original is an anti-capital weapon.
func TestUnit_MissileSpecs_HeavierClassIsSlowerAndHarder(t *testing.T) {
	t.Parallel()

	for i := 1; i < len(ctMissiles); i++ {
		lighter := combat.DefaultMissileSpec(ctMissiles[i-1].class)
		heavier := combat.DefaultMissileSpec(ctMissiles[i].class)
		assert.Greater(t, heavier.Damage, lighter.Damage,
			"class %d must hit harder than class %d", ctMissiles[i].class, ctMissiles[i-1].class)
		assert.Less(t, heavier.Speed, lighter.Speed,
			"class %d must be slower than class %d", ctMissiles[i].class, ctMissiles[i-1].class)
		assert.Less(t, heavier.TurnRate, lighter.TurnRate,
			"class %d must turn worse than class %d", ctMissiles[i].class, ctMissiles[i-1].class)
		assert.GreaterOrEqual(t, heavier.TTL, lighter.TTL,
			"class %d must loiter at least as long as class %d", ctMissiles[i].class, ctMissiles[i-1].class)
	}
}

// TestUnit_MissileSpecs_ReachExceedsRadar is the invariant behind taking
// ct_missiles.speed literally as units per SECOND while a ship's MaxSpeed stayed
// units per TICK: every class must be able to cross the distance a player can see.
// The widest per-class radar is 500 (balance.radarByCategory, scouts), so a launch
// at maximum scanner range has to be able to arrive — otherwise the expensive
// classes would be exactly what this task exists to remove: ammunition on sale
// that cannot be used. Reading speed as per-tick instead would give class 5
// 22/3 × 30 s ≈ 220 units of reach, less than half a radar ring.
func TestUnit_MissileSpecs_ReachExceedsRadar(t *testing.T) {
	t.Parallel()

	const widestRadar = 500.0
	for _, row := range ctMissiles {
		spec := combat.DefaultMissileSpec(row.class)
		reach := spec.Speed * spec.TTL.Seconds()
		assert.Greater(t, reach, widestRadar,
			"class %d reach %.0f must exceed the widest radar (%.0f)", row.class, reach, widestRadar)
	}
}

// TestUnit_MissileSpecs_SnapBoundaryHoldsAtTheDefaultTick pins the assumption the
// TurnRate calibration rests on, which nothing in the code can enforce: the catalog
// converts ct_missiles.maneureability (degrees per TICK) at a hard-coded nominal
// 3 s tick, while the tick itself is configuration
// (config.SectorConfig.TickInterval, `default:"3s"`, overridable via the CONFIG_PATH
// YAML and SE_SECTOR_TICK_INTERVAL).
//
// What breaks silently if they diverge is the SP's own split between an instant and
// a gradual turn: TO_Missiles snaps the heading when grad_speed > 180°/tick, and
// TickMissile snaps at TurnRate*dt >= π. At 3 s those are the same line — classes
// 1-3 (371/267/232 °/tick) snap, classes 4-5 (162/93) turn gradually. At 1 s every
// class turns gradually and three times slower per tick than the original, and the
// parity claim in missileSpecsByClass and missiles.md §3.1 becomes false with no
// test failing.
//
// Deriving TurnRate from cfg at wiring time was the alternative and was rejected:
// the specs live in combat as data, and threading config into them to keep one
// comment honest is the wrong trade. So the coupling is pinned instead — hence a
// combat test that reads pkg/config, the one import here that is not about combat.
// If the default tick is ever changed on purpose, this test is the checklist entry
// saying the missile catalog has to be recalibrated with it.
//
// HOW the Go side is measured is the load-bearing part: `snapsInGo` comes from real
// TickMissile calls (snapTurnProbeBearingsDeg via oneTickAlignsWithTarget), never
// from re-evaluating `TurnRate*dt >= π` in the test. Restating the condition here
// would pin the formula against itself: widening the threshold to 2π leaves classes
// 2-3 rotating a literal 267°/232° past a 45° target into the opposite hemisphere,
// and a test that spells the condition out passes that mutation unmoved.
func TestUnit_MissileSpecs_SnapBoundaryHoldsAtTheDefaultTick(t *testing.T) {
	// Not parallel: t.Setenv (needed to neutralise a developer's own override).
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("SE_SECTOR_TICK_INTERVAL", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, cfg.Sector.TickInterval,
		"the missile catalog converts ct_missiles' per-tick maneureability and ttl at a "+
			"nominal 3 s tick (combat/missile.go nominalTickSeconds); the default tick moved, "+
			"so recalibrate the catalog (and missiles.md §3.1) or the snap boundary no longer "+
			"matches SP TO_Missiles")

	dt := cfg.Sector.TickInterval.Seconds()
	for _, row := range ctMissiles {
		spec := combat.DefaultMissileSpec(row.class)
		snapsInSP := row.maneuv*1.16 > 180 // SP: grad_speed > 180°/tick

		// A snapping missile takes ANY heading in one tick; a bounded one only gets
		// part of the way round on the bearings wider than its own step, and must
		// never swing PAST the ones narrower than it. So ask the integrator on a
		// spread of bearings and let "aligned on every one of them" be the verdict.
		snapsInGo, fellShortAtDeg := true, 0.0
		for _, bearingDeg := range snapTurnProbeBearingsDeg {
			if !oneTickAlignsWithTarget(t, row.class, bearingDeg*math.Pi/180, dt) {
				snapsInGo, fellShortAtDeg = false, bearingDeg
				break
			}
		}
		assert.Equal(t, snapsInSP, snapsInGo,
			"class %d: SP snaps=%v at %.1f°/tick, one real TickMissile of %.1fs aligns the "+
				"heading from every probed bearing=%v (first bearing it fell short of: %.0f°) "+
				"at TurnRate %.4f rad/s",
			row.class, snapsInSP, row.maneuv*1.16, dt, snapsInGo, fellShortAtDeg, spec.TurnRate)
	}
}

// snapTurnProbeBearingsDeg are the target bearings oneTickAlignsWithTarget walks,
// shallow first so a missile that swings past a near target is what the failure
// message names. The narrow end catches an over-wide snap threshold (a missile that
// applies >180° as a literal rotation leaves a 45° target behind it); the wide end
// catches a missing one (a bounded step cannot cover a near-reversal in one tick).
// 179° rather than 180°: at an exact reversal the perpendicular component is zero
// and TickMissile's own left/right sign test degenerates.
var snapTurnProbeBearingsDeg = []float64{45, 90, 135, 179}

// oneTickAlignsWithTarget runs ONE real TickMissile of dt seconds on a class-`class`
// missile that starts pointing at +X with its target `bearing` radians off that
// heading, and reports whether the integrator brought Direction all the way onto the
// target in that single tick. The verdict is measured, not recomputed — that is the
// whole point of the helper (see the test's doc comment).
func oneTickAlignsWithTarget(t *testing.T, class int, bearing, dt float64) bool {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	a := attackerShip()
	a.Vel = domain.Vec2{} // only Direction is read; inherited drift would just move Pos
	a.Direction = domain.Vec2{X: 1, Y: 0}

	// Far enough that the tick is a pure turn: well outside HitRadius, and past the
	// range below which the integrator holds its heading instead of steering.
	const targetRange = 1000
	targetPos := domain.Vec2{
		X: targetRange * math.Cos(bearing),
		Y: targetRange * math.Sin(bearing),
	}
	m := combat.LaunchMissile(1, combat.DefaultMissileSpec(class), a,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 99}, targetPos, now)

	combat.TickMissile(m, targetPos, true, dt, now)

	// Aligned = the unit heading and the unit bearing point the same way. The
	// gradual outcomes sit whole degrees away, so the tolerance is not a judgement
	// call: it only absorbs float noise on an exact snap.
	dot := m.Direction.X*math.Cos(bearing) + m.Direction.Y*math.Sin(bearing)
	return dot > 1-1e-6
}

// TestUnit_DefaultMissileSpec_UnknownClassFallsBack: the HTTP handler validates
// the class upstream, but the accessor must never hand back a degenerate
// (zero-TTL, zero-Speed) spec. An unknown class — including the zero value a
// legacy caller or an in-package fixture leaves — yields the class-1 profile.
// Mirrors DefaultTorpedoSpec.
func TestUnit_DefaultMissileSpec_UnknownClassFallsBack(t *testing.T) {
	t.Parallel()
	require.Equal(t, combat.DefaultMissileSpec(1), combat.DefaultMissileSpec(0))
	require.Equal(t, combat.DefaultMissileSpec(1), combat.DefaultMissileSpec(99))
}
