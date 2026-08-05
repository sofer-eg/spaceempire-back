package combat

import (
	"math"
	"time"

	"spaceempire/back/internal/domain"
)

// MissileSpec captures the per-class missile magnitudes ported from SP
// `TO_Missiles` and `ct_missiles`. It mirrors TorpedoSpec: the fields are exactly
// those copied into a domain.Missile at launch (Damage/Speed/Accel/TurnRate/
// HitRadius) plus TTL → ExpiresAt, so TickMissile reads its magnitudes off the
// missile and never needs the spec again. The spec is therefore a launch-time
// catalog lookup only (see missileSpecsByClass).
type MissileSpec struct {
	// Damage applied to the target on Hit.
	Damage int
	// Speed is the upper bound on |Vel|, in world units per second.
	// The integrator scales by `dt` so the same spec works at any
	// tick rate.
	Speed float64
	// Accel is the per-second engine acceleration applied along
	// Direction.
	Accel float64
	// TurnRate is the maximum heading change per second (radians).
	// When TurnRate*dt >= π the rotation step degenerates into an
	// instant snap to target — mirrors SP `grad_speed > 180` branch.
	TurnRate float64
	// HitRadius is the distance threshold for `MissileHit`. Includes
	// the target's own collision radius — keep it generous (10–15)
	// since intentional hit-roll randomisation from the SP is not
	// ported (see spec §11).
	HitRadius float64
	// TTL is the wall-clock lifetime from launch to forced expire.
	TTL time.Duration
}

// Missile strafe / friction coefficients. Unlike per-class Damage/Speed these are
// uniform across all five classes (the SP computes them from the class's own
// acceleration and speed with fixed factors: `mis_strafe = 0.8 * mis_acceleration`,
// `mis_rub = 0.1 * mis_speed`), so MissileSpec carries no field for them and they
// live as package constants — same shape torpedoStrafeK / torpedoFrictionK use.
const (
	missileStrafeK   = 0.8
	missileFrictionK = 0.1
)

// nominalTickSeconds is the project's DEFAULT sector tick (config.SectorConfig
// TickInterval, `default:"3s"`). It appears here because two ct_missiles columns
// are counted in TICKS by the SP and have to be expressed in the seconds
// MissileSpec uses: `ttl` (compared against a per-tick `ttl = ttl + 1`) and
// `maneureability` (a per-tick angular rate).
//
// The tick is configurable, and this constant does NOT follow it: the catalog is
// deliberately free of config (a spec is data, not wiring), so what is calibrated
// here is calibrated against the nominal tick only. TTL is unaffected in effect —
// the integrator scales everything by dt, so a 30 s missile lives 30 s at any tick
// rate — but the SNAP BOUNDARY is not: TickMissile snaps the heading at
// TurnRate*dt >= π, which reproduces the SP's `grad_speed > 180°/tick` split
// (classes 1-3 snap, 4-5 turn gradually) only while dt is 3 s. Shorten the tick and
// every class turns gradually, three times slower per tick than the original.
// TestUnit_MissileSpecs_SnapBoundaryHoldsAtTheDefaultTick pins that assumption so
// a change of the default tick fails a test instead of quietly making this comment
// and missiles.md §3.1 false; recalibrating the catalog is then part of that change.
const nominalTickSeconds = 3.0

// maneuvToDegPerTick is SP TO_Missiles' `mob_to_grad_eq`: it converts
// ct_missiles.maneureability into degrees of heading change per tick
// (`mis_grad_speed = round(mis_manevr * 1.16)`).
const maneuvToDegPerTick = 1.16

// missileTurnRate converts a ct_missiles.maneureability value into the rad/s
// TurnRate the integrator wants. Derived rather than hand-computed so the five
// classes cannot drift from the SP's formula, and so the snap boundary lands where
// the original put it: the SP snaps the heading when grad_speed > 180°/tick, and
// TickMissile snaps when TurnRate*dt >= π — which at the nominal 3 s tick is the
// same threshold. Classes 1-3 (371/267/232 °/tick) snap in both; classes 4-5
// (162/93 °/tick) turn gradually in both. That equivalence is tick-dependent — see
// nominalTickSeconds.
func missileTurnRate(maneuverability float64) float64 {
	return maneuverability * maneuvToDegPerTick * math.Pi / 180 / nominalTickSeconds
}

// missileHitRadius is the detonation-trigger distance, uniform across classes:
// ct_missiles has no hit-radius column (the SP starts from a flat
// `std_hit_distance = 10` and applies pilot/size modifiers this port does not
// carry, see missiles.md §11), so there is nothing per-class to port.
const missileHitRadius = 12

