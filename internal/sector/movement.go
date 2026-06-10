package sector

import (
	"math"

	"spaceempire/back/internal/domain"
)

// moveShip is a Go port of `TO_ShipMovement` from the original StarWind
// (sql/db.sql:31541). It uses the per-tick units and direction-vector
// approach of the SP — the dt parameter is accepted for the signature
// but only consulted as a "tick happened" gate; all kinematic state is
// integrated in tick steps (pos += vel, vel += accel·dir, dir rotated
// by turn_rate radians) the way the SP does.
//
// Why per-tick: the SP's `if (ship_grad_speed > 180) snap` shortcut
// fires the moment the per-tick turn would exceed half a turn. Treating
// TurnRate as rad/sec and multiplying by a 3-second tick instantly hit
// that shortcut and produced the "ship teleports its heading" symptom
// players reported.
//
// Behaviour:
//   - Target == nil: coast (Pos += Vel). Vel and Direction preserved.
//   - rangeEq < 1 && speedEq < 1: snap-arrive, zero Vel, clear Target.
//   - Otherwise: rotate Direction toward target (rotation matrix step),
//     then thrust along Direction unless turning while still far away.
//     Brake when within v²/(2·a) of the target.
//   - Acceleration <= 0 or TurnRate <= 0: legacy fixture mode for
//     pre-3.18 tests that pass `Ship{MaxSpeed: N}` without physics —
//     same shortcut the SP takes for "perfect ships".
func moveShip(ship *domain.Ship, dt float64) bool {
	if dt <= 0 {
		return false
	}

	if ship.Target == nil {
		if ship.Vel.IsZero() {
			return false
		}
		ship.Pos = ship.Pos.Add(ship.Vel)
		return true
	}

	delta := ship.Target.Sub(ship.Pos)
	rangeEq := delta.Length()
	speedEq := ship.Vel.Length()

	if rangeEq < 1 && speedEq < 1 {
		ship.Pos = *ship.Target
		ship.Vel = domain.Vec2{}
		ship.Target = nil
		clearTargetRefOnArrival(ship)
		return true
	}

	if ship.MaxSpeed <= 0 {
		if ship.Vel.IsZero() {
			return false
		}
		ship.Vel = domain.Vec2{}
		return true
	}

	// Target unit vector. When rangeEq is tiny the SP keeps the previous
	// direction; we do the same so noise near the target doesn't churn
	// the heading.
	var targetDir domain.Vec2
	if rangeEq >= 1 {
		targetDir = domain.Vec2{X: delta.X / rangeEq, Y: delta.Y / rangeEq}
	} else {
		targetDir = ship.Direction
	}

	// Bootstrap a degenerate Direction (legacy rows with (0,0)) to the
	// target — matches the SP's `if (dir_eq<0.9) then set no_turn_ship=1`
	// path.
	dirSq := ship.Direction.X*ship.Direction.X + ship.Direction.Y*ship.Direction.Y
	if dirSq < 0.9 {
		if targetDir.X == 0 && targetDir.Y == 0 {
			ship.Direction = domain.Vec2{X: 1, Y: 0}
		} else {
			ship.Direction = targetDir
		}
	}

	// Rotation step — the SP encodes the relative angle as a dot/cross
	// pair so it never calls atan2. `checkDirX = cos(θ)` and
	// `checkDirY = sin(θ)` where θ is signed angle Direction→target.
	checkDirX := targetDir.X*ship.Direction.X + targetDir.Y*ship.Direction.Y
	checkDirY := targetDir.X*(-ship.Direction.Y) + targetDir.Y*ship.Direction.X

	turning := false
	var newDir domain.Vec2
	switch {
	case checkDirX > 0 && math.Abs(checkDirY) < 0.01:
		// Already aligned within ~0.6° — snap to remove FP drift.
		newDir = targetDir
	case ship.TurnRate <= 0 || ship.TurnRate >= math.Pi:
		// Legacy / "perfect" — instant turn, matches SP grad>180 branch.
		newDir = targetDir
	default:
		turning = true
		sign := 1.0
		if checkDirY < 0 {
			sign = -1.0
		}
		cosStep := math.Cos(ship.TurnRate)
		sinStep := math.Sin(sign * ship.TurnRate)
		// Rotation matrix application (SP uses the same form):
		//   new.x = c·d.x - s·d.y
		//   new.y = s·d.x + c·d.y
		rotated := domain.Vec2{
			X: cosStep*ship.Direction.X - sinStep*ship.Direction.Y,
			Y: sinStep*ship.Direction.X + cosStep*ship.Direction.Y,
		}
		// Overshoot check: if after rotating the cross-product flipped
		// sign, we crossed the target heading mid-step → snap.
		afterCross := targetDir.X*(-rotated.Y) + targetDir.Y*rotated.X
		if sign*afterCross <= 0 {
			newDir = targetDir
			turning = false
		} else {
			newDir = rotated
		}
	}
	ship.Direction = newDir

	// Velocity update: friction (rub) + thrust along Direction. Per-tick.
	// The SP uses `rub = -0.1·max_speed` for moving ships and a softer
	// `-(speed_eq)` near rest so a still ship doesn't lose negative
	// "speed".
	var rub float64
	if speedEq > 0.1*ship.MaxSpeed {
		rub = -0.1 * ship.MaxSpeed
	} else {
		rub = -speedEq
	}

	// Gas decision: brake when within stop distance; coast (gas=0) when
	// turning hard far from target; thrust otherwise. The SP has a more
	// elaborate gas_add policy (closing speed, strafe, fly_mode); we
	// port only the core that the player-flown sub case needs.
	stopDist := 0.0
	if ship.Acceleration > 0 {
		stopDist = (speedEq * speedEq) / (2 * ship.Acceleration)
	}
	gas := 1.0
	if rangeEq <= stopDist {
		gas = -1.0
	}
	const closeManoeuvreRange = 100.0
	if turning && rangeEq > closeManoeuvreRange {
		gas = 0
	}

	accelMag := gas * ship.Acceleration
	thrustX := accelMag * ship.Direction.X
	thrustY := accelMag * ship.Direction.Y

	var rubX, rubY float64
	if speedEq > 0 {
		rubX = rub * ship.Vel.X / speedEq
		rubY = rub * ship.Vel.Y / speedEq
	}

	newVel := domain.Vec2{
		X: ship.Vel.X + thrustX + rubX,
		Y: ship.Vel.Y + thrustY + rubY,
	}

	// Acceleration <= 0 means "no physical accel" — legacy fixture mode.
	// Skip the velocity update and behave like the old straight-line
	// integrator: vel = max_speed · direction.
	if ship.Acceleration <= 0 {
		newVel = domain.Vec2{
			X: ship.MaxSpeed * ship.Direction.X,
			Y: ship.MaxSpeed * ship.Direction.Y,
		}
	}

	// Cap |vel| at MaxSpeed.
	newSpeed := newVel.Length()
	if newSpeed > ship.MaxSpeed && newSpeed > 0 {
		newVel = domain.Vec2{
			X: newVel.X * ship.MaxSpeed / newSpeed,
			Y: newVel.Y * ship.MaxSpeed / newSpeed,
		}
		newSpeed = ship.MaxSpeed
	}

	// Overshoot guard: if the integrated step would jump past the
	// target, clamp pos to target and arrive — the SP relies on
	// auto-docking for this, we don't (phase 3.18 task), so do it here.
	stepLen := newSpeed
	if stepLen >= rangeEq && rangeEq > 0 {
		ship.Pos = *ship.Target
		ship.Vel = domain.Vec2{}
		ship.Target = nil
		clearTargetRefOnArrival(ship)
		return true
	}

	ship.Vel = newVel
	ship.Pos = ship.Pos.Add(newVel)
	return true
}

// clearTargetRefOnArrival drops the SPA highlight ref when the ship just
// reached its per-tick waypoint. The exception is approach-mode autopilot
// (Course.Approach != nil): the player has parked next to a static and
// the SPA still wants to highlight what they're parked at until they
// explicitly dock, undock, or pick a new target.
func clearTargetRefOnArrival(ship *domain.Ship) {
	if ship.FinalTarget != nil && ship.FinalTarget.Approach != nil {
		return
	}
	ship.CurrentTargetRef = nil
}
