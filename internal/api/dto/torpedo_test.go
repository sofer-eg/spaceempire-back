package dto_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
)

// TestUnit_TorpedoFromDomain maps the render-relevant torpedo fields onto the
// wire form: id/owner/target, the split Pos/Vel/Direction scalars, the ammo
// class and the shoot-downable HP (TASK-100.3.5.7, ЧТЗ FR-010).
func TestUnit_TorpedoFromDomain(t *testing.T) {
	t.Parallel()

	got := dto.TorpedoFromDomain(domain.Torpedo{
		ID:          7,
		OwnerShipID: 1,
		Target:      domain.EntityRef{Kind: domain.EntityKindStation, ID: 5},
		Pos:         domain.Vec2{X: 10, Y: 20},
		Vel:         domain.Vec2{X: 1, Y: 2},
		Direction:   domain.Vec2{X: 0, Y: 1},
		Class:       3,
		HP:          60,
	})

	assert.Equal(t, int64(7), got.ID)
	assert.Equal(t, int64(1), got.Owner)
	assert.Equal(t, dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 5}, got.Target)
	assert.Equal(t, 10.0, got.X)
	assert.Equal(t, 20.0, got.Y)
	assert.Equal(t, 1.0, got.VX)
	assert.Equal(t, 2.0, got.VY)
	assert.Equal(t, 0.0, got.DirX)
	assert.Equal(t, 1.0, got.DirY)
	assert.Equal(t, 3, got.Class)
	assert.Equal(t, 60, got.HP)
}

// TestUnit_TorpedoImpact_JSONShape locks the impact wire shape: a Hit carries the
// splash radius for the blast animation, while a non-Hit outcome (Expired) omits
// the zero-valued splash/Hit/Killed flags via omitempty so the SPA reads them as
// falsy (ЧТЗ §5.3).
func TestUnit_TorpedoImpact_JSONShape(t *testing.T) {
	t.Parallel()

	hit, err := json.Marshal(dto.TorpedoImpact{
		TorpedoID: 7, Owner: 1, X: 100, Y: 0, SplashRadius: 70, Hit: true,
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"torpedoID":7,"owner":1,"target":{"kind":0,"id":0},"x":100,"y":0,"splashRadius":70,"hit":true}`, string(hit))

	expired, err := json.Marshal(dto.TorpedoImpact{TorpedoID: 8, X: 5, Y: 5, Expired: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"torpedoID":8,"owner":0,"target":{"kind":0,"id":0},"x":5,"y":5,"expired":true}`, string(expired))
}