// missileSpecsByClass is the per-class balance profile, one entry per ct_missiles
// row: 1 = gt10 «Ракета Москит», 2 = gt11 «Ракета Оса», 3 = gt12 «Ракета
// Стрекоза», 4 = gt13 «Ракета Шелкопряд», 5 = gt14 «Ракета Шершень» (the ids are
// ct_missiles.cargo_id — see domain.MissileGoodsTypes).
//
// Calibration (TASK-175), axis by axis, because the axes were NOT decided the same
// way and a blanket "ported literally" would be false:
//
//   - Damage = ct_missiles.power, literally: 1000 / 2500 / 5000 / 12000 / 25000.
//     Class 1 was 30 until this task, a leftover from phase 4.3 when nothing else
//     was calibrated yet. Hulls, shields and lasers came over from StarWind
//     literally — Разведчик (class 77) has hull 4040, shield 6900 and ~146 laser
//     damage per shot; Кентавр (78) has 66000 + 161000 — so 30 was a fifth of one
//     laser shot: the whole weapon was decorative. The live market prices confirm
//     the power ladder is the intended one: 392 / 1151 / 2309 / 5778 / 11651 cr
//     ≈ 1 : 2.9 : 5.9 : 14.7 : 29.7 against power's 1 : 2.5 : 5 : 12 : 25.
//
//   - Speed / Accel = ct_missiles.speed / .acceleration, literally, and this is a
//     deliberate unit shift, not an oversight: the SP adds both once per tick, so
//     they are per-tick there, while MissileSpec is per-second — reading them
//     literally therefore makes a missile 3× faster relative to a ship, whose
//     MaxSpeed stayed per-tick (moveShip integrates `Pos += Vel`). The reason to
//     accept that: a missile's reach is Speed × TTL, and the widest radar is 500
//     units, so the per-tick reading would leave class 5 with ~220 units of reach —
//     unable to arrive at anything its owner can see, which is exactly the
//     "sold but unusable" defect this task closes. The ladder survives either way
//     (class 1 is 4× class 5), and so does the original's intent that heavy
//     classes cannot run down a fast hull: 28 and 22 u/s are 84 and 66 units per
//     3 s tick, below the 130.6 (141 with up_engine) of the top ship classes.
//
//   - TurnRate: derived through missileTurnRate, which is the SP's own formula.
//
//   - TTL = ct_missiles.ttl, but converted, because the SP's unit for it is
//     unambiguously ticks: 5 / 6 / 6 / 9 / 10 ticks × 3 s = 15 / 18 / 18 / 27 / 30 s.
//     Class 1 keeps the 15 s it already had — that value was this same conversion.
//     Taking the number literally as seconds instead would cut class 1's reach by
//     two thirds and make class 5 (22 u/s for 10 s = 220 units) unusable again.
var missileSpecsByClass = map[int]MissileSpec{
	1: {Damage: 1000, Speed: 90, Accel: 108.0, TurnRate: missileTurnRate(320), HitRadius: missileHitRadius, TTL: 15 * time.Second},
	2: {Damage: 2500, Speed: 78, Accel: 70.2, TurnRate: missileTurnRate(230), HitRadius: missileHitRadius, TTL: 18 * time.Second},
	3: {Damage: 5000, Speed: 60, Accel: 48.0, TurnRate: missileTurnRate(200), HitRadius: missileHitRadius, TTL: 18 * time.Second},
	4: {Damage: 12000, Speed: 28, Accel: 21.0, TurnRate: missileTurnRate(140), HitRadius: missileHitRadius, TTL: 27 * time.Second},
	5: {Damage: 25000, Speed: 22, Accel: 16.0, TurnRate: missileTurnRate(80), HitRadius: missileHitRadius, TTL: 30 * time.Second},
}

// DefaultMissileSpec returns the balance profile for an ammunition class 1-5. The
// HTTP handler validates the class before a launch reaches the worker; an unknown
// class falls back to the class-1 profile so this accessor never yields a
// degenerate (zero-TTL, zero-Speed) spec — the same contract DefaultTorpedoSpec
// has, and what keeps a fixture that leaves Class at its zero value flying a
// Москит. Consumed by LaunchMissile at spawn.
func DefaultMissileSpec(class int) MissileSpec {
	if spec, ok := missileSpecsByClass[class]; ok {
		return spec
	}
	return missileSpecsByClass[1]
}

// MissileOutcome reports the per-tick verdict TickMissile returns to its
// caller (sector worker).
type MissileOutcome uint8

