package sector

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

// TestUnit_missileTargetable enumerates the missile target set (TASK-113 FR-07):
// a different ship and every destructible static are legal; self, gates, and
// other non-destructible kinds are not. Missiles mirror torpedoes, so this is
// the same set torpedoTargetable enforces.
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
		// Gates carry no EntityKind on the backend (ЧТЗ C-04, until TASK-110), so
		// the "gate is not a weapon target" rule is exercised through the other
		// non-destructible kinds a ref could carry.
		{"container excluded", domain.EntityRef{Kind: domain.EntityKindContainer, ID: 1}, false},
		{"drone excluded", domain.EntityRef{Kind: domain.EntityKindDrone, ID: 1}, false},
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
