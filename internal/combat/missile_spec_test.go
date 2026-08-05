package combat_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
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
