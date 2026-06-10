package balance_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

func TestUnit_New_HappyPath(t *testing.T) {
	b, err := balance.New([]balance.Goods{
		{ID: 1, Name: "Батарейки", Space: 1, AvgPrice: 16, MaxPrice: 96},
		{ID: 2, Name: "Железо", Space: 2, AvgPrice: 123, MaxPrice: 738},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, b.GoodsCount())

	got, ok := b.Get(domain.GoodsTypeID(2))
	require.True(t, ok)
	require.Equal(t, "Железо", got.Name)
	require.Equal(t, 2, got.Space)
}

func TestUnit_New_RejectsDuplicateID(t *testing.T) {
	_, err := balance.New([]balance.Goods{
		{ID: 1, Name: "A", Space: 1},
		{ID: 1, Name: "B", Space: 1},
	}, nil)
	require.ErrorIs(t, err, balance.ErrDuplicateGoodsID)
}

func TestUnit_New_RejectsNonPositiveID(t *testing.T) {
	_, err := balance.New([]balance.Goods{{ID: 0, Name: "A", Space: 1}}, nil)
	require.ErrorIs(t, err, balance.ErrInvalidGoodsID)
}

func TestUnit_New_RejectsEmptyName(t *testing.T) {
	_, err := balance.New([]balance.Goods{{ID: 1, Name: "", Space: 1}}, nil)
	require.ErrorIs(t, err, balance.ErrEmptyGoodsName)
}

func TestUnit_New_RejectsNegativeNumericFields(t *testing.T) {
	cases := []struct {
		name string
		g    balance.Goods
	}{
		{"negative space", balance.Goods{ID: 1, Name: "A", Space: -1}},
		{"negative avg_price", balance.Goods{ID: 1, Name: "A", Space: 1, AvgPrice: -1}},
		{"negative max_price", balance.Goods{ID: 1, Name: "A", Space: 1, MaxPrice: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := balance.New([]balance.Goods{tc.g}, nil)
			require.ErrorIs(t, err, balance.ErrNegativeGoodsField)
		})
	}
}

func TestUnit_New_AllowsZeroPriceAndSpace(t *testing.T) {
	b, err := balance.New([]balance.Goods{
		{ID: 323, Name: "Рабы", Space: 1, AvgPrice: 0, MaxPrice: 0},
		{ID: 106, Name: "Космическая ромашка", Space: 0, AvgPrice: 0, MaxPrice: 0},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, b.GoodsCount())
}

func TestUnit_Get_UnknownIDReturnsFalse(t *testing.T) {
	b, err := balance.New([]balance.Goods{{ID: 1, Name: "A", Space: 1}}, nil)
	require.NoError(t, err)
	_, ok := b.Get(domain.GoodsTypeID(999))
	require.False(t, ok)
}

func TestUnit_LoadFromFile_Sample(t *testing.T) {
	path := filepath.Join("testdata", "sample_balance.yaml")
	b, err := balance.LoadFromFile(path)
	require.NoError(t, err)
	require.Equal(t, 3, b.GoodsCount())

	g, ok := b.Get(domain.GoodsTypeID(1))
	require.True(t, ok)
	require.Equal(t, "Батарейки", g.Name)
	require.Equal(t, int64(16), g.AvgPrice)
	require.Equal(t, 10, g.ObjectTypeID)
}

func TestUnit_LoadFromFile_MissingFile(t *testing.T) {
	_, err := balance.LoadFromFile(filepath.Join("testdata", "does_not_exist.yaml"))
	require.Error(t, err)
}

func TestUnit_LoadFromFile_PropagatesValidationError(t *testing.T) {
	path := filepath.Join("testdata", "duplicate_id.yaml")
	_, err := balance.LoadFromFile(path)
	require.True(t, errors.Is(err, balance.ErrDuplicateGoodsID), "got %v", err)
}

func TestUnit_New_AcceptsRecipes(t *testing.T) {
	goods := []balance.Goods{
		{ID: 2, Name: "Iron", Space: 1},
		{ID: 7, Name: "Microchips", Space: 1},
	}
	recipes := []balance.Recipe{{
		StationType: 1,
		CycleTime:   30 * time.Second,
		Inputs:      []balance.RecipeLine{{GoodsType: 2, Quantity: 5}},
		Outputs:     []balance.RecipeLine{{GoodsType: 7, Quantity: 3, Max: 100}},
	}}
	b, err := balance.New(goods, recipes)
	require.NoError(t, err)
	require.Equal(t, 1, b.RecipeCount())

	r, ok := b.Recipe(1)
	require.True(t, ok)
	require.Equal(t, 30*time.Second, r.CycleTime)
	require.Len(t, r.Inputs, 1)
	require.EqualValues(t, 5, r.Inputs[0].Quantity)
	require.Len(t, r.Outputs, 1)
	require.EqualValues(t, 100, r.Outputs[0].Max)
}

func TestUnit_New_RejectsInvalidRecipes(t *testing.T) {
	goods := []balance.Goods{
		{ID: 2, Name: "Iron", Space: 1},
	}
	cases := []struct {
		name string
		r    balance.Recipe
		want error
	}{
		{
			"zero cycle",
			balance.Recipe{StationType: 1, CycleTime: 0, Outputs: []balance.RecipeLine{{GoodsType: 2, Quantity: 1}}},
			balance.ErrInvalidRecipeCycle,
		},
		{
			"no outputs",
			balance.Recipe{StationType: 1, CycleTime: time.Second},
			balance.ErrEmptyRecipeOutputs,
		},
		{
			"unknown goods",
			balance.Recipe{StationType: 1, CycleTime: time.Second, Outputs: []balance.RecipeLine{{GoodsType: 999, Quantity: 1}}},
			balance.ErrUnknownRecipeGoods,
		},
		{
			"non-positive qty",
			balance.Recipe{StationType: 1, CycleTime: time.Second, Outputs: []balance.RecipeLine{{GoodsType: 2, Quantity: 0}}},
			balance.ErrInvalidRecipeQuantity,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := balance.New(goods, []balance.Recipe{tc.r})
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestUnit_New_RejectsDuplicateRecipeStationType(t *testing.T) {
	goods := []balance.Goods{{ID: 2, Name: "Iron", Space: 1}}
	r := balance.Recipe{
		StationType: 1,
		CycleTime:   time.Second,
		Outputs:     []balance.RecipeLine{{GoodsType: 2, Quantity: 1}},
	}
	_, err := balance.New(goods, []balance.Recipe{r, r})
	require.ErrorIs(t, err, balance.ErrDuplicateRecipe)
}

// TestUnit_LoadFromFile_ParsesGoods verifies the real balance.yaml parses into
// the goods catalog. Production recipes moved to station_types.yaml (phase
// 8.15), so balance.yaml now carries no recipes — see
// TestUnit_LoadStationTypes_RealConfig for the recipe coverage.
func TestUnit_LoadFromFile_ParsesGoods(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "balance.yaml")
	b, err := balance.LoadFromFile(path)
	require.NoError(t, err)
	require.Greater(t, b.GoodsCount(), 1)
	require.Equal(t, 0, b.RecipeCount(), "recipes moved to station_types.yaml")
}

// TestUnit_LoadFromFile_ParsesRecipes keeps coverage of LoadFromFile's recipe
// branch with an inline fixture (independent of the shipped configs).
func TestUnit_LoadFromFile_ParsesRecipes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "balance.yaml")
	const doc = `goods_types:
    - {id: 2, name: Iron, space: 1}
    - {id: 7, name: Microchip, space: 1}
recipes:
    - station_type: 1
      cycle_time: 60s
      inputs:
          - {type: 2, qty: 5}
      outputs:
          - {type: 7, qty: 3, max: 1000}
`
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o644))

	b, err := balance.LoadFromFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, b.RecipeCount())
	r, ok := b.Recipe(1)
	require.True(t, ok, "station_type=1 recipe must parse")
	require.Equal(t, 60*time.Second, r.CycleTime)
}
