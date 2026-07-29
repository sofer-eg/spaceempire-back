package sector

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

// TestUnit_missileTargetable enumerates the missile target set (TASK-113 FR-07,
// widened by TASK-110/111): a different ship — a spacesuit is a ships row, so EVA
// rides that branch — every destructible static including gates, and a loot
// container. Self and the non-damageable kinds are not. The container is where the
// missile set is wider than the torpedo's (torpedoTargetable stays on statics).
func TestUnit_missileTargetable(t *testing.T) {
	t.Parallel()
	const self = domain.ShipID(7)

	cases := []struct {
		name string
		ref  domain.EntityRef
		want bool
	}{
		{"other ship", domain.EntityRef{Kind: domain.EntityKindShip, ID: 9}, true},
		{"self ship", domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(self)}, false},
		{"station", domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}, true},
		{"shipyard", domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 1}, true},
		{"trade station", domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}, true},
		{"pirbase", domain.EntityRef{Kind: domain.EntityKindPirbase, ID: 1}, true},
		{"laser tower", domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 1}, true},
		{"satellite", domain.EntityRef{Kind: domain.EntityKindSatellite, ID: 1}, true},
		{"jammer", domain.EntityRef{Kind: domain.EntityKindJammer, ID: 1}, true},
		// TASK-110 gave gates combat state, which lifted ЧТЗ C-04.
		{"gate", domain.EntityRef{Kind: domain.EntityKindGate, ID: 1}, true},
		// TASK-111: a crate is destroyed with its cargo — denying loot is a move.
		{"container", domain.EntityRef{Kind: domain.EntityKindContainer, ID: 1}, true},
		{"drone excluded", domain.EntityRef{Kind: domain.EntityKindDrone, ID: 1}, false},
		{"torpedo excluded", domain.EntityRef{Kind: domain.EntityKindTorpedo, ID: 1}, false},
		{"unknown excluded", domain.EntityRef{Kind: domain.EntityKindUnknown, ID: 1}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, missileTargetable(self, tc.ref))
		})
	}
}
