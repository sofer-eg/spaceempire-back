package combat

import (
	"math"
	"time"

	"spaceempire/back/internal/domain"
)

// MissileSpec captures the per-class missile knobs ported from SP
// `TO_Missiles` and `ct_missiles`. Phase 4.3 has exactly one class so
// every missile is launched with DefaultMissileSpec; future phases may
// load this from a balance catalog.
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
	// StrafeK is the strafe-compensation cap as a fraction of Accel.
	// SP uses `mis_strafe = 0.8 * mis_acceleration`.
	StrafeK float64
	// FrictionK is the per-second friction coefficient: drag
	// magnitude = FrictionK * |Vel| * dt, pointing against the
	// velocity. SP uses `mis_rub = -0.1 * speed_eq` per tick.
	FrictionK float64
}

// DefaultMissileSpec returns the spec used for every starter missile in
// phase 4.3. Values calibrated against a 3-second tick interval and the
// starter ship's MaxSpeed=20 — a missile is roughly 4× faster than the
// ship so launches feel like guided ordnance, not a parallel-flight chase.
func DefaultMissileSpec() MissileSpec {
	return MissileSpec{
		Damage:    30,
		Speed:     80,
		Accel:     40,
		TurnRate:  math.Pi, // 180°/s — generous, missiles can pivot hard
		HitRadius: 12,
		TTL:       15 * time.Second,
		StrafeK:   0.8,
		FrictionK: 0.1,
	}
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

// TickMissile integrates one missile by dt seconds. targetAlive=true
// when the sector worker found the target in its live set; in that case
// targetPos must be the target's current Pos and the missile updates
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
	spec MissileSpec,
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
	radStep := spec.TurnRate * dt
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
	accel := spec.Accel * dt
	if turning {
		dot := newDir.X*targetDir.X + newDir.Y*targetDir.Y
		if dot < 0 {
			accel = (spec.FrictionK + spec.Accel*0.1) * dt
		}
	}
	acc := newDir.Scale(accel)

	// Step 4: strafe compensation against perpendicular drift.
	strafeMax := spec.StrafeK * spec.Accel * dt
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
	rubMag := spec.FrictionK * speedEq * dt
	rub := speedDir.Scale(-rubMag)

	// Step 6: integrate velocity, clamp magnitude.
	newVel := m.Vel.Add(acc).Add(rub).Add(addStrafe)
	if mag := newVel.Length(); mag > spec.Speed {
		newVel = newVel.Scale(spec.Speed / mag)
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
		if pointSegmentDistance(targetPos, oldPos, newPos) <= spec.HitRadius {
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
