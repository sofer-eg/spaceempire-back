package dto_test

import (
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
