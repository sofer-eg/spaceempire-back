package sector

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

// White-box tests for the torpedo snapshot/AOI helpers (TASK-100.3.5.7). They
// pin the pure-function behaviour the broadcaster relies on: the spatial AOI
// filter, the add/update/remove diff, and the impact AOI filter — all
// deterministic, no worker goroutine. The end-to-end delivery is covered by the
// black-box subscription test in torpedo_ws_test.go.

func mkTorpedo(id domain.TorpedoID, pos domain.Vec2) *domain.Torpedo {
	return &domain.Torpedo{
		ID:           id,
		OwnerShipID:  1,
		Target:       domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Pos:          pos,
		Class:        3,
		Damage:       600,
		HP:           60,
		SplashRadius: 70,
	}
}

func torpedoIDs(ts []domain.Torpedo) []domain.TorpedoID {
	out := make([]domain.TorpedoID, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// TestUnit_torpedosInRadius_FiltersByDistance: the per-tick AOI gate keeps only
// torpedoes within radius of the subscriber centre; radius<=0 disables it. This
// is the unit-level proof that a torpedo outside a player's radar never enters
// the diff (ЧТЗ AC-10 / NFR-003).
func TestUnit_torpedosInRadius_FiltersByDistance(t *testing.T) {
	t.Parallel()
	src := map[domain.TorpedoID]*domain.Torpedo{
		1: mkTorpedo(1, domain.Vec2{X: 100, Y: 0}),   // inside a 1000 radius
		2: mkTorpedo(2, domain.Vec2{X: 10000, Y: 0}), // far outside
	}

	in := torpedosInRadius(src, domain.Vec2{}, 1000)
	require.Len(t, in, 1, "only the near torpedo is in range")
	_, near := in[1]
	require.True(t, near)
	_, far := in[2]
	require.False(t, far, "the far torpedo is filtered out of the AOI")

	require.Len(t, torpedosInRadius(src, domain.Vec2{}, 0), 2, "radius<=0 disables the filter")
	require.Nil(t, torpedosInRadius(nil, domain.Vec2{}, 1000))
}

// TestUnit_diffTorpedos_AddUpdateRemove: the per-tick delta classifies a new
// torpedo as added, a moved torpedo as updated, a vanished one as removed, and
// leaves an unchanged torpedo out of every bucket (ЧТЗ FR-010, AC-10).
func TestUnit_diffTorpedos_AddUpdateRemove(t *testing.T) {
	t.Parallel()
	moved := *mkTorpedo(2, domain.Vec2{X: 0, Y: 0})
	movedNext := moved
	movedNext.Pos.X = 50 // one homing step
	unchanged := *mkTorpedo(3, domain.Vec2{X: 5, Y: 5})

	prev := map[domain.TorpedoID]domain.Torpedo{
		2: moved,
		3: unchanged,
		4: *mkTorpedo(4, domain.Vec2{X: 9, Y: 9}), // detonated/expired this tick
	}
	curr := map[domain.TorpedoID]domain.Torpedo{
		1: *mkTorpedo(1, domain.Vec2{X: 1, Y: 1}), // launched this tick
		2: movedNext,                              // moved → updated
		3: unchanged,                              // unchanged → absent everywhere
	}

	added, updated, removed := diffTorpedos(prev, curr)
	require.Equal(t, []domain.TorpedoID{1}, torpedoIDs(added))
	require.Equal(t, []domain.TorpedoID{2}, torpedoIDs(updated))
	require.Equal(t, []domain.TorpedoID{4}, removed)
}

// TestUnit_filterTorpedoImpactsForAOI_KeepsInRadiusPreservesFields: the impact
// AOI filter drops blasts outside the subscriber window and passes the in-window
// detonation through intact — blast centre, SplashRadius and the Hit outcome are
// exactly what the renderer needs (ЧТЗ §5.3, AC-10).
func TestUnit_filterTorpedoImpactsForAOI_KeepsInRadiusPreservesFields(t *testing.T) {
	t.Parallel()
	hit := TorpedoImpact{
		TorpedoID:    1,
		OwnerShipID:  1,
		Target:       domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		Pos:          domain.Vec2{X: 100, Y: 0},
		SplashRadius: 70,
		Hit:          true,
	}
	far := TorpedoImpact{TorpedoID: 2, Pos: domain.Vec2{X: 9000, Y: 0}, Expired: true}

	out := filterTorpedoImpactsForAOI([]TorpedoImpact{hit, far}, domain.Vec2{}, 1000)
	require.Len(t, out, 1, "only the in-radius blast survives the AOI filter")
	require.Equal(t, domain.TorpedoID(1), out[0].TorpedoID)
	require.True(t, out[0].Hit, "outcome type preserved")
	require.Equal(t, float64(70), out[0].SplashRadius, "splash radius preserved for the blast animation")
	require.Equal(t, domain.Vec2{X: 100, Y: 0}, out[0].Pos, "blast centre preserved")

	require.Nil(t, filterTorpedoImpactsForAOI(nil, domain.Vec2{}, 1000))
}
