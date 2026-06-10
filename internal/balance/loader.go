package balance

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"spaceempire/back/internal/domain"
)

// yamlGoods mirrors the on-disk shape produced by
// cmd/starwind-tools/convert-balance.
type yamlGoods struct {
	ID            int    `yaml:"id"`
	Name          string `yaml:"name"`
	MinWarRate    int    `yaml:"min_war_rate"`
	MinTradeRate  int    `yaml:"min_trade_rate"`
	MinRaceRate   int    `yaml:"min_race_rate"`
	AvgPrice      int64  `yaml:"avg_price"`
	MaxPrice      int64  `yaml:"max_price"`
	ProductionStd int    `yaml:"production_std"`
	Space         int    `yaml:"space"`
	ObjectTypeID  int    `yaml:"object_type_id"`
}

type yamlRecipeLine struct {
	Type int   `yaml:"type"`
	Qty  int64 `yaml:"qty"`
	Max  int64 `yaml:"max"`
}

type yamlRecipe struct {
	StationType int              `yaml:"station_type"`
	Inputs      []yamlRecipeLine `yaml:"inputs"`
	Outputs     []yamlRecipeLine `yaml:"outputs"`
	CycleTime   string           `yaml:"cycle_time"`
}

type yamlFile struct {
	GoodsTypes []yamlGoods  `yaml:"goods_types"`
	Recipes    []yamlRecipe `yaml:"recipes"`
}

// LoadFromFile reads, parses and validates the YAML balance file at path.
// The returned *Balance is ready to inject; on error the file content is
// not partially exposed.
func LoadFromFile(path string) (*Balance, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("balance: read %s: %w", path, err)
	}

	var f yamlFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("balance: parse %s: %w", path, err)
	}

	goods := make([]Goods, 0, len(f.GoodsTypes))
	for _, g := range f.GoodsTypes {
		goods = append(goods, Goods{
			ID:            domain.GoodsTypeID(g.ID),
			Name:          g.Name,
			MinWarRate:    g.MinWarRate,
			MinTradeRate:  g.MinTradeRate,
			MinRaceRate:   g.MinRaceRate,
			AvgPrice:      g.AvgPrice,
			MaxPrice:      g.MaxPrice,
			ProductionStd: g.ProductionStd,
			Space:         g.Space,
			ObjectTypeID:  g.ObjectTypeID,
		})
	}

	recipes := make([]Recipe, 0, len(f.Recipes))
	for _, r := range f.Recipes {
		dur, err := time.ParseDuration(r.CycleTime)
		if err != nil {
			return nil, fmt.Errorf("balance: parse cycle_time for station_type=%d: %w", r.StationType, err)
		}
		recipes = append(recipes, Recipe{
			StationType: r.StationType,
			Inputs:      mapRecipeLines(r.Inputs),
			Outputs:     mapRecipeLines(r.Outputs),
			CycleTime:   dur,
		})
	}

	return New(goods, recipes)
}

func mapRecipeLines(in []yamlRecipeLine) []RecipeLine {
	if len(in) == 0 {
		return nil
	}
	out := make([]RecipeLine, len(in))
	for i, l := range in {
		out[i] = RecipeLine{
			GoodsType: domain.GoodsTypeID(l.Type),
			Quantity:  l.Qty,
			Max:       l.Max,
		}
	}
	return out
}
