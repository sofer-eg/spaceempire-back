package balance_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
)

func TestUnit_NewStationTypes_BuildsAndLooksUp(t *testing.T) {
	cat, err := balance.NewStationTypes([]balance.StationType{
		{ID: 0, Name: "Строящаяся станция", Kind: balance.StationKindFactory},
		{ID: 1, Name: "Электростанция", RaceID: 0, Kind: balance.StationKindFactory, Sellable: true},
	})
	require.NoError(t, err)
	require.Equal(t, 2, cat.StationTypeCount())

	st, ok := cat.GetStationType(1)
	require.True(t, ok)
	assert.Equal(t, "Электростанция", st.Name)
	assert.Equal(t, balance.StationKindFactory, st.Kind)
	assert.Equal(t, "Фабрика", st.Kind.Label())

	_, ok = cat.GetStationType(999)
	assert.False(t, ok)
}

func TestUnit_NewStationTypes_Rejects(t *testing.T) {
	_, err := balance.NewStationTypes([]balance.StationType{{ID: -1, Name: "x"}})
	require.ErrorIs(t, err, balance.ErrInvalidStationTypeID)

	_, err = balance.NewStationTypes([]balance.StationType{{ID: 1, Name: ""}})
	require.ErrorIs(t, err, balance.ErrEmptyStationTypeName)

	_, err = balance.NewStationTypes([]balance.StationType{{ID: 1, Name: "a"}, {ID: 1, Name: "b"}})
	require.ErrorIs(t, err, balance.ErrDuplicateStationTypeID)
}

// TestUnit_LoadStationTypes_RealConfig loads the converted catalog + recipes
// and folds the recipes into a Balance built from the real goods catalog. A
// green run proves every recipe line references a goods id present in
// balance.yaml (balance.New validates that) and that station_type 1 stays the
// electroplant (Crystals→Energy Cells) per the original station_goods_types.
func TestUnit_LoadStationTypes_RealConfig(t *testing.T) {
	cat, recipes, err := balance.LoadStationTypesFromFile("../../configs/station_types.yaml")
	require.NoError(t, err)
	assert.Greater(t, cat.StationTypeCount(), 100, "expected the full ~168-type catalog")
	assert.Greater(t, len(recipes), 100, "expected the full ~165-recipe set")

	// Spot-check the catalog against station_types.
	st, ok := cat.GetStationType(5)
	require.True(t, ok)
	assert.Equal(t, "Компьютерный завод", st.Name)

	goods, err := balance.LoadFromFile("../../configs/balance.yaml")
	require.NoError(t, err)
	bal, err := balance.New(goods.AllGoods(), recipes)
	require.NoError(t, err, "every recipe goods id must exist in balance.yaml")

	// station_type 1 = Электростанция: Crystals(4) → Energy Cells(1).
	r, ok := bal.Recipe(1)
	require.True(t, ok)
	require.Len(t, r.Inputs, 1)
	assert.EqualValues(t, 4, r.Inputs[0].GoodsType)
	require.Len(t, r.Outputs, 1)
	assert.EqualValues(t, 1, r.Outputs[0].GoodsType)
	assert.EqualValues(t, 909, r.Outputs[0].Quantity)
}
