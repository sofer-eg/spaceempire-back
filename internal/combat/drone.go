package combat

import (
	"math"
	"time"

	"spaceempire/back/internal/domain"
)

// DroneSpec captures the per-class combat-drone knobs ported from SP
// `TO_Drones` (model=1) and `ct_drones`. Phase 4.4 has exactly one class
// so every drone is launched with DefaultDroneSpec; future phases may
// load this from a balance catalog. See back/docs/specs/drones.md §6.
type DroneSpec struct {
	// Damage applied to the target every tick the drone is in FireRange.
	Damage int
	// HP is the drone's starting hull. (Phase 4.4 drones are not yet
	// targetable, so HP only matters for the persisted snapshot and the
	// SPA health hint; combat-against-drones arrives with 6.2.)
	HP int
	// FireRange is the weapon reach: the drone fires when the target is
	// within this distance and in front of its nose.
	FireRange float64
	// StandoffRange is the distance at which the drone stops closing and
	// brakes to hold station. Keep < FireRange so it shoots while
	// hovering (SP `parrots_to_achieve` braking).
	StandoffRange float64
	// Speed is the upper bound on |Vel|, world units per second. The
	// integrator scales by dt so the same spec works at any tick rate.
	Speed float64
	// Accel is the per-second engine acceleration along Direction.
	Accel float64
	// TurnRate is the maximum heading change per second (radians).
	TurnRate float64
	// StrafeK is the strafe-compensation cap as a fraction of Accel
	// (SP `drn_strafe = 0.6 * acceleration`).
	StrafeK float64
	// FrictionK is the per-second proportional drag coefficient
	// (SP `drn_rub = 0.1 * speed`).
	FrictionK float64
	// TTL is the wall-clock lifetime from launch to forced self-destruct.
	TTL time.Duration
}

// DefaultDroneSpec returns the single drone class of phase 4.4. Values
// calibrated against a 3-second tick and the starter ship's MaxSpeed≈20:
// the drone is ~3× faster so it keeps up with and orbits its target,
// holds station at StandoffRange, and chips the target down at Damage per
// tick (weaker than a missile's 30 one-shot, but it fires every tick).
func DefaultDroneSpec() DroneSpec {
	return DroneSpec{
		Damage:        8,
		HP:            20,
		FireRange:     60,
		StandoffRange: 50,
		Speed:         60,
		Accel:         30,
		TurnRate:      math.Pi,
		StrafeK:       0.6,
		FrictionK:     0.1,
		TTL:           120 * time.Second,
	}
}

// DroneOutcome reports the per-tick verdict TickDrone returns to the
// sector worker.
type DroneOutcome uint8

const (
	// DroneKeep means the drone is still alive; the worker keeps it in
	// the live set, marks it dirty, and may fire (see DroneCanFire).
	DroneKeep DroneOutcome = iota
	// DroneExpired means the drone's TTL ran out this tick. The worker
	// removes it (immediate DELETE) and emits an expired DroneImpact.
	DroneExpired
)

// LaunchDrone builds a fresh Drone fired from owner at target. The drone
// spawns at the owner's position and inherits its velocity/direction so a
// strafing pilot does not eject backwards-drifting drones; the per-drone
// spawn spread (so a salvo does not stack pixel-perfect) is applied by the
// caller. Pure builder — the sector worker allocates the id (DB-assigned)
// and inserts it into the live set.
func LaunchDrone(
	id domain.DroneID,
	spec DroneSpec,
	owner *domain.Ship,
	target domain.EntityRef,
	now time.Time,
) *domain.Drone {
	dir := owner.Direction
	if dir.IsZero() {
		dir = domain.Vec2{X: 1, Y: 0}
	}
	return &domain.Drone{
		ID:          id,
		SectorID:    owner.SectorID,
		OwnerShipID: owner.ID,
		PlayerID:    owner.PlayerID,
		Pos:         owner.Pos,
		Vel:         owner.Vel,
		Direction:   dir,
		Target:      target,
		HP:          spec.HP,
		Damage:      spec.Damage,
		ExpiresAt:   now.Add(spec.TTL),
	}
}

