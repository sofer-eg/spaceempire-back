// Package balance holds the static economy reference data (goods catalog
// today; ship classes / recipes will follow as the consuming phases land).
// The data lives in configs/balance.yaml and is converted from the legacy
// includes/types_prod.php by cmd/starwind-tools/convert-balance.
//
// A *Balance is read-only after construction. Build it once at startup,
// inject it into services that need price/space lookups.
package balance

import (
	"fmt"
	"time"

	"spaceempire/back/internal/domain"
)

// Goods is one row from goods_types — the merged view of legacy fields
// from types_prod.php. Prices are in credits per unit; space is the
// per-unit cargobay footprint in cubic meters.
//
// MinWarRate / MinTradeRate / MinRaceRate are gating thresholds inherited
// from the old game; nothing in the new code consumes them yet, but we
// keep them so seed scripts don't lose information.
type Goods struct {
	ID            domain.GoodsTypeID
	Name          string
	MinWarRate    int
	MinTradeRate  int
	MinRaceRate   int
	AvgPrice      int64
	MaxPrice      int64
	ProductionStd int
	Space         int
	ObjectTypeID  int
}

// RecipeLine is one input or output row of a production recipe.
// Quantity is the per-cycle delta; Max is the soft cap on the output
// stack — when the current cargo plus Quantity would exceed Max the
// cycle does not start. Max == 0 means "no cap".
type RecipeLine struct {
	GoodsType domain.GoodsTypeID
	Quantity  int64
	Max       int64
}

// Recipe describes a per-station-type production cycle. One full cycle
// consumes Inputs and yields Outputs after CycleTime has elapsed; see
// internal/economy/production for the runtime contract.
type Recipe struct {
	StationType int
	Inputs      []RecipeLine
	Outputs     []RecipeLine
	CycleTime   time.Duration
}

// Balance is the immutable in-memory catalog built from configs/balance.yaml.
type Balance struct {
	goodsByID   map[domain.GoodsTypeID]Goods
	goodsList   []Goods
	recipeByKey map[int]Recipe
	recipeList  []Recipe
}

// New validates the input and returns a *Balance, or an error wrapping one
// of the package sentinels.
func New(goods []Goods, recipes []Recipe) (*Balance, error) {
	byID := make(map[domain.GoodsTypeID]Goods, len(goods))
	for _, g := range goods {
		if g.ID <= 0 {
			return nil, fmt.Errorf("%w: %d", ErrInvalidGoodsID, g.ID)
		}
		if g.Name == "" {
			return nil, fmt.Errorf("%w: id=%d", ErrEmptyGoodsName, g.ID)
		}
		if g.Space < 0 || g.AvgPrice < 0 || g.MaxPrice < 0 {
			return nil, fmt.Errorf("%w: id=%d", ErrNegativeGoodsField, g.ID)
		}
		if _, dup := byID[g.ID]; dup {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateGoodsID, g.ID)
		}
		byID[g.ID] = g
	}

	recipeByKey := make(map[int]Recipe, len(recipes))
	for _, r := range recipes {
		if r.CycleTime <= 0 {
			return nil, fmt.Errorf("%w: station_type=%d", ErrInvalidRecipeCycle, r.StationType)
		}
		if len(r.Outputs) == 0 {
			return nil, fmt.Errorf("%w: station_type=%d", ErrEmptyRecipeOutputs, r.StationType)
		}
		if err := validateRecipeLines(byID, r.Inputs); err != nil {
			return nil, fmt.Errorf("recipe station_type=%d inputs: %w", r.StationType, err)
		}
		if err := validateRecipeLines(byID, r.Outputs); err != nil {
			return nil, fmt.Errorf("recipe station_type=%d outputs: %w", r.StationType, err)
		}
		if _, dup := recipeByKey[r.StationType]; dup {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateRecipe, r.StationType)
		}
		recipeByKey[r.StationType] = r
	}

	list := make([]Goods, len(goods))
	copy(list, goods)
	recipeList := make([]Recipe, len(recipes))
	copy(recipeList, recipes)

	return &Balance{
		goodsByID:   byID,
		goodsList:   list,
		recipeByKey: recipeByKey,
		recipeList:  recipeList,
	}, nil
}

// validateRecipeLines enforces that every referenced goods id exists and
// quantities are positive.
func validateRecipeLines(goodsByID map[domain.GoodsTypeID]Goods, lines []RecipeLine) error {
	for _, l := range lines {
		if _, ok := goodsByID[l.GoodsType]; !ok {
			return fmt.Errorf("%w: %d", ErrUnknownRecipeGoods, l.GoodsType)
		}
		if l.Quantity <= 0 {
			return fmt.Errorf("%w: goods=%d", ErrInvalidRecipeQuantity, l.GoodsType)
		}
		if l.Max < 0 {
			return fmt.Errorf("%w: goods=%d", ErrInvalidRecipeMax, l.GoodsType)
		}
	}
	return nil
}

// Get returns the Goods row for id and true, or zero value and false when
// the id is unknown.
func (b *Balance) Get(id domain.GoodsTypeID) (Goods, bool) {
	g, ok := b.goodsByID[id]
	return g, ok
}

// GoodsCount is the number of distinct goods loaded.
func (b *Balance) GoodsCount() int { return len(b.goodsList) }

// AllGoods returns every loaded goods row, ordered as the underlying YAML.
// The returned slice is a defensive copy so callers cannot mutate the
// in-memory catalog.
func (b *Balance) AllGoods() []Goods {
	out := make([]Goods, len(b.goodsList))
	copy(out, b.goodsList)
	return out
}

// Recipe returns the production recipe registered for stationType, or
// zero value and false when no recipe is defined.
func (b *Balance) Recipe(stationType int) (Recipe, bool) {
	r, ok := b.recipeByKey[stationType]
	return r, ok
}

// RecipeCount is the number of recipes loaded.
func (b *Balance) RecipeCount() int { return len(b.recipeList) }