const (
	// MissileKeep means the missile is still in flight; the worker must
	// keep it in the live set and broadcast its updated state.
	MissileKeep MissileOutcome = iota
	// MissileHit means the missile arrived within HitRadius of an alive
	// target this tick. The worker applies damage and removes the missile.
	MissileHit
	// MissileExpired means the missile's TTL ran out. The worker removes
	// the missile without applying damage.
	MissileExpired
)

// LaunchMissile builds a fresh Missile fired from attacker against
// target. Initial Vel inherits attacker.Vel — a strafing pilot does not
// shoot backwards-drifting missiles. Initial Direction equals
// attacker.Direction; the first TickMissile call will rotate it toward
// the target if needed.
//
// LastTargetPos is set from targetPos so the missile has a fallback
// course once the target dies or leaves the sector (see spec §1).
//
// Caller (sector worker) is responsible for the id allocation and for
// inserting the returned missile into its live set; LaunchMissile itself
// is a pure builder.
func LaunchMissile(
	id domain.MissileID,
	spec MissileSpec,
	attacker *domain.Ship,
	target domain.EntityRef,
	targetPos domain.Vec2,
	now time.Time,
) *domain.Missile {
	dir := attacker.Direction
	if dir.IsZero() {
		dir = domain.Vec2{X: 1, Y: 0}
	}
	return &domain.Missile{
		ID:            id,
		SectorID:      attacker.SectorID,
		OwnerShipID:   attacker.ID,
		PlayerID:      attacker.PlayerID,
		Pos:           attacker.Pos,
		Vel:           attacker.Vel,
		Direction:     dir,
		Target:        target,
		LastTargetPos: targetPos,
		Damage:        spec.Damage,
		Speed:         spec.Speed,
		Accel:         spec.Accel,
		TurnRate:      spec.TurnRate,
		HitRadius:     spec.HitRadius,
		ExpiresAt:     now.Add(spec.TTL),
	}
}