// TickDrone integrates one drone by dt seconds toward destPos and returns
// whether it survives the tick. destPos is the target's current Pos when
// the target is alive in this sector, otherwise the owner's Pos (loiter).
// Owner-gone self-destruct is decided by the worker before calling this;
// here the only death cause is TTL.
//
// The movement is a port of the SP `TO_Drones` (model=1) integrator at
// the same fidelity TickMissile ported `TO_Missiles` (see
// back/docs/specs/drones.md §1): rotate Direction toward the destination
// by TurnRate·dt (snapping on overshoot), accelerate along the new
// heading, strafe-compensate the perpendicular drift, apply proportional
// friction, clamp to Speed, integrate position. The drone-specific
// addition is standoff braking: within StandoffRange it decelerates
// toward zero velocity instead of accelerating, so it holds station and
// fires repeatedly rather than flying through like a missile.
//
// All mutations of d happen here; the worker relies on the
// one-writer-per-sector invariant so no other goroutine touches d.
func TickDrone(
	d *domain.Drone,
	destPos domain.Vec2,
	spec DroneSpec,
	dt float64,
	now time.Time,
) DroneOutcome {
	if d == nil {
		return DroneExpired
	}
	if !now.Before(d.ExpiresAt) {
		return DroneExpired
	}

	delta := destPos.Sub(d.Pos)
	rng := delta.Length()

	noTurn := false
	var targetDir domain.Vec2
	if rng > 1 {
		targetDir = delta.Scale(1.0 / rng)
	} else {
		targetDir = d.Direction
		noTurn = true
	}

	newDir := rotateDirToward(d.Direction, targetDir, spec.TurnRate*dt, noTurn)

	speedEq := d.Vel.Length()
	var speedDir domain.Vec2
	if speedEq > 1 {
		speedDir = d.Vel.Scale(1.0 / speedEq)
	} else {
		speedDir = newDir
	}

	// Acceleration: close in when outside the standoff ring, brake toward
	// zero velocity once inside it so the drone holds station near the
	// target (SP parrots_to_achieve braking).
	var acc domain.Vec2
	if rng > spec.StandoffRange {
		acc = newDir.Scale(spec.Accel * dt)
	} else {
		brake := spec.Accel * dt
		if brake > speedEq {
			brake = speedEq
		}
		acc = speedDir.Scale(-brake)
	}

	// Strafe compensation cancels the velocity component perpendicular to
	// the target line, up to StrafeK*Accel*dt magnitude.
	addStrafe := domain.Vec2{}
	if strafeMax := spec.StrafeK * spec.Accel * dt; strafeMax > 0 && rng > 1 {
		perp := domain.Vec2{X: -targetDir.Y, Y: targetDir.X}
		side := (d.Vel.X+acc.X)*perp.X + (d.Vel.Y+acc.Y)*perp.Y
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

	// Proportional friction against current velocity.
	rub := speedDir.Scale(-spec.FrictionK * speedEq * dt)

	newVel := d.Vel.Add(acc).Add(rub).Add(addStrafe)
	if mag := newVel.Length(); mag > spec.Speed {
		newVel = newVel.Scale(spec.Speed / mag)
	}

	d.Pos = d.Pos.Add(newVel.Scale(dt))
	d.Vel = newVel
	d.Direction = newDir
	return DroneKeep
}

// DroneCanFire reports whether the drone may shoot its target this tick:
// the target must be within FireRange and in front of the drone's nose
// (SP fire gate `range_to_target_x>0 && range<fire_range`). The worker
// pairs a true result with combat.ApplyDamage(targetShip, d.Damage).
func DroneCanFire(d *domain.Drone, targetPos domain.Vec2, spec DroneSpec) bool {
	if d == nil {
		return false
	}
	delta := targetPos.Sub(d.Pos)
	rng := delta.Length()
	if rng > spec.FireRange {
		return false
	}
	if rng <= 1 {
		return true
	}
	// In front: the unit vector to the target points the same way as the
	// drone's heading (positive dot product).
	unit := delta.Scale(1.0 / rng)
	return unit.X*d.Direction.X+unit.Y*d.Direction.Y > 0
}

// rotateDirToward rotates the unit vector dir toward targetDir by at most
// radStep radians, snapping exactly onto targetDir when a single step
// would overshoot (or when radStep >= π, the instant-turn shortcut from
// SP `grad_speed > 180`). noTurn keeps dir unchanged (target coincident
// with the drone). Mirrors the rotation block in combat.TickMissile.
func rotateDirToward(dir, targetDir domain.Vec2, radStep float64, noTurn bool) domain.Vec2 {
	if noTurn {
		return dir
	}
	if radStep >= math.Pi {
		return targetDir
	}
	ckX := targetDir.X*dir.X + targetDir.Y*dir.Y
	ckY := targetDir.X*(-dir.Y) + targetDir.Y*dir.X
	if math.Abs(ckY) < 0.01 && ckX > 0 {
		return targetDir
	}
	step := radStep
	if ckY < 0 {
		step = -radStep
	}
	cs := math.Cos(step)
	sn := math.Sin(step)
	rotated := domain.Vec2{
		X: cs*dir.X - sn*dir.Y,
		Y: sn*dir.X + cs*dir.Y,
	}
	newCkY := targetDir.X*(-rotated.Y) + targetDir.Y*rotated.X
	if (ckY > 0) != (newCkY > 0) {
		return targetDir
	}
	return rotated
}
