package sector

import (
	"math"
	"testing"

	"spaceempire/back/internal/domain"
)

// TestUnit_MoveShip_LastStep_ChaseHasVector reproduces TASK-119: an NPC that
// re-targets a slowly-drifting enemy every tick moves via the overshoot snap
// (Pos = *Target, Vel = 0) each tick — so the integrator Vel stays ~0 even
// though the ship really crawls across the sector. The display velocity
// LastStep must reflect the actual per-tick step so the client draws the
// course-vector arrow. Before the fix LastStep does not exist / stays zero
// and this fails.
func TestUnit_MoveShip_LastStep_ChaseHasVector(t *testing.T) {
	t.Parallel()

	ship := &domain.Ship{
		ID:           1,
		Pos:          domain.Vec2{},
		Direction:    domain.Vec2{X: 1, Y: 0},
		MaxSpeed:     20,
		Acceleration: 10,
		TurnRate:     math.Pi / 4,
	}

	// Enemy sits just ahead and drifts ~4u/tick — inside one accel step, so
	// the overshoot guard fires every tick (Vel zeroed, Pos snapped).
	enemyX := 4.0
	const ticks = 60
	moved := 0
	for i := 0; i < ticks; i++ {
		prev := ship.Pos
		enemy := domain.Vec2{X: enemyX, Y: 0}
		ship.Target = &enemy
		moveShip(ship, moveTestDT)
		enemyX += 4.0

		step := ship.Pos.Sub(prev).Length()
		if step <= 0.5 {
			continue // ship did not move this tick — no vector expected
		}
		moved++
		if ship.LastStep.Length() <= 1.0 {
			t.Fatalf("tick %d: ship stepped %.2fu (Vel=%+v) but LastStep=%+v (len %.3f) — course vector would be hidden",
				i, step, ship.Vel, ship.LastStep, ship.LastStep.Length())
		}
	}
	if moved < 30 {
		t.Fatalf("expected the chaser to move on most ticks, moved on only %d/%d", moved, ticks)
	}
}

// TestUnit_MoveShip_LastStep_StoppedHasNoVector is the arrival regression: a
// ship that flies to a fixed target and stops must end with LastStep ~0 (no
// phantom arrow) AND Vel ~0 (stop physics intact — the AI arrival criterion
// still fires).
func TestUnit_MoveShip_LastStep_StoppedHasNoVector(t *testing.T) {
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
	for i := 0; i < maxTicks; i++ {
		moveShip(ship, moveTestDT)
		if ship.Target == nil {
			break
		}
	}
	if ship.Target != nil {
		t.Fatalf("did not arrive in %d ticks", maxTicks)
	}

	// One more tick at rest (Target==nil, Vel==0): LastStep must collapse to
	// zero so the client drops the arrow on the stopped ship.
	moveShip(ship, moveTestDT)
	if !ship.Vel.IsZero() {
		t.Fatalf("stopped ship Vel=%+v, want zero (stop physics)", ship.Vel)
	}
	if ship.LastStep.Length() > 0.001 {
		t.Fatalf("stopped ship LastStep=%+v, want ~zero (no phantom vector)", ship.LastStep)
	}
}

// TestUnit_MoveShip_LastStep_PatrolKeepsVector guards the case that already
// worked (race patrol point runs 15u/tick ahead, the ship trails it under
// thrust with a non-zero Vel): LastStep must stay well above the client's
// hide threshold so the vector remains visible.
func TestUnit_MoveShip_LastStep_PatrolKeepsVector(t *testing.T) {
	t.Parallel()

	ship := &domain.Ship{
		ID:           1,
		Pos:          domain.Vec2{},
		Direction:    domain.Vec2{X: 1, Y: 0},
		MaxSpeed:     20,
		Acceleration: 10,
		TurnRate:     math.Pi / 4,
	}

	// Warm up: give the ship a fixed far target so it reaches cruise speed.
	far := domain.Vec2{X: 400, Y: 0}
	ship.Target = &far
	for i := 0; i < 20; i++ {
		moveShip(ship, moveTestDT)
	}

	// Patrol: a waypoint that keeps running 15u ahead each tick. The ship
	// trails it at cruise; LastStep must stay visible (> VEC_MIN-equivalent).
	wpX := ship.Pos.X + 15
	for i := 0; i < 20; i++ {
		wp := domain.Vec2{X: wpX, Y: 0}
		ship.Target = &wp
		moveShip(ship, moveTestDT)
		wpX += 15
		if ship.LastStep.Length() <= 2.0 {
			t.Fatalf("patrol tick %d: LastStep=%+v (len %.3f) below the visible threshold",
				i, ship.LastStep, ship.LastStep.Length())
		}
	}
}