// TickMissile integrates one missile by dt seconds, reading its homing magnitudes
// (Speed/Accel/TurnRate/HitRadius) off the missile itself — copied there from the
// per-class spec at launch — so five classes with five profiles fly correctly in
// one sector. It took a spec argument until TASK-175, and the tick passed a single
// package-level one: every missile in the world flew the class-1 profile no matter
// what was launched. Strafe and friction are uniform across classes
// (missileStrafeK / missileFrictionK). Same shape as TickTorpedo.
//
// targetAlive=true when the sector worker found the target in its live set; in
// that case targetPos must be the target's current Pos and the missile updates
// its LastTargetPos before steering. When targetAlive=false the missile
// steers blindly toward m.LastTargetPos and Hit checks are suppressed
// (lost target → can only expire, see spec §1).
//
// The integrator is a port of SP `TO_Missiles` with the per-tick maths:
//  1. Compute deltaToTarget and its unit (or fall back to Direction if
//     the missile is essentially on top of the point).
//  2. Rotate Direction by up to TurnRate*dt toward the target unit;
//     if TurnRate*dt >= π, snap (SP `grad_speed > 180` branch).
//  3. Acceleration along the new Direction. When the missile is still
//     turning and the new direction is on the wrong side of the target
//     (dot < 0), the SP reduces acceleration heavily so the missile
//     does not power through the corner.
//  4. Strafe compensation cancels the velocity component perpendicular
//     to the target line, up to StrafeK*Accel*dt magnitude.
//  5. Friction subtracts FrictionK*|Vel|*dt along the current Vel.
//  6. Vel += acc + friction + strafe, clamp to Speed.
//  7. Pos += Vel*dt. If distance to targetPos ≤ HitRadius and
//     targetAlive: MissileHit. Else if ExpiresAt elapsed: MissileExpired.
//
// All mutations of m happen in this function; the worker can rely on
// "no other goroutine touched m" because of the one-writer-per-sector
// invariant.
func TickMissile(
	m *domain.Missile,
	targetPos domain.Vec2,
	targetAlive bool,
	dt float64,
	now time.Time,
) MissileOutcome {
	if m == nil {
		return MissileExpired
	}
	if !now.Before(m.ExpiresAt) {
		return MissileExpired
	}
	if targetAlive {
		m.LastTargetPos = targetPos
	} else {
		targetPos = m.LastTargetPos
	}

	delta := targetPos.Sub(m.Pos)
	rangeEq := delta.Length()

	noTurn := false
	var targetDir domain.Vec2
	if rangeEq > 1 {
		targetDir = delta.Scale(1.0 / rangeEq)
	} else {
		targetDir = m.Direction
		noTurn = true
	}

	speedEq := m.Vel.Length()
	var speedDir domain.Vec2
	if speedEq > 1 {
		speedDir = m.Vel.Scale(1.0 / speedEq)
	} else {
		speedDir = m.Direction
	}

	// Step 2: rotate Direction toward targetDir.
	newDir := m.Direction
	turning := false
	radStep := m.TurnRate * dt
	if !noTurn {
		if radStep >= math.Pi {
			newDir = targetDir
		} else {
			// Project targetDir into the local frame of Direction:
			//   ck_x = targetDir · Direction  (forward component)
			//   ck_y = targetDir · perp(Direction) (left/right sign)
			// perp(d) = (-d.y, d.x). Then |ck_y| < ~0 means already aligned.
			ckX := targetDir.X*m.Direction.X + targetDir.Y*m.Direction.Y
			ckY := targetDir.X*(-m.Direction.Y) + targetDir.Y*m.Direction.X
			if math.Abs(ckY) < 0.01 && ckX > 0 {
				newDir = targetDir
			} else {
				// Rotate by ±radStep depending on the sign of ckY (which
				// tells us whether targetDir lies to the left or right of
				// the current Direction).
				step := radStep
				if ckY < 0 {
					step = -radStep
				}
				cs := math.Cos(step)
				sn := math.Sin(step)
				// 2×2 rotation of Direction by `step`.
				newDir = domain.Vec2{
					X: cs*m.Direction.X - sn*m.Direction.Y,
					Y: sn*m.Direction.X + cs*m.Direction.Y,
				}
				// Did this single step overshoot the target? If so,
				// snap and stop turning.
				newCkY := targetDir.X*(-newDir.Y) + targetDir.Y*newDir.X
				if (ckY > 0) != (newCkY > 0) {
					newDir = targetDir
				} else {
					turning = true
				}
			}
		}
	}

	// Step 3: acceleration along newDir, with the SP brake on bad turns.
	accel := m.Accel * dt
	if turning {
		dot := newDir.X*targetDir.X + newDir.Y*targetDir.Y
		if dot < 0 {
			accel = (missileFrictionK + m.Accel*0.1) * dt
		}
	}
	acc := newDir.Scale(accel)

	// Step 4: strafe compensation against perpendicular drift.
	strafeMax := missileStrafeK * m.Accel * dt
	addStrafe := domain.Vec2{}
	if strafeMax > 0 {
		perp := domain.Vec2{X: -targetDir.Y, Y: targetDir.X}
		side := (m.Vel.X+acc.X)*perp.X + (m.Vel.Y+acc.Y)*perp.Y
		if math.Abs(side) > 0.001 {
			mag := math.Abs(side)
			if mag > strafeMax {
				mag = strafeMax
			}
			sign := 1.0
			if side > 0 {
				sign = -1.0
			}
			addStrafe = perp.Scale(sign * mag)
		}
	}

	// Step 5: friction (proportional drag).
	rubMag := missileFrictionK * speedEq * dt
	rub := speedDir.Scale(-rubMag)

	// Step 6: integrate velocity, clamp magnitude.
	newVel := m.Vel.Add(acc).Add(rub).Add(addStrafe)
	if mag := newVel.Length(); mag > m.Speed {
		newVel = newVel.Scale(m.Speed / mag)
	}

	// Step 7: position + hit check.
	oldPos := m.Pos
	newPos := m.Pos.Add(newVel.Scale(dt))

	m.Pos = newPos
	m.Vel = newVel
	m.Direction = newDir

	if targetAlive {
		// Hit when the line segment oldPos→newPos passes within HitRadius
		// of targetPos. A pure endpoint check misses fast-moving missiles
		// that fly straight through a target between integration steps
		// (Speed > distance-to-target within one dt).
		if pointSegmentDistance(targetPos, oldPos, newPos) <= m.HitRadius {
			return MissileHit
		}
	}
	return MissileKeep
}

// pointSegmentDistance returns the shortest distance from p to the line
// segment [a, b]. When a==b it degenerates into |p-a|.
func pointSegmentDistance(p, a, b domain.Vec2) float64 {
	ab := b.Sub(a)
	abLen2 := ab.X*ab.X + ab.Y*ab.Y
	if abLen2 == 0 {
		return p.Sub(a).Length()
	}
	t := ((p.X-a.X)*ab.X + (p.Y-a.Y)*ab.Y) / abLen2
	switch {
	case t < 0:
		t = 0
	case t > 1:
		t = 1
	}
	closest := domain.Vec2{X: a.X + ab.X*t, Y: a.Y + ab.Y*t}
	return p.Sub(closest).Length()
}
