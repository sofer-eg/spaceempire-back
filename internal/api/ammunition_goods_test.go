package api_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// TestUnit_AmmunitionGoodsIDsAreInTheCatalog checks that every goods id a command
// spends from the hold names a good that actually exists in configs/balance.yaml,
// and names the one it means to.
//
// This is the check whose absence let TASK-167's defect live. The handler tests
// assert their literal id (see install_satellite_test.go), which catches a constant
// aimed at the wrong good — but nothing caught a constant aimed at NO good. Goods
// 50 «Missile» and 51 «Combat Drone» existed only in goods_types, never in the
// catalog the client is served, so every launch spent something the player could
// not be shown a name for and no station sold. Deleting those rows then left
// sector's kill drop pointing at an id that had ceased to exist in either place,
// and it failed silently: a missile stack simply stopped matching and dropped in
// full instead of going through the SP's probabilistic throw.
//
// A unit test on purpose — the catalog is a file, so this needs no database and
// runs on every `make test-unit`, which is where a wrong constant should be caught.
// The table's side of the same invariant (name and space agreeing between
// goods_types and the YAML) is TestIntegration_BalanceCatalog_MatchesGoodsTypesTable.
//
// Names are spelled out rather than read from the constant's own doc comment so
// that a copy-paste of the wrong id fails here instead of quietly agreeing with
// itself.
func TestUnit_AmmunitionGoodsIDsAreInTheCatalog(t *testing.T) {
	t.Parallel()

	cat, err := balance.LoadFromFile("../../configs/balance.yaml")
	require.NoError(t, err)

	for _, tc := range []struct {
		what string
		id   domain.GoodsTypeID
		name string
	}{
		{"missile launch + starter magazine", domain.MissileGoodsType, "Ракета Москит"},
		{"drone salvo", domain.DroneGoodsType, "Боевой дрон"},
		{"satellite install", api.SatelliteGoodsType, "Навигационный спутник"},
		{"jammer install", api.JammerGoodsType, "Генератор гипер-помех"},
		{"torpedo class 2", api.TorpedoFirestormGoodsType, `Торпеда "Огненная Буря"`},
		{"torpedo class 3", api.TorpedoHolyGoodsType, "Святая Торпеда"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			t.Parallel()
			g, ok := cat.Get(tc.id)
			require.True(t, ok,
				"%s spends goods %d, which is not in configs/balance.yaml: the client cannot label it "+
					"and no station can sell it", tc.what, tc.id)
			assert.Equal(t, tc.name, g.Name,
				"%s spends goods %d, which the catalog calls %q", tc.what, tc.id, g.Name)
		})
	}
}
