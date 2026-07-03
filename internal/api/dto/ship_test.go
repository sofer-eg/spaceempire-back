package dto_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
)

// TestUnit_ShipFromDomain_HullCategory checks the resolver is invoked with the
// ship's class id and its result lands in HullCategory (phase 10.13).
func TestUnit_ShipFromDomain_HullCategory(t *testing.T) {
	t.Parallel()

	resolver := func(id domain.ShipClassID) string {
		if id == 81 {
			return "TS"
		}
		return ""
	}

	got := dto.ShipFromDomain(domain.Ship{ID: 1, ShipClassID: 81}, resolver)
	assert.Equal(t, "TS", got.HullCategory)

	// Unknown class id → resolver returns "" → field omitted.
	unknown := dto.ShipFromDomain(domain.Ship{ID: 2, ShipClassID: 999}, resolver)
	assert.Empty(t, unknown.HullCategory)
}

// TestUnit_ShipFromDomain_NilResolver guards the tests/minimal-deployment path:
// a nil resolver leaves HullCategory empty (client falls back to its heuristic).
func TestUnit_ShipFromDomain_NilResolver(t *testing.T) {
	t.Parallel()

	got := dto.ShipFromDomain(domain.Ship{ID: 1, ShipClassID: 81}, nil)
	assert.Empty(t, got.HullCategory)
}

// TestUnit_ShipFromDomain_MiningTarget checks the sustained-mining asteroid id
// is surfaced as a bare id, and omitted when the ship is not mining (phase
// 10.3.21 — drives the SPA's «Бурить/Стоп» toggle).
func TestUnit_ShipFromDomain_MiningTarget(t *testing.T) {
	t.Parallel()

	ast := domain.AsteroidID(42)
	mining := dto.ShipFromDomain(domain.Ship{ID: 1, MiningTarget: &ast}, nil)
	if assert.NotNil(t, mining.MiningTarget) {
		assert.Equal(t, int64(42), *mining.MiningTarget)
	}

	idle := dto.ShipFromDomain(domain.Ship{ID: 2}, nil)
	assert.Nil(t, idle.MiningTarget, "non-mining ship omits miningTarget")
}

// TestUnit_ShipFromDomain_VelocityVectorFromLastStep pins the DTO course-vector
// source to domain.Ship.LastStep, decoupled from the integrator Vel (TASK-119).
// A ship that stepped this tick exposes a non-zero Vx/Vy even when Vel is zero
// (arrival/overshoot snap); a ship at rest exposes zero.
func TestUnit_ShipFromDomain_VelocityVectorFromLastStep(t *testing.T) {
	t.Parallel()

	// Overshoot-snap tick: the ship really stepped (LastStep set) but the
	// integrator Vel was zeroed by the snap. The arrow must still show.
	moving := dto.ShipFromDomain(domain.Ship{
		ID:       1,
		LastStep: domain.Vec2{X: 4, Y: 3},
		Vel:      domain.Vec2{},
	}, nil)
	assert.Equal(t, 4.0, moving.Vx)
	assert.Equal(t, 3.0, moving.Vy)
	assert.Positive(t, math.Hypot(moving.Vx, moving.Vy), "moving ship must expose a course vector")

	// At rest: no step this tick → no arrow, regardless of a stale Vel.
	stopped := dto.ShipFromDomain(domain.Ship{
		ID:       2,
		LastStep: domain.Vec2{},
		Vel:      domain.Vec2{X: 9, Y: 9},
	}, nil)
	assert.Zero(t, stopped.Vx)
	assert.Zero(t, stopped.Vy)
}

// TestUnit_ShipsFromDomain_AppliesResolver checks the batch path stamps every
// ship via the same resolver.
func TestUnit_ShipsFromDomain_AppliesResolver(t *testing.T) {
	t.Parallel()

	resolver := func(id domain.ShipClassID) string {
		switch id {
		case 73:
			return "M1"
		case 81:
			return "TS"
		default:
			return ""
		}
	}

	out := dto.ShipsFromDomain([]domain.Ship{
		{ID: 1, ShipClassID: 73},
		{ID: 2, ShipClassID: 81},
	}, resolver)

	assert.Equal(t, "M1", out[0].HullCategory)
	assert.Equal(t, "TS", out[1].HullCategory)
}
