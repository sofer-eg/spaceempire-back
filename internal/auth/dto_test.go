package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
)

// Phase 10.10: registration accepts only the playable races 1..5; 0 (unset)
// and the NPC-only races 6..8 (and the special 100) are rejected.
func TestUnit_RegisterRequest_Validate_Race(t *testing.T) {
	t.Parallel()

	for r := domain.RaceID(1); r <= 5; r++ {
		req := auth.RegisterRequest{Login: "sofer", Password: "1", Race: r}
		assert.NoError(t, req.Validate(), "race %d should be playable", r)
	}
	for _, r := range []domain.RaceID{0, 6, 7, 8, 100} {
		req := auth.RegisterRequest{Login: "sofer", Password: "1", Race: r}
		assert.Error(t, req.Validate(), "race %d should be rejected", r)
	}
}
