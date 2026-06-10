package sector

import (
	"math"
	"testing"

	"spaceempire/back/internal/domain"
)

// Per-tick kinematics: dt is the "tick signal" but the SP-style port
// integrates in tick units so the numeric value of dt doesn't change
// behaviour.
const moveTestDT = 1.0

// TestUnit_MoveShip_ZeroVel_Arrives walks a ship from rest to a target
// 200 units away and confirms it does arrive (Pos snapped, Vel zero,
// Target cleared) within a generous upper bound on ticks.
func TestUnit_MoveShip_ZeroVel_Arrives(t *testing.T) {
	t.Parallel()
	target := domain.Vec2{X: 200, Y: 0}
	ship := &domain.Ship{
		ID:           1,
		Pos:          domain.Vec2{},
		Direction:    domain.Vec2{X: 1, Y: 0},
		MaxSpeed:     20,
		Acceleration: 10,
		TurnRate:     math.Pi / 4,
		Target:       &target,
	}

	const maxTicks = 60
	arrived := false
	for i := 0; i < maxTicks; i++ {
		moveShip(ship, moveTestDT)
		if ship.Target == nil {
			arrived = true
			break
		}
	}
	if !arrived {
		t.Fatalf("did not arrive in %d ticks; pos=%+v vel=%+v", maxTicks, ship.Pos, ship.Vel)
	}
	if ship.Pos != target {
		t.Fatalf("final pos=%+v, want %+v", ship.Pos, target)
	}
	if !ship.Vel.IsZero() {
		t.Fatalf("final vel=%+v, want zero", ship.Vel)
	}
}

// TestUnit_MoveShip_ReverseTakesMultipleTicks_AndDrifts validates the
// "submarine" feel: a ship at full cruise, told to go back the way it
// came, must spend several ticks rotating (Direction.X stays positive
// for at least the first tick) while inertia keeps Vel.X > 0. Only
// after the heading flips does the ship start moving in -X.
func TestUnit_MoveShip_ReverseTakesMultipleTicks_AndDrifts(t *testing.T) {
	t.Parallel()
	target := domain.Vec2{X: -200, Y: 0}
	ship := &domain.Ship{
		ID:           1,
		Pos:          domain.Vec2{X: 0, Y: 0},
		Vel:          domain.Vec2{X: 20, Y: 0},
		Direction:    domain.Vec2{X: 1, Y: 0},
		MaxSpeed:     20,
		Acceleration: 10,
		TurnRate:     math.Pi / 4, // 45° per tick → at least 4 ticks for π
		Target:       &target,
	}

	// First tick: rotation can shift at most π/4 ≈ 45°, so Direction.X
	// must still be ≥ cos(45°) ≈ 0.707. The ship has barely begun to
	// turn — the heading must NOT be flipped already.
	moveShip(ship, moveTestDT)
	if ship.Direction.X < 0.7 {
		t.Fatalf("after 1 tick Direction.X=%v, expected ~cos(π/4)=0.707 (turn limited per tick)", ship.Direction.X)
	}
	// Inertia: even though we're being told to go -X, Vel must still be
	// pointing in +X (the engine cannot brake faster than the ship's
	// momentum allows on tick 1).
	if ship.Vel.X <= 0 {
		t.Fatalf("after 1 tick Vel.X=%v, expected positive (drift inertia)", ship.Vel.X)
	}

	// Within a handful of ticks the ship must flip its heading and
	// start moving in -X. Drift back through origin first, then accel
	// outward — Pos.X must eventually become negative.
	for i := 0; i < 50; i++ {
		moveShip(ship, moveTestDT)
		if ship.Pos.X < 0 {
			break
		}
	}
	if ship.Pos.X >= 0 {
		t.Fatalf("after reversal manoeuvre Pos.X=%v, expected negative", ship.Pos.X)
	}
}

// TestUnit_MoveShip_NoTarget_KeepsInertia validates the inertia clause:
// a ship with non-zero Vel and no Target must coast at the same speed
// in the same direction, indefinitely.
func TestUnit_MoveShip_NoTarget_KeepsInertia(t *testing.T) {
	t.Parallel()
	ship := &domain.Ship{
		ID:           1,
		Pos:          domain.Vec2{},
		Vel:          domain.Vec2{X: 10, Y: 0},
		Direction:    domain.Vec2{X: 1, Y: 0},
		MaxSpeed:     20,
		Acceleration: 10,
		TurnRate:     math.Pi / 4,
		Target:       nil,
	}

	moveShip(ship, moveTestDT)
	if ship.Vel.X != 10 || ship.Vel.Y != 0 {
		t.Fatalf("inertia lost: vel=%+v, want (10, 0)", ship.Vel)
	}
	if ship.Pos.X != 10 || ship.Pos.Y != 0 {
		t.Fatalf("coast pos=%+v, want (10, 0) after one per-tick step", ship.Pos)
	}
}
